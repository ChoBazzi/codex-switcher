package main

import (
	"context"
	"encoding/json"
	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
	"github.com/ChoBazzi/codex-switcher/internal/cliprobe"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstalledProbeParser(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("synthetic installed CLI opt-in")
	}
	fixture := &cliprobe.Upstream{Scenario: "success", MessagePhase: "final_answer"}
	var selected atomic.Int32
	type observation struct {
		slot    string
		history bool
		phase   bool
	}
	observations := make(chan observation, 8)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		r.Body.Close()
		if strings.Contains(string(body), `"additional_tools"`) {
			http.Error(w, "tools survived", 400)
			return
		}
		observations <- observation{r.Header.Get("Authorization"), strings.Contains(string(body), cliprobe.Reply) && strings.Contains(string(body), "synthetic blue apple"), strings.Contains(string(body), `"phase":"final_answer"`)}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		fixture.ServeHTTP(w, r)
	}))
	defer up.Close()
	h, err := proxy.New(up.URL, func(r *http.Request) (proxy.Identity, error) {
		id, err := cliidentity.ThreadID(r.Header)
		token := "synthetic-a"
		if selected.Load() == 1 {
			token = "synthetic-b"
		}
		return proxy.Identity{Session: id, Token: token}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	parserErrors := make(chan error, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		r.Body.Close()
		normalized, err := probeTextBody(body)
		if err != nil {
			parserErrors <- err
			http.Error(w, "parser rejected", 409)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(normalized)))
		r.ContentLength = int64(len(normalized))
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	root := t.TempDir()
	profile, err := cliprobe.Profile(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	// The real CLI default emits additional_tools; the synthetic model did not.
	profile = strings.ReplaceAll(profile, "model = \"switcher-synthetic\"\n", "")
	if err = os.WriteFile(filepath.Join(root, "config.toml"), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(tail ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		args := append([]string{"exec", "--skip-git-repo-check", "--json"}, tail...)
		cmd := exec.CommandContext(ctx, "codex", args...)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "CODEX_HOME=" + root, "TMPDIR=" + os.TempDir(), "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			select {
			case e := <-parserErrors:
				t.Fatalf("parser: %#v", e)
			default:
				t.Fatal("synthetic CLI failed; raw output omitted")
			}
		}
		for _, line := range strings.Split(string(out), "\n") {
			var event struct {
				Type   string `json:"type"`
				Thread string `json:"thread_id"`
			}
			if json.Unmarshal([]byte(line), &event) == nil && event.Type == "thread.started" {
				return event.Thread
			}
		}
		t.Fatal("no CLI thread")
		return ""
	}
	id := run("Remember synthetic blue apple. Do not use tools.")
	selected.Store(1)
	if next := run("resume", id, "What word did I ask you to remember? Do not use tools."); next != id {
		t.Fatal("thread changed")
	}
	if fixture.Calls.Load() != 2 {
		t.Fatal("expected exactly two upstream calls")
	}
	first, second := <-observations, <-observations
	if first.slot != "Bearer synthetic-a" || second.slot != "Bearer synthetic-b" || !second.history || !second.phase {
		t.Fatal("credential switch or history failed")
	}
	t.Log("default-model first turn and resume passed parser + proxy; synthetic A/B credentials and text history verified; not a live/TUI test")
}
