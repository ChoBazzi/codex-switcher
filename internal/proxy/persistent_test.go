//go:build darwin && cgo

package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

func persistentOrigin(id string) checkpoint.Origin {
	return checkpoint.Origin{Project: "synthetic-project", Worktree: "synthetic-worktree", Branch: "dev", Session: id}
}
func persistentHandler(t *testing.T, db *affinity.Store, url, id, slot string) *Handler {
	t.Helper()
	h, err := NewPersistent(url, func(*http.Request) (Identity, error) {
		return Identity{Session: id, Origin: persistentOrigin(id), Slot: slot, Token: "synthetic-token", AccountID: "synthetic-account-" + slot}, nil
	}, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}
func invoke(h *Handler, body string) (status int, aborted bool) {
	w := httptest.NewRecorder()
	defer func() {
		if p := recover(); p != nil {
			if p != http.ErrAbortHandler {
				panic(p)
			}
			aborted = true
		}
		status = w.Code
	}()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/responses", strings.NewReader(body)))
	return w.Code, false
}
func TestPersistentProxyOwnershipAndRestart(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "private")
			db, err := affinity.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			for id, slot := range map[string]string{"one": "a", "two": "b"} {
				if _, err := db.Register(persistentOrigin(id), slot, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if r.Header.Get("ChatGPT-Account-ID") != "synthetic-account-a" {
					t.Error("cross-account forwarding")
				}
				response := fmt.Sprintf(`{"id":"synthetic-response-%d","status":"completed"}`, n)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
				} else {
					fmt.Fprint(w, response)
				}
			}))
			defer up.Close()
			h := persistentHandler(t, db, up.URL, "one", "a")
			if status, abort := invoke(h, `{"input":"hello"}`); status != 200 || abort {
				t.Fatal("first request failed")
			}
			h.Close()
			db.Close()
			db, err = affinity.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			h = persistentHandler(t, db, up.URL, "one", "a")
			if status, abort := invoke(h, `{"input":"continue","previous_response_id":"synthetic-response-1"}`); status != 200 || abort {
				t.Fatal("owned continuation failed after restart")
			}
			other := persistentHandler(t, db, up.URL, "two", "b")
			if status, _ := invoke(other, `{"input":"continue","previous_response_id":"synthetic-response-1"}`); status != 409 {
				t.Fatal("cross-account reference accepted")
			}
			if calls.Load() != 2 {
				t.Fatal("rejected request reached upstream")
			}
		})
	}
}
func TestPersistentProxyFailureSurvivesRestart(t *testing.T) {
	for _, scenario := range []string{"429", "partial", "invalid-completed", "invalid-json"} {
		t.Run(scenario, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "private")
			db, err := affinity.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			db.Register(persistentOrigin("one"), "a", time.Now())
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch scenario {
				case "429":
					w.WriteHeader(429)
				case "partial":
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.created\"}\n\n")
				case "invalid-completed":
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
				case "invalid-json":
					fmt.Fprint(w, `{"id":"bad","status":"incomplete"}`)
				}
			}))
			defer up.Close()
			h := persistentHandler(t, db, up.URL, "one", "a")
			invoke(h, `{"input":"hello"}`)
			h.Close()
			db.Close()
			db, err = affinity.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			h = persistentHandler(t, db, up.URL, "one", "a")
			if status, _ := invoke(h, `{"input":"hello"}`); status != 409 {
				t.Fatal("failure replayed after restart")
			}
			if calls.Load() != 1 {
				t.Fatal("duplicate upstream request")
			}
		})
	}
}

func TestContinuationContract(t *testing.T) {
	if _, err := continuationRefs([]byte(`{"input":"x","previous_response_id":"one","previous_response_id":"two"}`)); err == nil {
		t.Fatal("duplicate continuation key accepted")
	}
	for _, body := range []string{`null`, `{"input":null}`, `{"input":"x","conversation":"other"}`, `{"input":[{"type":"item_reference"}]}`, `{"input":[{"role":"assistant","content":"x"}]}`, `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`, `{"input":"x","previous_response_id":123}`} {
		if _, err := continuationRefs([]byte(body)); err == nil {
			t.Fatalf("accepted unsupported shape %s", body)
		}
	}
	for _, body := range []string{`{"input":"hello"}`, `{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`, `{"input":"next","previous_response_id":"owned"}`} {
		if _, err := continuationRefs([]byte(body)); err != nil {
			t.Fatal("supported shape rejected")
		}
	}
}
