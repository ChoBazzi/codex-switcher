package main

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
	"sync/atomic"
	"testing"
	"time"
)

// Exercises isolated auxiliary conversations and preserves root binding.
// Real installed CLI -> actual probe -> synthetic upstream. Never real accounts.
func TestInstalledProbeAuxiliaryIdentity(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("installed CLI opt-in")
	}
	for _, mode := range []string{"review", "spawn", "spawn_fork", "guardian", "resume", "fork", "new"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			blocked := make(chan map[string]any, 16)
			ready := make(chan string, 1)
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				n := calls.Add(1)
				var output []any
				if n == 1 && (mode == "spawn" || mode == "spawn_fork" || mode == "guardian") {
					call := map[string]any{"type": "function_call", "id": "fc_synthetic", "call_id": "call_synthetic", "status": "completed"}
					if mode == "guardian" {
						call["name"] = "exec_command"
						call["arguments"] = `{"cmd":"printf synthetic-approval-check","sandbox_permissions":"require_escalated","justification":"Synthetic check: print a fixed string."}`
					} else {
						call["name"], call["namespace"] = "spawn_agent", "multi_agent_v1"
						args, _ := json.Marshal(map[string]any{"message": "Reply OK without tools.", "fork_context": mode == "spawn_fork"})
						call["arguments"] = string(args)
					}
					output = []any{call}
				} else {
					// Keep the parent alive long enough for a child to make its first request.
					if mode == "spawn" || mode == "spawn_fork" || mode == "guardian" {
						select {
						case event := <-blocked:
							blocked <- event
						case <-time.After(300 * time.Millisecond):
						}
					}
					output = []any{map[string]any{"type": "message", "id": "msg_synthetic", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Synthetic complete.", "annotations": []any{}}}}}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(kind string, values map[string]any) {
					values["type"] = kind
					data, _ := json.Marshal(values)
					fmt.Fprintf(w, "data: %s\n\n", data)
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
			done := make(chan error, 1)
			go func() {
				done <- switchProbeWithAccess([]string{"--allow-live", "--managed", "--tools"}, input, sink, leasedProbeAccess{dir: t.TempDir()}, up.URL)
			}()
			defer func() {
				commands.Close()
				select {
				case err := <-done:
					if err != nil {
						t.Error("synthetic probe shutdown failed")
					}
				case <-time.After(300 * time.Millisecond):
					t.Error("probe shutdown timeout")
				}
				input.Close()
				sink.Close()
				output.Close()
			}()
			go func() {
				scanner := bufio.NewScanner(output)
				for scanner.Scan() {
					var event map[string]any
					if json.Unmarshal(scanner.Bytes(), &event) != nil {
						continue
					}
					if event["event"] == "probe_ready" {
						ready <- event["codex_home"].(string)
					}
					if event["event"] == "probe_blocked" {
						select {
						case blocked <- event:
						default:
						}
					}
				}
			}()
			var home string
			select {
			case home = <-ready:
			case <-time.After(300 * time.Millisecond):
				t.Fatal("probe ready timeout")
			}
			repo := t.TempDir()
			init := exec.Command("git", "init", "-q", repo)
			if err := init.Run(); err != nil {
				t.Fatal("synthetic repository init failed")
			}
			if err := os.WriteFile(repo+"/example.txt", []byte("synthetic change\n"), 0600); err != nil {
				t.Fatal("fixture write failed")
			}
			args := []string{"exec", "--skip-git-repo-check", "--json", "-m", "synthetic-model"}
			if mode == "guardian" {
				args = append(args, "--approve-for-me")
			}
			args = append(args, "Spawn one agent to reply OK without tools, or perform the synthetic approval check.")
			if mode == "review" {
				args = []string{"review", "--uncommitted", "-c", `model="synthetic-model"`}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "codex", args...)
			cmd.Dir = repo
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + home, "TMPDIR=" + os.TempDir(), "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
			// Keep generated conversation IDs in memory only; never print CLI output.
			var captured bytes.Buffer
			cmd.Stdout = io.Discard
			if mode == "resume" || mode == "fork" || mode == "new" {
				cmd.Stdout = &captured
			}
			cmd.Stderr = io.Discard
			_ = cmd.Run()
			if mode == "resume" || mode == "fork" || mode == "new" {
				var thread string
				scanner := bufio.NewScanner(&captured)
				for scanner.Scan() {
					var event map[string]any
					if json.Unmarshal(scanner.Bytes(), &event) == nil && event["type"] == "thread.started" {
						thread, _ = event["thread_id"].(string)
					}
				}
				if thread == "" || calls.Load() != 1 {
					t.Fatal("initial synthetic thread failed")
				}
				args = []string{"exec", mode, "--json", "--skip-git-repo-check", thread, "Reply OK without tools."}
				if mode == "new" {
					args = []string{"exec", "--json", "--skip-git-repo-check", "-m", "synthetic-model", "Reply OK without tools."}
				}
				second := exec.CommandContext(ctx, "codex", args...)
				second.Dir = repo
				second.Env = cmd.Env
				second.Stdout = io.Discard
				second.Stderr = io.Discard
				err := second.Run()
				if mode == "resume" {
					if err != nil || calls.Load() != 2 {
						t.Fatal("same-thread resume should remain compatible")
					}
					select {
					case <-blocked:
						t.Fatal("resume was blocked")
					default:
					}
					t.Log("confirmed: same-thread resume accepted by actual probe")
					return
				}
			}
			if ctx.Err() != nil {
				t.Fatal("synthetic CLI timeout")
			}
			if mode == "review" || mode == "spawn" || mode == "spawn_fork" || mode == "guardian" {
				select {
				case event := <-blocked:
					t.Fatalf("unexpected block: %v / %v", event["code"], event["detail"])
				default:
				}
				minimum := int32(3)
				if mode == "review" {
					minimum = 1
				}
				if calls.Load() < minimum {
					t.Fatalf("auxiliary request did not reach synthetic upstream: %d", calls.Load())
				}
				t.Log("auxiliary upstream accepted")
				return
			}
			select {
			case event := <-blocked:
				expectedCode, expectedEqual := "probe_identity_invalid", false
				if mode == "fork" || mode == "new" {
					expectedCode, expectedEqual = "probe_conversation_changed", true
				}
				if event["code"] != expectedCode || event["thread_header_count"] != float64(1) || event["session_header_count"] != float64(1) || event["identity_headers_equal"] != expectedEqual {
					t.Fatal("unexpected conversation admission result")
				}
				if mode == "review" && calls.Load() != 0 {
					t.Fatal("review reached upstream")
				}
				if (mode == "fork" || mode == "new") && calls.Load() != 1 {
					t.Fatal("different conversation reached upstream")
				}
				t.Log("confirmed actual probe rejection:", expectedCode)
			case <-time.After(time.Second):
				t.Fatal("expected auxiliary identity rejection was not observed")
			}
		})
	}
}
