package cliprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// An unknown synthetic function never invokes a real tool. This probes CLI wire
// shape only; it is not an end-to-end persistent-proxy launcher test.
func TestInstalledCodexFunctionOutput(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("opt in to synthetic CLI probe")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var found atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n > 2 {
			w.WriteHeader(409)
			return
		}
		var body struct {
			Input []map[string]json.RawMessage `json:"input"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		if n == 2 {
			for _, item := range body.Input {
				var kind, call string
				json.Unmarshal(item["type"], &kind)
				json.Unmarshal(item["call_id"], &call)
				if kind == "function_call_output" && call == "call_synthetic_unknown" {
					var text string
					if json.Unmarshal(item["output"], &text) == nil && text != "" {
						found.Store(true)
					}
				}
			}
			// Body has been consumed; the fixture only drains it before responding.
			(&Upstream{Scenario: "success"}).ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"type": "function_call", "id": "fc_synthetic_unknown", "call_id": "call_synthetic_unknown", "name": "switcher_synthetic_nonexistent_tool", "arguments": "{}", "status": "completed"}
		for _, event := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp_synthetic_tool", "status": "in_progress", "output": []any{}}},
			{"type": "response.output_item.added", "output_index": 0, "item": item},
			{"type": "response.output_item.done", "output_index": 0, "item": item},
			{"type": "response.completed", "response": map[string]any{"id": "resp_synthetic_tool", "status": "completed", "output": []any{item}}},
		} {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
	}))
	defer up.Close()
	root := t.TempDir()
	profile, err := Profile(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "switcher-probe.config.toml"), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "exec", "--strict-config", "--ephemeral", "--skip-git-repo-check", "--profile", "switcher-probe", "--json", "Synthetic protocol probe only.")
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir(), "CODEX_HOME=" + root, "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
	if _, err := cmd.CombinedOutput(); err != nil {
		t.Fatal("synthetic tool probe failed; raw output omitted")
	}
	if calls.Load() != 2 || !found.Load() {
		t.Fatalf("unexpected function-output shape: calls=%d recognized=%t", calls.Load(), found.Load())
	}
}
