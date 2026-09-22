package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Actual root response -> connection registry -> new child request. The text
// is only an envelope: accepting a response that lost the opaque task must fail.
func TestProbeAgentTaskDispatch(t *testing.T) {
	for _, change := range []string{"unchanged", "restart", "token", "account", "registration", "dispatch", "unknown"} {
		t.Run(change, func(t *testing.T) {
			var calls atomic.Int32
			call := syntheticAgentCall("synthetic-parent-opaque")
			encoded, _ := json.Marshal(call)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				output := string(encoded)
				if n != 1 {
					if n == 3 && !strings.Contains(string(body), `"encrypted_content":"synthetic-parent-opaque"`) {
						t.Error("child task was deleted")
					}
					output = `{"type":"message","role":"assistant","phase":"final_answer","content":[]}`
				}
				io.WriteString(w, `data: {"type":"response.completed","response":{"status":"completed","output":[`+output+`]}}`+"\n\n")
			}))
			defer up.Close()
			source := newHistoryAccess()
			dir := t.TempDir()
			p := startHistoryProbe(t, source, up.URL, dir)
			send := func(thread, body string) int {
				r, _ := http.NewRequest("POST", p.address+"/responses", strings.NewReader(body))
				r.Header.Set("Thread-Id", thread)
				r.Header.Set("Session-Id", "12345678-1234-4234-8234-123456789012")
				r.Header.Set("X-Switcher-Run", p.secret)
				response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
				if err != nil {
					t.Fatal("synthetic task request failed")
				}
				defer response.Body.Close()
				io.Copy(io.Discard, response.Body)
				return response.StatusCode
			}
			root := "12345678-1234-4234-8234-123456789012"
			if send(root, historyFirst) != 200 {
				t.Fatal("parent did not emit task")
			}
			followup := `{"input":[{"role":"user","content":"synthetic request"},` + string(encoded) + `,{"type":"function_call_output","call_id":"synthetic-call","output":"started"}]}`
			if p.send(t, "/responses", followup) != 200 {
				t.Fatal("parent tool followup lost ownership")
			}
			switch change {
			case "restart":
				p.stop()
				p = startHistoryProbe(t, source, up.URL, dir)
			case "dispatch":
				source.change("account", true)
			case "token", "account", "registration":
				source.change(change, false)
			}
			payload := "synthetic-parent-opaque"
			if change == "unknown" {
				payload = "synthetic-unknown-opaque"
			}
			childBody := `{"input":[{"role":"user","content":"repository instructions"},{"type":"agent_message","author":"parent","recipient":"child","content":[{"type":"input_text","text":"TASK Payload:"},{"type":"encrypted_content","encrypted_content":"` + payload + `"}]}]}`
			child := "11111111-1111-4111-8111-111111111111"
			status := send(child, childBody)
			if change == "unchanged" || change == "restart" {
				if status != 200 || calls.Load() != 3 {
					t.Fatal("owned child task failed")
				}
			} else {
				if status == 200 || calls.Load() != 2 {
					t.Fatal("unknown/changed owner reached upstream")
				}
				if send(child, childBody) != 409 || calls.Load() != 2 {
					t.Fatal("failed task was replayed")
				}
			}
		})
	}
}
