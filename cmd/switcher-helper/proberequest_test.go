package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
)

// Exercise the real HTTP server read deadline without binding any local port.
type probePipeListener struct {
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *probePipeListener) Accept() (net.Conn, error) {
	if l.conn != nil {
		c := l.conn
		l.conn = nil
		return c, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *probePipeListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (*probePipeListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestProbeBodyDeadline(t *testing.T) {
	for _, mode := range []string{"stalled_upload", "slow_response"} {
		t.Run(mode, func(t *testing.T) {
			serverConn, client := net.Pipe()
			defer client.Close()
			listener := &probePipeListener{conn: serverConn, closed: make(chan struct{})}
			outcome := make(chan string, 1)
			server := probeHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := readProbeBody(w, r)
				if err != nil {
					status, code := probeBodyRejection(err)
					http.Error(w, code, status)
					outcome <- code
					return
				}
				time.Sleep(80 * time.Millisecond)
				io.WriteString(w, "complete")
				outcome <- "complete"
			}))
			if server.ReadTimeout != 30*time.Second || server.WriteTimeout != 0 {
				t.Fatal("wrong upload/stream limits")
			}
			server.ReadTimeout = 40 * time.Millisecond
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			defer func() { server.Close(); <-done }()
			client.SetDeadline(time.Now().Add(2 * time.Second))
			length := "4"
			if mode == "slow_response" {
				length = "1"
			}
			if _, err := io.WriteString(client, "POST /responses HTTP/1.1\r\nHost: synthetic\r\nConnection: close\r\nContent-Length: "+length+"\r\n\r\nx"); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(client), nil)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			code := <-outcome
			if mode == "stalled_upload" {
				if response.StatusCode != 408 || code != "request_body_timeout" {
					t.Fatal("stalled body did not time out")
				}
			} else if response.StatusCode != 200 || string(data) != "complete" {
				t.Fatal("body deadline truncated response")
			}
		})
	}
}

type probeFailedBody struct{ err error }

func (b probeFailedBody) Read([]byte) (int, error) { return 0, b.err }
func (probeFailedBody) Close() error               { return nil }

func TestAuxiliaryBodyFailureReleasesAndNeverReplays(t *testing.T) {
	for _, failure := range []error{os.ErrDeadlineExceeded, &http.MaxBytesError{Limit: 4 << 20}, io.ErrUnexpectedEOF} {
		a := &probeAuxiliary{binding: probeAuxiliaryBinding{Thread: "synthetic", Slot: "a"}}
		var mu sync.Mutex
		r := httptest.NewRequest("POST", "/responses", nil)
		r.Body = probeFailedBody{failure}
		w := httptest.NewRecorder()
		a.serve(w, r, &mu, newHistoryAccess(), "http://127.0.0.1:1", "synthetic", func() bool { return true }, nil)
		status, code := probeBodyRejection(failure)
		if w.Code != status || !strings.Contains(w.Body.String(), code) || a.busy || a.pending || !a.failed || a.handler != nil {
			t.Fatal("body failure dispatched, retained busy state, or lost diagnostic")
		}
		r = httptest.NewRequest("POST", "/responses", strings.NewReader(historyFirst))
		w = httptest.NewRecorder()
		a.serve(w, r, &mu, newHistoryAccess(), "http://127.0.0.1:1", "synthetic", func() bool { return true }, nil)
		if w.Code != 409 || a.handler != nil {
			t.Fatal("failed body request replayed")
		}
	}
}

func TestProbeAuthenticationPrevalidationDiagnostic(t *testing.T) {
	source := newRefreshHistorySource(t)
	source.exchange.failure = "network"
	if _, err := source.Manager.RequestAccess("a", time.Now().Add(2*time.Hour)); !errors.Is(err, accounts.ErrRefresh) {
		t.Fatal("fixture refresh did not fail")
	}
	if _, err := probeHistoryCredential(source, "a"); !errors.Is(err, accounts.ErrRefresh) {
		t.Fatal("prevalidation swallowed refresh failure")
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{accounts.ErrRefresh, 401, "authentication_expired"},
		{accounts.ErrExpired, 401, "authentication_expired"},
		{accounts.ErrStore, 503, "credential_store_unavailable"},
		{accounts.ErrNotRegistered, 401, "account_unavailable"},
		{errors.New("synthetic-secret"), 401, "session_unavailable"},
	} {
		status, code := probeAuthenticationRejection(tc.err)
		if status != tc.status || code != tc.code {
			t.Fatal("incorrect authentication diagnostic")
		}
	}
}

func TestProbeAuxiliaryCapacityAdmission(t *testing.T) {
	if probeAuxiliaryAdmission(false, false, 127) != "" || probeAuxiliaryAdmission(false, false, 128) != "auxiliary_capacity_reached" || probeAuxiliaryAdmission(true, false, 128) != "probe_auxiliary_unavailable" {
		t.Fatal("capacity confused with failed session or limit bypassed")
	}
}
