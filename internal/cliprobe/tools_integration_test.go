//go:build darwin && cgo

package cliprobe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/clisession"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

// An unknown synthetic function never invokes a real tool. This probes CLI wire
// shape only; it is not an end-to-end persistent-proxy launcher test.
func TestInstalledCodexFunctionOutput(t *testing.T) {
	runFunctionProbe(t, false)
}
func TestInstalledCodexPersistentFunction(t *testing.T) { runFunctionProbe(t, true) }

func runFunctionProbe(t *testing.T, persistent bool) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("opt in to synthetic CLI probe")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var found atomic.Bool
	var proxyRequests atomic.Int32
	var lastProxyStatus atomic.Int32
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
	endpoint := up.URL
	var db *affinity.Store
	var startedID string
	var binding *clisession.Binding
	if persistent {
		db, err = affinity.Open(filepath.Join(t.TempDir(), "private"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		router, err := routing.NewPersistent(probeAccess{}, 90, db)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		data, err := usage.Parse([]byte(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`))
		if err != nil {
			t.Fatal(err)
		}
		router.Update([]usage.Snapshot{{Slot: "a", State: "ok", LastAttempt: now, LastSuccess: &now, Usage: &data}})
		binding, err = clisession.New(router, checkpoint.Origin{Project: "synthetic", Worktree: "synthetic", Branch: "dev"}, "synthetic-probe-run-token", true, "")
		if err != nil {
			t.Fatal(err)
		}
		defer binding.Close()
		h, err := proxy.NewPersistent(up.URL, binding.Resolve, db)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			proxyRequests.Add(1)
			w = &probeStatusWriter{ResponseWriter: w, last: &lastProxyStatus}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
			if err != nil {
				w.WriteHeader(413)
				return
			}
			r.Body.Close()
			var shape map[string]json.RawMessage
			json.Unmarshal(body, &shape)
			keys := make([]string, 0, len(shape))
			for k := range shape {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			t.Logf("synthetic CLI field names: %v", keys)
			var items []map[string]json.RawMessage
			json.Unmarshal(shape["input"], &items)
			for _, item := range items {
				keys := []string{}
				for k := range item {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				var kind, role string
				json.Unmarshal(item["type"], &kind)
				json.Unmarshal(item["role"], &role)
				t.Logf("synthetic item shape: type=%s role=%s keys=%v null_id=%t", kind, role, keys, bytes.Equal(item["id"], []byte("null")))
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			h.ServeHTTP(w, r)
		}))
		defer p.Close()
		endpoint = p.URL
	}
	profile, err := Profile(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if persistent {
		profile += "X-Switcher-Run = \"synthetic-probe-run-token\"\n"
	}
	if err = os.WriteFile(filepath.Join(root, "switcher-probe.config.toml"), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "exec", "--strict-config", "--ephemeral", "--skip-git-repo-check", "--profile", "switcher-probe", "--json", "Synthetic protocol probe only.")
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir(), "CODEX_HOME=" + root, "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var registrationErr error
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if persistent && event.Type == "thread.started" && startedID == "" {
			registrationErr = binding.Started(event.ThreadID)
			if registrationErr == nil {
				startedID = event.ThreadID
			}
		}
	}
	runErr := cmd.Wait()
	// Constant labels and scalar diagnostics only: never dump raw CLI output,
	// request bodies, thread IDs, account IDs or credentials on failure.
	if runErr != nil || scanner.Err() != nil || registrationErr != nil || calls.Load() != 2 || !found.Load() {
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		t.Logf("probe diagnostics: persistent=%t started=%t registration_failed=%t scanner_failed=%t timed_out=%t exit_code=%d proxy_requests=%d last_proxy_status=%d upstream_calls=%d function_output_found=%t", persistent, startedID != "", registrationErr != nil, scanner.Err() != nil, ctx.Err() != nil, exitCode, proxyRequests.Load(), lastProxyStatus.Load(), calls.Load(), found.Load())
	}
	if runErr != nil {
		t.Fatal("synthetic tool probe failed; see probe diagnostics above")
	}
	if scanner.Err() != nil || registrationErr != nil {
		t.Fatal("CLI event registration failed")
	}
	if calls.Load() != 2 || !found.Load() {
		t.Fatalf("unexpected function-output shape: calls=%d recognized=%t", calls.Load(), found.Load())
	}
}

type probeAccess struct{}

func (probeAccess) Access(slot string, now time.Time) (accounts.Access, error) {
	return accounts.Access{Token: "synthetic", AccountID: "synthetic-" + slot, ExpiresAt: now.Add(time.Hour)}, nil
}

type probeStatusWriter struct {
	http.ResponseWriter
	last  *atomic.Int32
	wrote bool
}

func (w *probeStatusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.last.Store(int32(code))
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *probeStatusWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *probeStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
