package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanceledRequestNeverDispatches(t *testing.T) {
	for _, stage := range []string{"before_resolve", "after_resolve", "before_attempt"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resolved := 0
			h, err := New("http://127.0.0.1:1", func(*http.Request) (Identity, error) {
				resolved++
				if stage == "after_resolve" {
					cancel()
				}
				return Identity{Session: "synthetic", Token: "synthetic-token"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			if stage == "before_resolve" {
				cancel()
			}
			if stage == "before_attempt" {
				h.BeforeAttempt = func() error { cancel(); return nil }
			}
			r := httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":"synthetic"}`)).WithContext(ctx)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 408 || h.Diagnostics().Rejection != "request_canceled" || h.Diagnostics().Attempts != 0 {
				t.Fatal("canceled request counted or dispatched an upstream attempt")
			}
			if stage == "before_resolve" && resolved != 0 {
				t.Fatal("pre-canceled request resolved credentials")
			}
		})
	}
}
