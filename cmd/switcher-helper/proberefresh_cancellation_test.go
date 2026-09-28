package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
)

type cancellationProbeSource struct {
	*refreshHistorySource
	watch   atomic.Bool
	entered chan struct{}
}

func (s *cancellationProbeSource) RequestAccessContext(ctx context.Context, slot string, now time.Time) (accounts.Access, error) {
	if s.watch.Load() {
		s.entered <- struct{}{}
	}
	return s.refreshHistorySource.RequestAccessContext(ctx, slot, now)
}

// Exercise the auxiliary handler without sockets as well as through the managed
// HTTP integration below, so cancellation and cleanup can run in a sandbox.
func TestAuxiliaryCanceledAuthenticationWait(t *testing.T) {
	source := &cancellationProbeSource{refreshHistorySource: newRefreshHistorySource(t), entered: make(chan struct{}, 1)}
	source.exchange.entered, source.exchange.release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(source.exchange.release) }) }
	defer unblock()
	source.elapsed.Store(int64(2 * time.Hour))
	refreshed := make(chan error, 1)
	go func() {
		_, err := source.refreshHistorySource.RequestAccessContext(context.Background(), "a", time.Now())
		refreshed <- err
	}()
	select {
	case <-source.exchange.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("exchange did not start")
	}
	source.watch.Store(true)
	a := &probeAuxiliary{binding: probeAuxiliaryBinding{Thread: "synthetic-child", Slot: "a"}}
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r := httptest.NewRequest("POST", "/responses", strings.NewReader(historyFirst)).WithContext(ctx)
		a.serve(w, r, &mu, source, "http://127.0.0.1:1", "synthetic-salt", func() bool { return true }, nil)
		close(done)
	}()
	select {
	case <-source.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("auxiliary did not enter authentication wait")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("auxiliary waited for exchange after cancellation")
	}
	defer a.handler.Close()
	if w.Code != 408 || a.busy || a.pending || !a.failed || a.handler.Diagnostics().Rejection != "request_canceled" || a.handler.Diagnostics().Attempts != 0 {
		t.Fatal("canceled auxiliary did not release busy state or prevent dispatch")
	}
	w = httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("POST", "/responses", strings.NewReader(historyFirst)), &mu, source, "http://127.0.0.1:1", "synthetic-salt", func() bool { return true }, nil)
	if w.Code != 409 || a.handler.Diagnostics().Attempts != 0 {
		t.Fatal("canceled auxiliary replayed")
	}
	unblock()
	if err := <-refreshed; err != nil || source.exchange.calls.Load() != 1 {
		t.Fatal("cancellation interrupted or repeated exchange")
	}
}

func TestProbeCanceledAuthenticationWait(t *testing.T) {
	for _, mode := range []string{"root", "auxiliary"} {
		t.Run(mode, func(t *testing.T) {
			source := &cancellationProbeSource{refreshHistorySource: newRefreshHistorySource(t), entered: make(chan struct{}, 1)}
			source.exchange.entered, source.exchange.release = make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(source.exchange.release) }) }
			defer unblock()
			var calls atomic.Int32
			up := historyUpstream(&calls)
			defer up.Close()
			p := startHistoryProbe(t, source, up.URL, t.TempDir())
			if p.send(t, "/responses", historyFirst) != 200 {
				t.Fatal("initial root request failed")
			}
			thread := "12345678-1234-4234-8234-123456789012"
			body, event := historyFollowup, "probe_request_finished"
			if mode == "auxiliary" {
				thread = "23456789-2345-4345-8345-234567890123"
				body, event = historyFirst, "probe_auxiliary_finished"
			}
			owner, _ := source.HistoryCredential("a")
			source.elapsed.Store(int64(2 * time.Hour))
			refreshed := make(chan error, 1)
			go func() {
				_, err := source.refreshHistorySource.RequestAccessContext(context.Background(), "a", time.Now())
				refreshed <- err
			}()
			select {
			case <-source.exchange.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("exchange did not start")
			}
			source.watch.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", p.address+"/responses", strings.NewReader(body))
			req.Header.Set("Thread-Id", thread)
			req.Header.Set("Session-Id", "12345678-1234-4234-8234-123456789012")
			req.Header.Set("X-Switcher-Run", p.secret)
			requestDone := make(chan error, 1)
			go func() {
				resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
				if resp != nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				requestDone <- err
			}()
			select {
			case <-source.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("model request did not enter authentication wait")
			}
			cancel()
			if err := <-requestDone; !errors.Is(err, context.Canceled) {
				t.Fatal("client request did not cancel")
			}
			diagnostic := p.next(t, event)
			if diagnostic["local_rejection_code"] != "request_canceled" || diagnostic["last_http_status"] != float64(408) {
				t.Fatalf("wrong cancellation diagnostic: %v", diagnostic)
			}
			if _, err := io.WriteString(p.commands, "{\"action\":\"status\"}\n"); err != nil {
				t.Fatal(err)
			}
			for p.next(t, "probe_state")["busy"] != false {
			}
			// Cancellation must finish before the unrelated exchange is released,
			// and neither this request nor its replay may dispatch a model call.
			if p.sendThread(t, "/responses", body, thread) != 409 || calls.Load() != 1 {
				t.Fatal("canceled model request dispatched or replayed")
			}
			unblock()
			if err := <-refreshed; err != nil {
				t.Fatal(err)
			}
			restored := accounts.NewRefreshing(source.vault, source.dir, source.exchange)
			access, err := restored.RequestAccess("a", time.Now())
			if err != nil || access.Token != source.exchange.next.AccessToken || access.HistoryCredential() != owner || source.exchange.calls.Load() != 1 || calls.Load() != 1 {
				t.Fatal("cancellation lost the committed refresh or replayed work")
			}
		})
	}
}
