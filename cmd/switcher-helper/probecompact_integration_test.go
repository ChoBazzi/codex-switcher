package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

// Real installed CLI -> actual managed probe -> synthetic upstream, no accounts,
// network models, real history, or user-owned workspace files involved.
func TestInstalledProbeCompaction(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("installed CLI opt-in")
	}
	t.Setenv("TMPDIR", t.TempDir())
	var calls atomic.Int32
	observations := make(chan string, 8)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		r.Body.Close()
		n := calls.Add(1)
		if n > 5 {
			http.Error(w, "synthetic unexpected extra call", 400)
			return
		}
		if r.URL.Path != "/" {
			observations <- "unexpected_route"
			http.Error(w, "synthetic route", 400)
			return
		}
		text := "synthetic initial answer"
		if n == 2 {
			text = "Portable checkpoint: synthetic-blue-apple. Do not run tools."
		}
		if n >= 3 {
			if !strings.Contains(string(body), "Portable checkpoint: synthetic-blue-apple") {
				observations <- "compaction_summary_missing"
			}
			text = "synthetic-blue-apple"
		}
		if n == 4 && r.Header.Get("Authorization") != "Bearer synthetic-b" {
			observations <- "wrong_compaction_recovery_account"
		}
		item := map[string]any{"type": "message", "id": "msg_synthetic", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(kind string, values map[string]any) {
			values["type"] = kind
			b, _ := json.Marshal(values)
			fmt.Fprintf(w, "data: %s\n\n", b)
			http.NewResponseController(w).Flush()
		}
		emit("response.created", map[string]any{"response": map[string]any{"id": "resp_synthetic", "status": "in_progress"}})
		emit("response.output_item.added", map[string]any{"output_index": 0, "item": item})
		emit("response.output_item.done", map[string]any{"output_index": 0, "item": item})
		tokens := 10
		if n == 1 {
			tokens = 5000
		}
		emit("response.completed", map[string]any{"response": map[string]any{"id": "resp_synthetic", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": tokens, "output_tokens": 10, "total_tokens": tokens + 10}}})
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
		timer := time.NewTimer(25 * time.Second)
		defer timer.Stop()
		for {
			select {
			case e := <-events:
				if e["event"] == "probe_blocked" {
					t.Fatalf("probe blocked: %v", e["detail"])
				}
				if e["event"] == kind {
					return e
				}
			case <-timer.C:
				t.Fatal("event timeout")
				return nil
			}
		}
	}
	home := next("probe_ready")["codex_home"].(string)
	next("probe_state")
	run := func(tail ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		args := append([]string{"exec", "--skip-git-repo-check", "--json"}, tail...)
		cmd := exec.CommandContext(ctx, "codex", args...)
		cmd.Dir = home
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + home, "TMPDIR=" + os.TempDir(), "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			select {
			case e := <-observations:
				t.Fatal(e)
			default:
			}
			// Only structural diagnostics from our parser, never raw CLI history.
			for len(events) > 0 {
				e := <-events
				if e["event"] == "probe_blocked" {
					t.Fatalf("parser: %v", e["detail"])
				}
			}
			t.Fatal("synthetic tool CLI failed; raw output omitted")
		}
		for _, line := range strings.Split(string(out), "\n") {
			var e struct {
				Type   string `json:"type"`
				Thread string `json:"thread_id"`
			}
			if json.Unmarshal([]byte(line), &e) == nil && e.Type == "thread.started" {
				return e.Thread
			}
		}
		t.Fatal("thread missing")
		return ""
	}
	id := run("Remember synthetic-blue-apple; do not use tools.")
	if calls.Load() != 1 {
		t.Fatal("initial request count")
	}
	if run("resume", "-c", "model_auto_compact_token_limit=1024", id, "Continue without tools.") != id {
		t.Fatal("thread changed")
	}
	if calls.Load() != 3 {
		t.Fatalf("expected one compaction and followup, got %d total calls", calls.Load())
	}
	compacted := false
	err := filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(data), "\n") {
			var item struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(line), &item) == nil && item.Type == "compacted" {
				compacted = true
			}
		}
		return nil
	})
	if err != nil || !compacted {
		t.Fatal("installed CLI did not persist a compaction boundary")
	}
	commands.Write([]byte("{\"action\":\"status\"}\n"))
	var state map[string]any
	for {
		state = next("probe_state")
		if state["busy"] == false && state["revision"].(float64) >= 6 {
			break
		}
	}
	command, _ := json.Marshal(map[string]any{"action": "select", "slot": "b", "revision": state["revision"]})
	commands.Write(append(command, '\n'))
	if next("probe_selection")["accepted"] != true {
		t.Fatal("post-compaction switch rejected")
	}
	if run("resume", id, "What was the remembered marker? Do not use tools.") != id {
		t.Fatal("thread changed")
	}
	if calls.Load() != 4 {
		t.Fatal("unexpected cross-account request count")
	}

	select {
	case e := <-observations:
		t.Fatal(e)
	default:
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
