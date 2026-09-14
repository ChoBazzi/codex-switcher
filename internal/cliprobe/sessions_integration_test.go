package cliprobe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
)

func TestInstalledCodexSessionIsolation(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("opt-in installed CLI test")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("codex executable required")
	}
	fixture := &Upstream{Scenario: "success"}
	up := httptest.NewServer(fixture)
	defer up.Close()
	var mu sync.Mutex
	counts := map[string]int{}
	h, err := proxy.New(up.URL, func(r *http.Request) (proxy.Identity, error) {
		id, err := cliidentity.ThreadID(r.Header)
		if err != nil {
			return proxy.Identity{}, err
		}
		mu.Lock()
		counts[id]++
		mu.Unlock()
		return proxy.Identity{Session: id, Token: "synthetic"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	p := httptest.NewServer(h)
	defer p.Close()
	root := t.TempDir()
	profile, err := Profile(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "switcher-probe.config.toml"), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	if fixture.Calls.Load() != 0 {
		t.Fatal("setup triggered a model request")
	}
	dirs := []string{t.TempDir(), t.TempDir()}
	type result struct {
		index  int
		output []byte
		err    error
	}
	results := make(chan result, 2)
	for i, dir := range dirs {
		go func(i int, dir string) {
			out, err := invokeCLI(binary, root, dir, "Synthetic connectivity probe. Do not use tools.")
			results <- result{i, out, err}
		}(i, dir)
	}
	ids := make([]string, 2)
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal("concurrent CLI run failed (raw output omitted)")
		}
		ids[r.index] = startedID(r.output)
	}
	if ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatal("new threads not distinct")
	}
	out, err := invokeCLI(binary, root, dirs[0], "resume", ids[0], "Synthetic second turn. Do not use tools.")
	if err != nil || startedID(out) != ids[0] {
		t.Fatal("resume did not preserve conversation identity")
	}
	out, err = invokeCLI(binary, root, dirs[0], "Synthetic new conversation. Do not use tools.")
	third := startedID(out)
	if err != nil || third == "" || third == ids[0] || third == ids[1] {
		t.Fatal("new conversation reused identity")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(counts) != 3 || counts[ids[0]] != 2 || counts[ids[1]] != 1 || counts[third] != 1 || fixture.Calls.Load() != 4 {
		t.Fatal("session request counts differ from expected 2/1/1")
	}
}

func invokeCLI(binary, root, dir string, tail ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := append([]string{"exec", "--strict-config", "--skip-git-repo-check", "--profile", "switcher-probe", "--json"}, tail...)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir(), "CODEX_HOME=" + root,
		"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
	cmd.WaitDelay = time.Second
	return cmd.CombinedOutput()
}

func startedID(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		var event struct {
			Type   string `json:"type"`
			Thread string `json:"thread_id"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "thread.started" {
			return event.Thread
		}
	}
	return ""
}
