package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A real proxy/CLI HTTP request remains open across the explicit limit. This
// exercises the current tool turn and its next follow-up, not just selection.
func TestProbeUsageLimitFailover(t *testing.T) {
	for _, connection := range []string{"1", "2"} {
		for _, shape := range []string{"json", "sse", "generic429", "partial", "all_limited"} {
			t.Run(connection+"_"+shape, func(t *testing.T) {
				dir := t.TempDir()
				os.Chmod(dir, 0700)
				fetcher := &recoveryQuotaFetcher{}
				var calls atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := calls.Add(1)
					body, _ := io.ReadAll(r.Body)
					account := "a"
					if r.Header.Get("Authorization") == "Bearer synthetic-b" {
						account = "b"
					}
					if n <= 2 && account != "a" {
						t.Error("first tool turn changed account prematurely")
					}
					if n >= 3 && account != "b" {
						t.Error("limit did not switch to B")
					}
					if n >= 3 && (strings.Contains(string(body), "opaque-a") || strings.Contains(string(body), "call-a") || !strings.Contains(string(body), "already done")) {
						t.Error("portable tool context wrong")
					}
					if n == 2 || n == 3 && shape == "all_limited" {
						if shape == "sse" || shape == "partial" {
							w.Header().Set("Content-Type", "text/event-stream")
							if shape == "partial" {
								io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
							} else {
								io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"output\":[]}}\n\n")
							}
							io.WriteString(w, "data: {\"type\":\"error\",\"code\":\"usage_limit_reached\"}\n\n")
						} else {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(429)
							code := "usage_limit_reached"
							if shape == "generic429" {
								code = "rate_limit_exceeded"
							}
							fmt.Fprintf(w, `{"error":{"code":%q,"message":"synthetic"}}`, code)
						}
						return
					}
					var output []any
					if n == 1 {
						output = []any{
							map[string]any{"type": "reasoning", "id": "reason-a", "summary": []any{}, "encrypted_content": "opaque-a"},
							map[string]any{"type": "function_call", "name": "read", "call_id": "call-a", "arguments": "{}"},
						}
					} else if n == 3 {
						output = []any{
							map[string]any{"type": "reasoning", "id": "reason-b", "summary": []any{}, "encrypted_content": "opaque-b"},
							map[string]any{"type": "function_call", "name": "read", "call_id": "call-b", "arguments": "{}"},
						}
					} else {
						output = []any{map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{}}}
					}
					data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "synthetic", "status": "completed", "output": output}})
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: "+string(data)+"\n\n")
				}))
				defer up.Close()
				h := startMulti(t, dir, up.URL, syntheticProbeAccess{}, fetcher, nil)
				home := h.next(t, "probe_ready", nil)["codex_home"].(string)
				h.send(t, map[string]any{"action": "usage_refresh", "request_id": 1})
				h.next(t, "usage_refresh", func(e map[string]any) bool { return e["status"] == "finished" })
				if connection == "2" {
					h.send(t, map[string]any{"action": "connection_create"})
					home = h.next(t, "probe_ready", func(e map[string]any) bool { return e["connection_id"] == "2" })["codex_home"].(string)
					h.next(t, "connection_result", nil)
				}
				url, key, _ := checkpointProfile(t, home)
				send := func(body string) (int, string) {
					request, _ := http.NewRequest("POST", url+"/responses", strings.NewReader(body))
					request.Header.Set("X-Switcher-Run", key)
					request.Header.Set("Thread-Id", "12345678-1234-4234-8234-123456789012")
					request.Header.Set("Session-Id", "12345678-1234-4234-8234-123456789012")
					response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
					if err != nil {
						return 0, ""
					}
					defer response.Body.Close()
					data, _ := io.ReadAll(response.Body)
					return response.StatusCode, string(data)
				}
				if code, _ := send(`{"input":[{"role":"user","content":"work"}]}`); code != 200 {
					t.Fatal("first response failed")
				}
				h.next(t, "probe_state", func(e map[string]any) bool { return e["can_abandon_turn"] == true })
				items := `{"role":"user","content":"work"},{"type":"reasoning","id":"reason-a","summary":[],"encrypted_content":"opaque-a"},{"type":"function_call","name":"read","call_id":"call-a","arguments":"{}"},{"type":"function_call_output","call_id":"call-a","output":"already done"}`
				code, response := send(`{"input":[` + items + `]}`)
				if shape == "generic429" || shape == "partial" || shape == "all_limited" {
					expected := int32(2)
					if shape == "all_limited" {
						expected = 3
					}
					if calls.Load() != expected {
						t.Fatal("unexpected retry count")
					}
					if shape != "partial" && code != 429 {
						t.Fatalf("terminal limit status %d", code)
					}
					h.next(t, "probe_state", func(e map[string]any) bool { return e["failed"] == true && e["busy"] == false })
					if code, _ = send(`{"input":[` + items + `]}`); code != 409 || calls.Load() != expected {
						t.Fatal("exhausted retry budget replayed")
					}
				} else {
					if code != 200 || strings.Contains(response, "usage_limit_reached") || strings.Contains(response, "response.created") || !strings.Contains(response, "call-b") || calls.Load() != 3 {
						t.Fatal("failover was not transparent")
					}
					h.next(t, "probe_state", func(e map[string]any) bool { return e["slot"] == "b" && e["can_abandon_turn"] == true })
					items += `,{"type":"reasoning","id":"reason-b","summary":[],"encrypted_content":"opaque-b"},{"type":"function_call","name":"read","call_id":"call-b","arguments":"{}"},{"type":"function_call_output","call_id":"call-b","output":"second done"}`
					if code, _ = send(`{"input":[` + items + `]}`); code != 200 {
						t.Fatal("next tool follow-up lost migrated context")
					}
					h.next(t, "probe_state", func(e map[string]any) bool { return e["slot"] == "b" && e["busy"] == false })
					if calls.Load() != 4 {
						t.Fatal("extra model calls")
					}
				}
				h.shutdown(t)
			})
		}
	}
}

