package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

// Negative control: a bare 429 with no structured usage-limit code must still
// fail closed. Successful completed-turn quota switching remains unchanged.
func TestProbeQuotaBoundaryReproduction(t *testing.T) {
	for _, connection := range []string{"1", "2"} {
		for _, toolsPending := range []bool{false, true} {
			t.Run(fmt.Sprintf("connection_%s_tools_%v", connection, toolsPending), func(t *testing.T) {
				dir := t.TempDir()
				if os.Chmod(dir, 0700) != nil {
					t.Fatal("private directory")
				}
				fetcher := &recoveryQuotaFetcher{}
				var calls atomic.Int32
				observed := make(chan string, 4)
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := calls.Add(1)
					io.Copy(io.Discard, r.Body)
					// Synthetic account labels only; never record real request headers.
					account := "unexpected"
					switch r.Header.Get("Authorization") {
					case "Bearer synthetic-a":
						account = "a"
					case "Bearer synthetic-b":
						account = "b"
					}
					observed <- account
					if n == 2 && toolsPending {
						http.Error(w, "synthetic quota exhaustion", 429)
						return
					}
					output := []any{map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{}}}
					if n == 1 && toolsPending {
						output = []any{map[string]any{"type": "function_call", "name": "synthetic_tool", "call_id": "synthetic-call", "arguments": "{}"}}
					}
					data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "synthetic", "status": "completed", "output": output}})
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: "+string(data)+"\n\n")
				}))
				defer up.Close()
				h := startMulti(t, dir, up.URL, syntheticProbeAccess{}, fetcher, nil)
				home := h.next(t, "probe_ready", nil)["codex_home"].(string)
				refresh := func(id int) {
					h.send(t, map[string]any{"action": "usage_refresh", "request_id": id})
					h.next(t, "usage_refresh", func(e map[string]any) bool { return e["status"] == "finished" })
				}
				refresh(1)
				if connection == "2" {
					h.send(t, map[string]any{"action": "connection_create"})
					home = h.next(t, "probe_ready", func(e map[string]any) bool { return e["connection_id"] == "2" })["codex_home"].(string)
					h.next(t, "connection_result", nil)
				}
				url, key, _ := checkpointProfile(t, home)
				if code := checkpointRequest(t, url, key, `{"input":[{"role":"user","content":"first task"}]}`); code != 200 {
					t.Fatalf("first request %d", code)
				}
				if <-observed != "a" {
					t.Fatal("first request did not choose A")
				}
				state := h.next(t, "probe_state", func(e map[string]any) bool {
					return e["connection_id"] == connection && (e["can_abandon_turn"] == true || e["busy"] == false)
				})
				if (state["can_abandon_turn"] == true) != toolsPending {
					t.Fatal("unexpected tool boundary")
				}
				fetcher.low.Store(true) // A:80 -> 4; B remains 60.
				refresh(2)
				h.send(t, map[string]any{"action": "usage"})
				snapshot := h.next(t, "usage_snapshot", nil)["accounts"].([]any)
				for _, item := range snapshot {
					row := item.(map[string]any)
					if row["slot"] == "a" && row["usage"].(map[string]any)["remaining_percent"] != float64(4) {
						t.Fatal("A exhaustion was not published")
					}
					if row["slot"] == "b" && row["usage"].(map[string]any)["remaining_percent"] != float64(60) {
						t.Fatal("B eligibility was not published")
					}
				}
				body := `{"input":[{"role":"user","content":"next task"}]}`
				if toolsPending {
					body = `{"input":[{"role":"user","content":"first task"},{"type":"function_call","name":"synthetic_tool","call_id":"synthetic-call","arguments":"{}"},{"type":"function_call_output","call_id":"synthetic-call","output":"done"}]}`
				}
				wantCode, wantAccount := 200, "b"
				if toolsPending {
					wantCode, wantAccount = 429, "a"
				}
				if code := checkpointRequest(t, url, key, body); code != wantCode {
					t.Fatalf("follow-up status %d, want %d", code, wantCode)
				}
				if <-observed != wantAccount {
					t.Fatal("unexpected follow-up account")
				}
				state = h.next(t, "probe_state", func(e map[string]any) bool { return e["connection_id"] == connection && e["busy"] == false })
				if toolsPending {
					if state["slot"] != "a" || state["failed"] != true {
						t.Fatal("quota failure did not lock A")
					}
					if checkpointRequest(t, url, key, body) != 409 {
						t.Fatal("failed request not blocked")
					}
					if checkpointRequest(t, url, key, `{"input":[{"role":"user","content":"new instruction"}]}`) != 409 {
						t.Fatal("failure gate skipped before quota selection")
					}
					t.Log("NEGATIVE CONTROL: unclassified 429 never authorizes automatic replay")
				} else {
					if state["slot"] != "b" || state["failed"] != false {
						t.Fatal("completed-turn automatic switch failed")
					}
					t.Log("CONTROL: fresh A=4%, B=60%; completed turn followed by new input selects B")
				}
				if calls.Load() != 2 {
					t.Fatal("unexpected replay")
				}
				h.shutdown(t)
			})
		}
	}
}
