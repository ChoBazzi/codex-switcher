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

func TestProbeAbandonGuards(t *testing.T) {
	for _, tc := range []struct {
		expected                             uint64
		busy, waiting, pending, failed, want bool
	}{
		{2, false, false, true, false, true}, {1, false, false, true, false, false},
		{2, true, false, true, false, false}, {2, false, true, true, false, false},
		{2, false, false, false, false, false}, {2, false, false, true, true, false},
	} {
		if probeAbandonAllowed(tc.expected, 2, tc.busy, tc.waiting, tc.pending, tc.failed) != tc.want {
			t.Fatal("unsafe abandonment admission")
		}
	}
}

func TestProbeAbandonToolTurn(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var attempts atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		n := attempts.Add(1)
		output := `[{"type":"function_call","name":"sleep","arguments":"{}","call_id":"synthetic-call"}]`
		if n > 1 {
			if r.Header.Get("Authorization") != "Bearer synthetic-b" {
				t.Error("wrong recovery account")
			}
			output = `[{"type":"message","role":"assistant","phase":"final_answer","content":[]}]`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"type":"response.completed","response":{"id":"synthetic-response","status":"completed","output":`+output+"}}\n\n")
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
	request := func(body string) int {
		t.Helper()
		r, _ := http.NewRequest("POST", endpoint+"/responses", strings.NewReader(body))
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
	original := `{"input":[{"role":"user","content":"wait"}]}`
	if request(original) != 200 {
		t.Fatal("initial tool response failed")
	}
	var state map[string]any
	for {
		state = next("probe_state")
		if state["can_abandon_turn"] == true {
			break
		}
	}
	if state["busy"] != true {
		t.Fatal("tool turn not locked")
	}
	if command("select", "b", state["revision"], "probe_selection")["accepted"] != false {
		t.Fatal("tool turn switched without confirmation")
	}
	if command("abandon_turn", "a", 0, "probe_abandonment")["accepted"] != false {
		t.Fatal("stale confirmation accepted")
	}
	ack := command("abandon_turn", "a", state["revision"], "probe_abandonment")
	if ack["accepted"] != true || ack["failed"] != true || ack["busy"] != false {
		t.Fatal("turn not abandoned")
	}
	if command("abandon_turn", "a", ack["revision"], "probe_abandonment")["accepted"] != false {
		t.Fatal("duplicate abandonment accepted")
	}
	if request(original) != 409 {
		t.Fatal("old turn ran before account selection")
	}
	if command("select", "b", ack["revision"], "probe_selection")["accepted"] != true {
		t.Fatal("recovery selection rejected")
	}
	if request(original) != 409 || attempts.Load() != 1 {
		t.Fatal("old input replayed")
	}
	if request(`{"input":[{"role":"user","content":"wait"},{"role":"user","content":"cancelled; only answer ok"}]}`) != 200 || attempts.Load() != 2 {
		t.Fatal("explicit new input failed")
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
