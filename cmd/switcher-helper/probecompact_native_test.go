package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeNativeCompaction(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var attempts atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		attempts.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-a" {
			t.Error("opaque state sent to another account")
		}
		if r.URL.Path == "/compact" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"object":"response.compaction","output":[{"role":"user","content":"first"},{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-opaque"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"type":"response.completed","response":{"id":"synthetic-response","status":"completed","output":[{"type":"message","role":"assistant","phase":"final_answer","content":[]}]}}`+"\n\n")
	}))
	defer up.Close()
	input, commands := io.Pipe()
	output, sink := io.Pipe()
	defer input.Close()
	defer commands.Close()
	defer output.Close()
	defer sink.Close()
	done := make(chan error, 1)
	go func() {
		done <- switchProbeWithAccess([]string{"--allow-live", "--managed", "--tools"}, input, sink, syntheticProbeAccess{}, up.URL)
	}()
	events := make(chan map[string]any, 64)
	go func() {
		s := bufio.NewScanner(output)
		for s.Scan() {
			var e map[string]any
			if json.Unmarshal(s.Bytes(), &e) == nil {
				events <- e
			}
		}
	}()
	next := func(kind string) map[string]any {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case e := <-events:
				if e["event"] == kind {
					return e
				}
			case <-timer.C:
				t.Fatal("event timeout: " + kind)
				return nil
			}
		}
	}
	home := next("probe_ready")["codex_home"].(string)
	next("probe_state")
	profile, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	extract := func(key string) string {
		m := regexp.MustCompile(`(?m)^` + key + ` = "([^"]+)"`).FindSubmatch(profile)
		if len(m) != 2 {
			t.Fatal("profile missing")
		}
		return string(m[1])
	}
	endpoint, secret := extract("base_url"), extract("X-Switcher-Run")
	request := func(path, body string) int {
		t.Helper()
		r, _ := http.NewRequest("POST", endpoint+path, strings.NewReader(body))
		r.Header.Set("X-Switcher-Run", secret)
		r.Header.Set("Thread-Id", "12345678-1234-4234-8234-123456789012")
		r.Header.Set("Session-Id", "12345678-1234-4234-8234-123456789012")
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	command := func(action, slot string, revision any, event string) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"action": action, "slot": slot, "revision": revision})
		commands.Write(append(b, '\n'))
		return next(event)
	}
	idle := func() map[string]any {
		t.Helper()
		for {
			e := next("probe_state")
			if e["busy"] == false {
				return e
			}
		}
	}
	if request("/responses/compact", `{"input":[{"role":"user","content":"first"}]}`) != 200 {
		t.Fatal("native compaction failed")
	}
	state := idle()
	if command("select", "b", state["revision"], "probe_selection")["accepted"] != false || attempts.Load() != 1 {
		t.Fatal("native owner pin bypassed")
	}
	body := `{"input":[{"role":"user","content":"first"},{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-opaque"},{"role":"user","content":"next"}]}`
	if request("/responses", body) != 200 || attempts.Load() != 2 {
		t.Fatal("owner continuation failed")
	}
	idle()
	// A fresh text window removes the opaque dependency; no auto-summary is invented.
	if request("/responses", `{"input":[{"role":"user","content":"new portable context"}]}`) != 200 {
		t.Fatal("portable context rejected")
	}
	state = idle()
	if command("select", "b", state["revision"], "probe_selection")["accepted"] != true {
		t.Fatal("portable window remained pinned")
	}
	if request("/responses", body) != 409 || attempts.Load() != 3 {
		t.Fatal("old A compact blob exported to B")
	}

	commands.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timeout")
	}
}
