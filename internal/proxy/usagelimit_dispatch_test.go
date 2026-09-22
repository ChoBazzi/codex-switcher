package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type quotaRoundTrip func(*http.Request) (*http.Response, error)

func (f quotaRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Run the real dispatch/observer pipeline with an in-memory HTTP transport.
// This verifies suppression and failure gates without binding any local port.
func TestUsageLimitDispatch(t *testing.T) {
	for _, mode := range []string{"disabled", "json", "sse", "rate", "partial", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			h, err := New("http://127.0.0.1/responses", func(*http.Request) (Identity, error) { return Identity{Session: "synthetic", Token: "synthetic"}, nil })
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			h.DeferUsageLimit = mode != "disabled"
			var calls atomic.Int32
			h.transport.RegisterProtocol("http", quotaRoundTrip(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				status, format, body := 429, "application/json", `{"error":{"type":"usage_limit_reached"}}`
				if mode == "rate" {
					body = `{"error":{"code":"rate_limit_exceeded"}}`
				}
				if mode == "sse" || mode == "partial" {
					status, format = 200, "text/event-stream"
					body = "data: {\"type\":\"error\",\"code\":\"usage_limit_reached\"}\n\n"
					if mode == "partial" {
						body = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" + body
					}
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{format}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			request := httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":[]}`))
			if mode == "cancel" {
				ctx, cancel := context.WithCancel(request.Context())
				cancel()
				request = request.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			func() {
				defer func() {
					if p := recover(); p != nil && p != http.ErrAbortHandler {
						panic(p)
					}
				}()
				h.ServeHTTP(w, request)
			}()
			d := h.Diagnostics()
			if mode == "json" || mode == "sse" {
				if !d.UsageLimit || d.Status != 429 || w.Body.Len() != 0 || w.Flushed {
					t.Fatal("recognized limit leaked output or was not classified")
				}
			} else if d.UsageLimit {
				t.Fatal("generic/partial/canceled error allowed failover")
			}
			if mode == "partial" && !strings.Contains(w.Body.String(), "partial") {
				t.Fatal("stream changed")
			}
			before := calls.Load()
			if mode != "cancel" {
				again := httptest.NewRecorder()
				h.ServeHTTP(again, httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":[]}`)))
				if again.Code != 409 || calls.Load() != before {
					t.Fatal("low-level handler replayed on its own")
				}
			} else if before != 0 {
				t.Fatal("canceled request dispatched")
			}
		})
	}
}
