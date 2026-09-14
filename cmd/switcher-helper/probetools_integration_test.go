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
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Real installed CLI -> actual managed probe -> synthetic upstream, no accounts,
// network models, real history, or user-owned workspace files involved.
func TestInstalledProbeTools(t *testing.T) {
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
		var p map[string]json.RawMessage
		_ = json.Unmarshal(body, &p)
		var output []any
		if n == 1 {
			name, toolType := "", "function"
			var names []string
			var find func(json.RawMessage)
			find = func(raw json.RawMessage) {
				var ds []map[string]json.RawMessage
				_ = json.Unmarshal(raw, &ds)
				for _, d := range ds {
					names = append(names, probeString(d, "name"))
					if v := probeString(d, "name"); v == "exec_command" || v == "shell_command" || v == "exec" {
						name = v
						toolType = probeString(d, "type")
					}
					find(d["tools"])
				}
			}
			find(p["tools"])
			var input []map[string]json.RawMessage
			_ = json.Unmarshal(p["input"], &input)
			for _, item := range input {
				if probeString(item, "type") == "additional_tools" {
					find(item["tools"])
				}
			}
			if name == "" {
				observations <- "shell_tool_missing: " + strings.Join(names, ",")
				http.Error(w, "synthetic tool missing", 400)
				return
			}
			args := `{"cmd":"printf switcher-tool-ok","yield_time_ms":1000}`
			if name == "shell_command" {
				args = `{"command":"printf switcher-tool-ok","timeout_ms":1000}`
			}
			call := map[string]any{"type": "function_call", "id": "fc_synthetic_old", "call_id": "call_synthetic_old", "name": name, "arguments": args, "status": "completed"}
			if name == "exec" && toolType == "custom" {
				call["type"], call["namespace"] = "custom_tool_call", "functions"
				// Exercise the installed tool runtime without creating a nested
				// OS sandbox (sandbox-exec is unavailable inside this test host).
				call["input"] = `text("switcher-tool-ok");`
				delete(call, "arguments")
			}
			output = []any{
				map[string]any{"type": "reasoning", "id": "rs_synthetic_old", "summary": []any{}, "encrypted_content": "synthetic-opaque"},
				call,
			}
		} else {
			var input []map[string]json.RawMessage
			_ = json.Unmarshal(p["input"], &input)
			pair, result := "", ""
			for _, item := range input {
				if probeString(item, "type") == "function_call" || probeString(item, "type") == "custom_tool_call" {
					pair = probeString(item, "call_id")
				}
				if probeString(item, "type") == "function_call_output" || probeString(item, "type") == "custom_tool_call_output" {
					result = probeString(item, "call_id")
					if !strings.Contains(string(item["output"]), "switcher-tool-ok") {
						observations <- "tool_execution_failed"
					}
				}
			}
			if pair == "" || pair != result || !strings.Contains(string(body), "switcher-tool-ok") {
				observations <- "tool_pair_lost"
			}
			if n == 2 && (!strings.Contains(string(body), "synthetic-opaque") || pair != "call_synthetic_old" || r.Header.Get("Authorization") != "Bearer synthetic-a") {
				observations <- "current_turn_changed"
			}
			if n == 3 && (strings.Contains(string(body), "synthetic-opaque") || strings.Contains(string(body), "call_synthetic_old") || r.Header.Get("Authorization") != "Bearer synthetic-b") {
				observations <- "cross_account_isolation_failed"
			}
			output = []any{map[string]any{"type": "message", "id": "msg_synthetic_final", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Synthetic tool complete.", "annotations": []any{}}}}}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(kind string, values map[string]any) {
			values["type"] = kind
			b, _ := json.Marshal(values)
			fmt.Fprintf(w, "data: %s\n\n", b)
			_ = http.NewResponseController(w).Flush()
		}
		emit("response.created", map[string]any{"response": map[string]any{"id": "resp_synthetic", "status": "in_progress"}})
		for i, item := range output {
			emit("response.output_item.added", map[string]any{"output_index": i, "item": item})
			emit("response.output_item.done", map[string]any{"output_index": i, "item": item})
		}
		emit("response.completed", map[string]any{"response": map[string]any{"id": "resp_synthetic", "status": "completed", "output": output}})
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
	// Keep permissions read-only: the synthetic tool only prints a literal.
	id := run("Run the synthetic check once.")
	var state map[string]any
	for {
		state = next("probe_state")
		if state["busy"] == false {
			break
		}
	}
	if calls.Load() != 2 || state["failed"] == true {
		t.Fatal("tool round trip failed")
	}
	command, _ := json.Marshal(map[string]any{"action": "select", "slot": "b", "revision": state["revision"]})
	_, _ = commands.Write(append(command, '\n'))
	if next("probe_selection")["accepted"] != true {
		t.Fatal("post-tool switch rejected")
	}
	if run("resume", id, "Summarize the previous tool result without running it again.") != id {
		t.Fatal("thread changed")
	}
	if calls.Load() != 3 {
		t.Fatal("tool replay or extra request")
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
