package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func bridge(t *testing.T, target string) *httptest.Server {
	t.Helper()
	h, err := New(target, func(r *http.Request) (Identity, error) {
		return Identity{Session: "local-session", Token: "synthetic-token", AccountID: "synthetic-account"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p := httptest.NewServer(h)
	t.Cleanup(func() { p.Close(); h.Close() })
	return p
}

func request(t *testing.T, endpoint string) *http.Response {
	t.Helper()
	r, _ := http.NewRequest("POST", endpoint+"/responses", strings.NewReader(`{"input":"synthetic"}`))
	r.Header.Set("Authorization", "Bearer incoming-secret")
	r.Header.Set("ChatGPT-Account-ID", "incoming-account")
	r.Header.Set("Cookie", "incoming-cookie")
	r.Header.Set("Idempotency-Key", "incoming-key")
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestErrorsNeverReplayAndBlockSession(t *testing.T) {
	for _, status := range []int{401, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-token" || r.Header.Get("ChatGPT-Account-ID") != "synthetic-account" {
					t.Error("auth not replaced")
				}
				if r.Header.Get("Cookie") != "" || r.Header.Get("Idempotency-Key") != "" {
					t.Error("sensitive incoming headers leaked")
				}
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"code":"synthetic"}}`)
			}))
			defer up.Close()
			p := bridge(t, up.URL)
			r := request(t, p.URL)
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			if r.StatusCode != status {
				t.Fatal(r.StatusCode)
			}
			r = request(t, p.URL)
			r.Body.Close()
			if r.StatusCode != 409 || calls.Load() != 1 {
				t.Fatalf("replayed: %d calls %d", r.StatusCode, calls.Load())
			}
		})
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Redirect(w, r, "/other", 307) }))
	defer up.Close()
	r := request(t, bridge(t, up.URL).URL)
	defer r.Body.Close()
	if r.StatusCode != 502 || calls.Load() != 1 {
		t.Fatal("redirect followed")
	}
}

func TestSSEFlushBeforeUpstreamCompletion(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer up.Close()
	p := bridge(t, up.URL)
	r := request(t, p.URL)
	b := make([]byte, 5)
	_, err := io.ReadFull(r.Body, b)
	close(release)
	if err != nil || string(b) != "data:" {
		t.Fatal("SSE buffered until completion", err)
	}
	_, err = io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPartialStreamAbortsWithoutJSONOrReplay(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\"}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer up.Close()
	p := bridge(t, up.URL)
	r := request(t, p.URL)
	b, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err == nil || r.StatusCode != 200 || strings.Contains(string(b), "upstream_transport_error") {
		t.Fatal("partial stream rewritten as HTTP error", err)
	}
	r = request(t, p.URL)
	r.Body.Close()
	if r.StatusCode != 409 || calls.Load() != 1 {
		t.Fatal("partial stream replayed")
	}
}

func TestCancelledClientCancelsUpstream(t *testing.T) {
	done := make(chan struct{})
	stop := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		io.WriteString(w, ": ping\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(done)
		case <-stop:
		}
	}))
	defer up.Close()
	defer close(stop)
	p := bridge(t, up.URL)
	r := request(t, p.URL)
	r.Body.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not cancelled")
	}
}

func TestEventObserverSplitFramesAndFailures(t *testing.T) {
	for _, kind := range []string{"response.completed", "response.failed", "response.incomplete", "error"} {
		t.Run(kind, func(t *testing.T) {
			o := eventObserver{}
			frame := "data: {\"type\":\"" + kind + "\"}\r\n\r\n"
			for i := range frame {
				o.feed([]byte{frame[i]})
			}
			if o.completed != (kind == "response.completed") || o.failed != (kind != "response.completed") {
				t.Fatal("terminal event was not recognized across chunks")
			}
		})
	}
	o := eventObserver{}
	if o.feed(make([]byte, (1<<20)+1)) {
		t.Fatal("oversized frame accepted")
	}
}
