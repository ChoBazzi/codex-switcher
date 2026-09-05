package cliprobe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
)

// Explicit opt-in: launches the installed CLI against synthetic loopback only.
func TestInstalledCodex(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("set SWITCHER_CODEX_INTEGRATION=1 to run the installed CLI against synthetic servers")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("codex executable required")
	}
	for _, scenario := range []string{"success", "rate-limit", "server-error", "partial"} {
		t.Run(scenario, func(t *testing.T) {
			upstream := &Upstream{Scenario: scenario}
			up := httptest.NewServer(upstream)
			defer up.Close()
			var mu sync.Mutex
			var names []string
			var sessionID, threadID string
			incoming := 0
			requests := 0
			invalidTransport := false
			h, err := proxy.New(up.URL, func(r *http.Request) (proxy.Identity, error) {
				mu.Lock()
				defer mu.Unlock()
				incoming++
				sessionID, threadID = r.Header.Get("Session-Id"), r.Header.Get("Thread-Id")
				if incoming == 1 {
					for key := range r.Header {
						names = append(names, key)
					}
				}
				id, err := cliidentity.ThreadID(r.Header)
				return proxy.Identity{Session: id, Token: "synthetic-token"}, err
			})
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests++
				invalidTransport = invalidTransport || r.Method != "POST" || r.URL.Path != "/responses" || r.Header.Get("Upgrade") != ""
				mu.Unlock()
				h.ServeHTTP(w, r)
			}))
			defer p.Close()
			root := t.TempDir()
			profile, err := Profile(p.URL)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "switcher-probe.config.toml"), []byte(profile), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "exec", "--strict-config", "--ephemeral", "--skip-git-repo-check", "--profile", "switcher-probe", "--json", "Synthetic connectivity probe. Do not use tools.")
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir(), "CODEX_HOME=" + root,
				"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
			output, runErr := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("CLI probe timed out (raw CLI output omitted)")
			}
			mu.Lock()
			count := incoming
			total, invalid := requests, invalidTransport
			sort.Strings(names)
			t.Logf("request header names (values omitted): %v", names)
			mu.Unlock()
			if total != 1 || invalid || count != 1 || upstream.Calls.Load() != 1 {
				t.Fatalf("expected one HTTP POST: total=%d resolved=%d upstream=%d invalid_transport=%t", total, count, upstream.Calls.Load(), invalid)
			}
			if scenario == "success" {
				if runErr != nil || !strings.Contains(string(output), Reply) {
					t.Fatal("CLI success not recognized (raw CLI output omitted)")
				}
				var started string
				for _, line := range strings.Split(string(output), "\n") {
					var event struct {
						Type   string `json:"type"`
						Thread string `json:"thread_id"`
					}
					if json.Unmarshal([]byte(line), &event) == nil && event.Type == "thread.started" {
						started = event.Thread
					}
				}
				if started == "" {
					t.Fatal("missing thread.started")
				}
				if sessionID != started || threadID != started {
					t.Fatal("request identity differs from thread.started")
				}
			} else if runErr == nil {
				t.Fatal("CLI accepted failed response")
			}
		})
	}
}
