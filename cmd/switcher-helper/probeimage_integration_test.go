package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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

// The installed CLI executes only text/image literals in its custom runtime.
// Its actual tool output then crosses the managed probe on A and, after an
// explicit new user input, on B. No real accounts, models, or histories are used.
func TestInstalledProbeToolsInlineImageHistory(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("synthetic installed CLI opt-in")
	}
	t.Setenv("TMPDIR", t.TempDir())
	pixel := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	pixel.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 30, B: 60, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixel); err != nil {
		t.Fatal("synthetic image setup failed")
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	var calls atomic.Int32
	var priorHistoryCall atomic.Value
	observations := make(chan string, 32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		r.Body.Close()
		if err != nil {
			observations <- "synthetic_request_unreadable"
			http.Error(w, "synthetic request unreadable", 400)
			return
		}
		n := calls.Add(1)
		if n > 4 {
			observations <- "unexpected_upstream_attempt"
			http.Error(w, "synthetic unexpected attempt", 400)
			return
		}
		var payload struct {
			Tools json.RawMessage
			Input []map[string]json.RawMessage
		}
		if json.Unmarshal(body, &payload) != nil {
			t.Error("synthetic upstream received invalid JSON")
			return
		}
		var output []any
		if n == 1 {
			customExec := false
			var find func(json.RawMessage)
			find = func(raw json.RawMessage) {
				var declarations []map[string]json.RawMessage
				_ = json.Unmarshal(raw, &declarations)
				for _, declaration := range declarations {
					if probeString(declaration, "name") == "exec" && probeString(declaration, "type") == "custom" {
						customExec = true
					}
					find(declaration["tools"])
				}
			}
			find(payload.Tools)
			for _, item := range payload.Input {
				if probeString(item, "type") == "additional_tools" {
					find(item["tools"])
				}
			}
			if !customExec {
				observations <- "synthetic_image_runtime_missing"
				http.Error(w, "synthetic custom runtime missing", 400)
				return
			}
			program := fmt.Sprintf(`text("switcher-image-ok"); image(%q);`, dataURL)
			output = []any{
				map[string]any{"type": "reasoning", "id": "rs_synthetic_image", "summary": []any{}, "encrypted_content": "synthetic-image-reasoning"},
				map[string]any{"type": "custom_tool_call", "id": "ctc_synthetic_image", "call_id": "call_synthetic_image", "namespace": "functions", "name": "exec", "input": program, "status": "completed"},
			}
		} else {
			callID, resultID := "", ""
			textSeen, imageSeen := false, false
			imageCount, imageDetail, imageMatches := 0, "", false
			for _, item := range payload.Input {
				switch probeString(item, "type") {
				case "custom_tool_call":
					callID = probeString(item, "call_id")
					if _, exists := item["id"]; exists {
						observations <- "tool_item_id_not_removed"
					}
				case "custom_tool_call_output":
					resultID = probeString(item, "call_id")
					var parts []map[string]json.RawMessage
					if json.Unmarshal(item["output"], &parts) != nil {
						observations <- "image_tool_result_not_array"
						continue
					}
					for _, part := range parts {
						switch probeString(part, "type") {
						case "input_text":
							textSeen = textSeen || strings.Contains(probeString(part, "text"), "switcher-image-ok")
						case "input_image":
							imageCount++
							imageDetail = probeString(part, "detail")
							imageMatches = probeString(part, "image_url") == dataURL
							// CLI 0.155.1 omits high/default detail on this path.
							// Explicit high preservation is covered by unit/API tests.
							imageSeen = imageMatches && (imageDetail == "" || imageDetail == "high")
						}
					}
				}
			}
			if callID == "" || callID != resultID || !textSeen || !imageSeen {
				observations <- fmt.Sprintf("image_text_or_tool_pair_not_preserved: pair=%t text=%t image_count=%d detail=%q same_bytes=%t", callID != "" && callID == resultID, textSeen, imageCount, imageDetail, imageMatches)
			}
			if n == 2 && (r.Header.Get("Authorization") != "Bearer synthetic-a" || callID != "call_synthetic_image" || !strings.Contains(string(body), "synthetic-image-reasoning")) {
				observations <- "image_current_turn_owner_changed"
			}
			if n == 3 {
				priorHistoryCall.Store(callID)
				if r.Header.Get("Authorization") != "Bearer synthetic-a" || callID == "call_synthetic_image" || strings.Contains(string(body), "synthetic-image-reasoning") {
					observations <- "image_completed_history_not_normalized"
				}
			}
			if n == 4 && (r.Header.Get("Authorization") != "Bearer synthetic-b" || callID == "call_synthetic_image" || callID == priorHistoryCall.Load() || strings.Contains(string(body), "synthetic-image-reasoning") || strings.Contains(string(body), "rs_synthetic_image")) {
				observations <- "image_history_account_isolation_failed"
			}
			output = []any{map[string]any{"type": "message", "id": "msg_synthetic_image_final", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Synthetic image tool complete.", "annotations": []any{}}}}}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(kind string, values map[string]any) {
			values["type"] = kind
			data, _ := json.Marshal(values)
			fmt.Fprintf(w, "data: %s\n\n", data)
			_ = http.NewResponseController(w).Flush()
		}
		emit("response.created", map[string]any{"response": map[string]any{"id": "resp_synthetic_image", "status": "in_progress"}})
		for i, item := range output {
			emit("response.output_item.added", map[string]any{"output_index": i, "item": item})
			emit("response.output_item.done", map[string]any{"output_index": i, "item": item})
		}
		emit("response.completed", map[string]any{"response": map[string]any{"id": "resp_synthetic_image", "status": "completed", "output": output}})
	}))
	defer upstream.Close()
	input, commands := io.Pipe()
	output, sink := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- switchProbeWithAccess([]string{"--allow-live", "--managed", "--tools"}, input, sink, syntheticProbeAccess{}, upstream.URL)
	}()
	events := make(chan map[string]any, 128)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				events <- event
			}
		}
	}()
	t.Cleanup(func() {
		commands.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error("synthetic probe shutdown failed")
			}
		case <-time.After(3 * time.Second):
			t.Error("synthetic probe shutdown timeout")
		}
		input.Close()
		sink.Close()
		output.Close()
	})
	next := func(kind string) map[string]any {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event := <-events:
				if event["event"] == "probe_blocked" {
					t.Fatalf("synthetic probe rejected image history: %v", event["detail"])
				}
				if event["event"] == kind {
					return event
				}
			case <-timer.C:
				t.Fatal("synthetic probe event timeout")
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
		cmd := exec.CommandContext(ctx, "codex", append([]string{"exec", "--skip-git-repo-check", "--json"}, tail...)...)
		cmd.Dir = home
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + home, "TMPDIR=" + os.TempDir(), "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
		result, err := cmd.CombinedOutput()
		if err != nil {
			select {
			case observation := <-observations:
				t.Fatal(observation)
			default:
			}
			for len(events) > 0 {
				if event := <-events; event["event"] == "probe_blocked" {
					t.Fatalf("synthetic image parser rejection: %v; upstream attempts=%d", event["detail"], calls.Load())
				}
			}
			t.Fatal("synthetic image CLI failed; raw output omitted")
		}
		for _, line := range strings.Split(string(result), "\n") {
			var event struct {
				Type   string `json:"type"`
				Thread string `json:"thread_id"`
			}
			if json.Unmarshal([]byte(line), &event) == nil && event.Type == "thread.started" {
				return event.Thread
			}
		}
		t.Fatal("synthetic thread ID missing")
		return ""
	}
	thread := run("Run the synthetic inline-image check once.")
	var state map[string]any
	for {
		state = next("probe_state")
		if state["busy"] == false {
			break
		}
	}
	if calls.Load() != 2 || state["failed"] == true {
		t.Fatal("image tool round trip failed")
	}
	if run("resume", thread, "Describe the earlier image result without running the tool again.") != thread {
		t.Fatal("image history resumed a different thread")
	}
	for {
		state = next("probe_state")
		if state["busy"] == false {
			break
		}
	}
	if calls.Load() != 3 || state["failed"] == true {
		t.Fatal("image history did not continue on A")
	}
	command, _ := json.Marshal(map[string]any{"action": "select", "slot": "b", "revision": state["revision"]})
	_, _ = commands.Write(append(command, '\n'))
	if next("probe_selection")["accepted"] != true || calls.Load() != 3 {
		t.Fatal("account selection dispatched or was rejected")
	}
	if run("resume", thread, "Summarize the earlier image result without running the tool again.") != thread {
		t.Fatal("image history resumed a different thread")
	}
	if calls.Load() != 4 {
		t.Fatal("image tool replay or extra upstream attempt")
	}
	select {
	case observation := <-observations:
		t.Fatal(observation)
	default:
	}
	t.Log("actual CLI image round trip and same-thread A/B resume preserved PNG/text; exactly four upstream attempts")
}