func TestAuxiliaryUsageLimitFailover(t *testing.T) {
	var calls atomic.Int32
	committed := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-b" {
			t.Error("auxiliary did not switch")
		}
		// The next request contains the CLI's original history; it must be normalized again.
		if n == 3 && (!strings.Contains(string(body), "finished tool") || strings.Contains(string(body), "opaque-b") || strings.Contains(string(body), "call-original")) {
			t.Error("auxiliary portable follow-up failed")
		}
		output := []any{map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{}}}
		if n == 2 {
			output = []any{map[string]any{"type": "reasoning", "id": "reason-b", "summary": []any{}, "encrypted_content": "opaque-b"}, map[string]any{"type": "function_call", "name": "read", "call_id": "call-original", "arguments": "{}"}}
		}
		data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "synthetic", "status": "completed", "output": output}})
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+string(data)+"\n\n")
	}))
	defer up.Close()
	var mu sync.Mutex
	a := &probeAuxiliary{binding: probeAuxiliaryBinding{Thread: "synthetic-child", Root: "synthetic-root", Slot: "a"}}
	a.quotaAlternative = func(tried map[string]bool) string {
		if !tried["b"] {
			return "b"
		}
		return ""
	}
	defer func() {
		if a.handler != nil {
			a.handler.Close()
		}
	}()
	send := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
		w := httptest.NewRecorder()
		a.serve(w, request, &mu, syntheticProbeAccess{}, up.URL, "synthetic-salt", func() bool {
			if a.binding.Slot == "b" {
				committed = true
			}
			return true
		}, nil)
		return w
	}
	w := send(`{"input":[{"role":"user","content":"work"}]}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "usage_limit_reached") || a.failed || !a.pending || a.binding.Slot != "b" || !committed {
		t.Fatal("auxiliary failover state incorrect")
	}
	w = send(`{"input":[{"role":"user","content":"work"},{"type":"reasoning","id":"reason-b","summary":[],"encrypted_content":"opaque-b"},{"type":"function_call","name":"read","call_id":"call-original","arguments":"{}"},{"type":"function_call_output","call_id":"call-original","output":"finished tool"}]}`)
	if w.Code != 200 || a.failed || a.pending || calls.Load() != 3 {
		t.Fatal("auxiliary follow-up failed")
	}
}
