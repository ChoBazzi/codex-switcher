package livetest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/cliprobe"
)

const testSecret = "synthetic-run-secret"

func testAccess() accounts.Access {
	return accounts.Access{Token: "synthetic-access", AccountID: "synthetic-account", ExpiresAt: time.Now().Add(time.Hour)}
}

func TestInstalledCLI(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("opt-in installed CLI with synthetic credentials and upstream")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("codex required")
	}
	for _, scenario := range []string{"success", "rate-limit", "server-error", "partial"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := &cliprobe.Upstream{Scenario: scenario}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error("request unreadable")
				}
				var fields map[string]json.RawMessage
				if json.Unmarshal(data, &fields) != nil || string(fields["stream"]) != "true" || string(fields["store"]) != "false" {
					t.Error("expected streaming without response storage")
				}
				r.Body = io.NopCloser(bytes.NewReader(data))
				if r.Header.Get("Authorization") != "Bearer synthetic-access" || r.Header.Get("ChatGPT-Account-ID") != "synthetic-account" {
					t.Error("account credentials not injected")
				}
				if r.Header.Get("X-Switcher-Run") != "" {
					t.Error("local secret sent upstream")
				}
				fixture.ServeHTTP(w, r)
			}))
			defer up.Close()
			b, err := New(up.URL, testSecret, testAccess())
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			p := httptest.NewServer(b)
			defer p.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			parent := t.TempDir()
			answer, err := Run(ctx, binary, parent, p.URL, testSecret, "switcher-synthetic")
			if scenario == "success" {
				if err != nil || answer != cliprobe.Reply {
					t.Fatal("CLI success not recognized", err)
				}
			} else if err == nil {
				t.Fatal("failed request accepted")
			}
			if b.Requests.Load() != 1 || b.Forwarded.Load() != 1 || fixture.Calls.Load() != 1 {
				t.Fatalf("wrong request counts: CLI=%d admitted=%d upstream=%d", b.Requests.Load(), b.Forwarded.Load(), fixture.Calls.Load())
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatal("temporary CLI data not cleaned")
			}
		})
	}
}

func TestBridgeRejectsContinuationAndMultipleRequests(t *testing.T) {
	for _, body := range []string{`{"input":[],"previous_response_id":"synthetic-old"}`, `{"input":[{"type":"item_reference","id":"synthetic-old"}]}`, `{"input":[],"conversation":"synthetic-old"}`, `{"input":[{"role":"assistant","content":"synthetic-old"}]}`} {
		up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("continuation forwarded") }))
		b, _ := New(up.URL, testSecret, testAccess())
		w := httptest.NewRecorder()
		b.ServeHTTP(w, testRequest(body))
		if w.Code != 400 || b.Forwarded.Load() != 0 {
			t.Fatal("continuation accepted")
		}
		b.Close()
		up.Close()
	}
	fixture := &cliprobe.Upstream{Scenario: "success"}
	up := httptest.NewServer(fixture)
	defer up.Close()
	b, _ := New(up.URL, testSecret, testAccess())
	defer b.Close()
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		r := testRequest(`{"input":[{"role":"user","content":"synthetic"}]}`)
		if i == 1 {
			r.Header.Set("Thread-Id", "22222222-2222-4222-8222-222222222222")
			r.Header.Set("Session-Id", "22222222-2222-4222-8222-222222222222")
		}
		b.ServeHTTP(w, r)
		if i == 0 && w.Code != 200 || i == 1 && w.Code != 409 {
			t.Fatal("wrong request gate result")
		}
	}
	if fixture.Calls.Load() != 1 {
		t.Fatal("new thread bypassed one-request limit")
	}
}

func testRequest(body string) *http.Request {
	r := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
	r.Header.Set("X-Switcher-Run", testSecret)
	r.Header.Set("Thread-Id", "11111111-1111-4111-8111-111111111111")
	r.Header.Set("Session-Id", "11111111-1111-4111-8111-111111111111")
	return r
}

func TestBridgeRejectsBadAuthenticationAndExpiry(t *testing.T) {
	for _, kind := range []string{"secret", "identity", "expired", "upgrade"} {
		t.Run(kind, func(t *testing.T) {
			access := testAccess()
			if kind == "expired" {
				access.ExpiresAt = time.Now().Add(-time.Hour)
			}
			b, _ := New("http://127.0.0.1:1", testSecret, access)
			defer b.Close()
			r := testRequest(`{"input":[]}`)
			switch kind {
			case "secret":
				r.Header.Del("X-Switcher-Run")
			case "identity":
				r.Header.Del("Thread-Id")
			case "upgrade":
				r.Header.Set("Upgrade", "websocket")
			}
			w := httptest.NewRecorder()
			b.ServeHTTP(w, r)
			if w.Code < 400 || b.Forwarded.Load() != 0 {
				t.Fatal("invalid request admitted")
			}
		})
	}
}

func TestProfileAndBoundedOutput(t *testing.T) {
	if _, err := Profile("https://example.com", testSecret, ""); err == nil {
		t.Fatal("remote CLI endpoint accepted")
	}
	profile, err := Profile("http://127.0.0.1:8765", testSecret, "synthetic-model")
	if err != nil || !strings.Contains(profile, "stream_max_retries = 0") || !strings.Contains(profile, "request_max_retries = 0") {
		t.Fatal("unsafe profile")
	}
	var out boundedOutput
	n, err := io.WriteString(&out, strings.Repeat("x", (1<<20)+1))
	if err != nil || n != (1<<20)+1 || !out.overflow || out.data.Len() != 1<<20 {
		t.Fatal("output not bounded")
	}
}
