//go:build darwin && cgo

package directcli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/cliprobe"
	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

func persistentBridge(t *testing.T) (*Bridge, *cliprobe.Upstream, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	origin, err := projectidentity.Resolve(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := affinity.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r, _ := routing.NewPersistent(access{}, 90, db)
	d, _ := usage.Parse([]byte(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`))
	now := time.Now()
	r.Update([]usage.Snapshot{{Slot: "a", State: "ok", LastAttempt: now, LastSuccess: &now, Usage: &d}})
	b, _ := New(r, origin, modelSecret, hookSecret)
	fixture := &cliprobe.Upstream{Scenario: "success"}
	up := httptest.NewServer(fixture)
	t.Cleanup(up.Close)
	h, err := proxy.NewPersistent(up.URL, b.Resolve, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	mux := http.NewServeMux()
	mux.HandleFunc("/control/cli-hook", b.Hook)
	mux.Handle("/responses", h)
	p := httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return b, fixture, p, dir
}

func TestDirectHTTPHooksThenModelAndResume(t *testing.T) {
	_, fixture, p, dir := persistentBridge(t)
	path := filepath.Join(t.TempDir(), "connection.json")
	data, _ := json.Marshal(Connection{p.URL, hookSecret})
	os.WriteFile(path, data, 0600)
	for _, e := range []Event{{"SessionStart", sid, dir, "startup"}, {"UserPromptSubmit", sid, dir, ""}} {
		if err := SendHook(path, e); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.Calls.Load() != 0 {
		t.Fatal("hook caused a model request")
	}
	r := request()
	r.URL.Scheme = "http"
	r.URL.Host = strings.TrimPrefix(p.URL, "http://")
	r.RequestURI = ""
	r.Body = io.NopCloser(strings.NewReader(`{"input":"synthetic"}`))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || fixture.Calls.Load() != 1 {
		t.Fatal("direct model request failed", resp.StatusCode)
	}
	for _, e := range []Event{{"SessionEnd", sid, dir, ""}, {"SessionStart", sid, dir, "resume"}, {"UserPromptSubmit", sid, dir, ""}} {
		if err := SendHook(path, e); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.Calls.Load() != 1 {
		t.Fatal("resume hook invoked model")
	}
}

func TestInstalledCLIDirectProfileUnregisteredFailsClosed(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("installed CLI opt in")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	_, fixture, p, dir := persistentBridge(t)
	home := t.TempDir()
	cleanup, err := Install(home, "/usr/bin/false", p.URL, modelSecret, hookSecret)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "exec", "--strict-config", "--profile", "switcher", "--skip-git-repo-check", "--sandbox", "read-only", "--json", "--ephemeral", "-")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + home, "TMPDIR=" + os.TempDir(), "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=localhost,127.0.0.1", "RUST_LOG=off"}
	cmd.Stdin = strings.NewReader("Synthetic direct profile check")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("CLI timeout")
	}
	if err == nil || fixture.Calls.Load() != 0 {
		t.Fatal("untrusted hook reached model")
	}
	if !strings.Contains(string(output), "thread.started") {
		t.Fatal("CLI did not load generated profile (raw output omitted)")
	}
}
