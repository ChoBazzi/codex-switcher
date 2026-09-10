package cliprobe

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
)

// This establishes the local-history prerequisite, NOT cross-account or
// same-process TUI compatibility. No real credentials or transcripts are read.
func TestInstalledCodexLocalHistoryPayload(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("opt-in installed CLI test; synthetic loopback only")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("codex executable required")
	}
	const marker = "synthetic-memory-blue-apple-7391"
	type observation struct{ user, assistant, previousResponse bool }
	var mu sync.Mutex
	var observations []observation
	fixture := &Upstream{Scenario: "success"}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		r.Body.Close()
		if err != nil {
			w.WriteHeader(413)
			return
		}
		var payload struct {
			Input []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
			Previous string `json:"previous_response_id"`
		}
		if json.Unmarshal(body, &payload) != nil {
			w.WriteHeader(400)
			return
		}
		o := observation{previousResponse: payload.Previous != ""}
		for _, item := range payload.Input {
			if item.Role == "user" && strings.Contains(string(item.Content), marker) {
				o.user = true
			}
			if item.Role == "assistant" && strings.Contains(string(item.Content), Reply) {
				o.assistant = true
			}
		}
		mu.Lock()
		observations = append(observations, o)
		mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		fixture.ServeHTTP(w, r)
	}))
	defer up.Close()
	h, err := proxy.New(up.URL, func(r *http.Request) (proxy.Identity, error) {
		id, err := cliidentity.ThreadID(r.Header)
		return proxy.Identity{Session: id, Token: "synthetic-only"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	p := httptest.NewServer(h)
	defer p.Close()
	root, dir := t.TempDir(), t.TempDir()
	profile, err := Profile(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "switcher-probe.config.toml"), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := invokeCLI(binary, root, dir, "Remember "+marker+". Do not use tools.")
	id := startedID(out)
	if err != nil || id == "" {
		t.Fatal("first turn failed; raw output omitted")
	}
	out, err = invokeCLI(binary, root, dir, "resume", id, "What was the remembered word? Do not use tools.")
	if err != nil || startedID(out) != id {
		t.Fatal("resume failed; raw output omitted")
	}
	out, err = invokeCLI(binary, root, dir, "New unrelated conversation. Do not use tools.")
	if err != nil || startedID(out) == "" || startedID(out) == id {
		t.Fatal("new conversation failed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observations) != 3 || fixture.Calls.Load() != 3 {
		t.Fatal("expected exactly three requests; no replay")
	}
	if !observations[0].user || observations[0].assistant {
		t.Fatal("invalid first-turn control")
	}
	if !observations[1].user || !observations[1].assistant {
		t.Fatal("resume did not include prior user and assistant text")
	}
	if observations[2].user || observations[2].assistant {
		t.Fatal("history leaked into new conversation")
	}
	t.Logf("resume_user_history=true resume_assistant_history=true previous_response_reference=%t new_conversation_isolated=true; NOT a live account-switch test", observations[1].previousResponse)
}
