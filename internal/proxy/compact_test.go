package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCompactEndpointSingleAttempt(t *testing.T) {
	for _, mode := range []string{"ok", "missing_type", "invalid", "oversize", "reject", "failure", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/responses/compact" || r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("Cookie") != "" {
					t.Error("compact routing/auth isolation")
				}
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
				if mode == "failure" {
					http.Error(w, "synthetic-upstream-private-error", 429)
					return
				}
				if mode == "redirect" {
					w.Header().Set("Location", "/elsewhere")
					w.WriteHeader(302)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if mode == "missing_type" {
					w.Header()["Content-Type"] = nil
				}
				if mode == "invalid" {
					io.WriteString(w, "{")
					return
				}
				if mode == "oversize" {
					io.WriteString(w, strings.Repeat(" ", (4<<20)+1))
					return
				}
				io.WriteString(w, `{"object":"response.compaction","output":[]}`)
			}))
			defer up.Close()
			h, err := New(up.URL+"/responses", func(*http.Request) (Identity, error) {
				return Identity{Session: "synthetic-session", Token: "synthetic"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			h.ValidateCompaction = func(raw []byte) error {
				if mode == "reject" || !json.Valid(raw) {
					return errors.New("invalid")
				}
				return nil
			}
			r := httptest.NewRequest("POST", "/responses/compact", strings.NewReader(`{"input":[]}`))
			r.Header.Set("Cookie", "private-client-cookie")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 502
			if mode == "ok" || mode == "missing_type" {
				want = 200
			}
			if mode == "failure" {
				want = 429
			}
			if w.Code != want || calls.Load() != 1 {
				t.Fatalf("status=%d calls=%d", w.Code, calls.Load())
			}
			if strings.Contains(w.Body.String(), "synthetic-upstream-private-error") {
				t.Fatal("raw error leaked")
			}
			if want != 200 {
				w = httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("POST", "/responses/compact", strings.NewReader(`{}`)))
				if calls.Load() != 1 {
					t.Fatal("failed compact retried")
				}
			}
		})
	}
}
