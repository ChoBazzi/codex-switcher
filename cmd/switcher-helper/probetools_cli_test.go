package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/cliprobe"
)

// Characterize installed CLI serialization from synthetic rollout records.
// Only a temporary home and local synthetic server are used; real histories,
// account credentials, and the user's running proxy are never accessed.
func TestInstalledProbeToolsAgentMessageHistory(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("synthetic installed CLI opt-in")
	}
	for _, mode := range []string{"plain", "agent_text", "agent_encrypted"} {
		t.Run(mode, func(t *testing.T) {
			includeAgent := mode != "plain"
			home := t.TempDir()
			fixture := &cliprobe.Upstream{Scenario: "success", MessagePhase: "final_answer"}
			var mu sync.Mutex
			var requests, agentItems, metadataItems, encryptedAttachments int
			var rejection string
			var wireAgentKeys []string
			var normalizedAgentPreserved, crossAccountAgentPreserved bool
			preservesAgent := func(body []byte) bool {
				var p struct{ Input []map[string]json.RawMessage }
				if json.Unmarshal(body, &p) != nil {
					return false
				}
				for _, item := range p.Input {
					if probeString(item, "type") != "agent_message" {
						continue
					}
					_, hasID := item["id"]
					_, hasMetadata := item["internal_chat_message_metadata_passthrough"]
					var parts []map[string]json.RawMessage
					_ = json.Unmarshal(item["content"], &parts)
					contentOK := len(parts) == 1
					if mode == "agent_encrypted" {
						contentOK = len(parts) == 2 && probeString(parts[1], "encrypted_content") == "synthetic-agent-opaque"
					}
					return !hasID && !hasMetadata && contentOK && probeString(item, "author") == "synthetic-child" &&
						probeString(item, "recipient") == "synthetic-parent" &&
						probeString(parts[0], "type") == "input_text" && probeString(parts[0], "text") == "Synthetic metadata-only history."
				}
				return false
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
				r.Body.Close()
				if err != nil {
					http.Error(w, "synthetic unreadable request", 400)
					return
				}
				var payload struct{ Input []map[string]json.RawMessage }
				_ = json.Unmarshal(body, &payload)
				mu.Lock()
				requests++
				for _, item := range payload.Input {
					if probeString(item, "type") == "agent_message" {
						agentItems++
						var parts []map[string]json.RawMessage
						_ = json.Unmarshal(item["content"], &parts)
						for _, part := range parts {
							if probeString(part, "type") == "encrypted_content" {
								encryptedAttachments++
							}
						}
						for key := range item {
							wireAgentKeys = append(wireAgentKeys, key)
						}
						sort.Strings(wireAgentKeys)
					}
					if _, exists := item["internal_chat_message_metadata_passthrough"]; exists {
						metadataItems++
					}
				}
				parsed := parseProbeToolInput(body)
				// Fixture-only owner: actual successful-output registration is
				// covered by TestProbeAgentTaskDispatch.
				parsed.agentOwner = func(content string) bool { return content == "synthetic-agent-opaque" }
				normalized, parseErr := parsed.normalize("a", "a", "synthetic-salt", nil, nil)
				var detail *probeBodyError
				if errors.As(parseErr, &detail) {
					rejection = detail.Reason
				}
				if includeAgent && requests == 2 && parseErr == nil {
					normalizedAgentPreserved = preservesAgent(normalized)
					otherSlot, otherErr := probeToolBody(body, "b", "a", "synthetic-salt")
					crossAccountAgentPreserved = otherErr == nil && preservesAgent(otherSlot)
					if mode == "agent_encrypted" {
						var blocked *probeBodyError
						crossAccountAgentPreserved = errors.As(otherErr, &blocked) && blocked.Reason == "agent_message_owner_unavailable"
					}
				}
				mu.Unlock()
				if parseErr != nil {
					http.Error(w, "probe_tool_history_unsupported", 409)
					return
				}
				r.Body = io.NopCloser(strings.NewReader(string(normalized)))
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			profile := fmt.Sprintf(`model_provider = "switch_probe"
approval_policy = "never"
sandbox_mode = "read-only"
web_search = "disabled"
check_for_update_on_startup = false
[model_providers.switch_probe]
name = "Synthetic parser test"
base_url = %q
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
`, server.URL)
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(profile), 0600); err != nil {
				t.Fatal("synthetic profile setup failed")
			}
			run := func(tail ...string) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "codex", append([]string{"exec", "--skip-git-repo-check", "--json"}, tail...)...)
				cmd.Dir = home
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + home, "TMPDIR=" + home, "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
				out, err := cmd.CombinedOutput()
				for _, line := range strings.Split(string(out), "\n") {
					var event struct {
						Type   string `json:"type"`
						Thread string `json:"thread_id"`
					}
					if json.Unmarshal([]byte(line), &event) == nil && event.Type == "thread.started" {
						return event.Thread, err
					}
				}
				return "", err
			}
			thread, err := run("Return a synthetic answer without tools.")
			if err != nil || thread == "" {
				t.Fatal("synthetic initial CLI request failed; raw output omitted")
			}
			rollout := ""
			err = filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if !entry.IsDir() && strings.HasSuffix(path, ".jsonl") {
					if rollout != "" {
						return errors.New("multiple synthetic rollouts")
					}
					rollout = path
				}
				return nil
			})
			if err != nil || rollout == "" {
				t.Fatal("synthetic rollout unavailable")
			}
			existing, err := os.ReadFile(rollout)
			if err != nil {
				t.Fatal("synthetic rollout read failed")
			}
			var nextOrdinal uint64
			for _, line := range strings.Split(string(existing), "\n") {
				var entry struct {
					Ordinal uint64 `json:"ordinal"`
				}
				if json.Unmarshal([]byte(line), &entry) == nil && entry.Ordinal >= nextOrdinal {
					nextOrdinal = entry.Ordinal + 1
				}
			}
			metadata := map[string]any{"turn_id": "11111111-1111-4111-8111-111111111111", "create_time": 1800000000}
			item := map[string]any{
				"type": "message", "role": "developer", "id": "msg_synthetic_metadata",
				"content": []any{map[string]any{"type": "input_text", "text": "Synthetic metadata-only history."}},
				"internal_chat_message_metadata_passthrough": metadata,
			}
			if includeAgent {
				item["type"], item["id"] = "agent_message", "msg_synthetic_agent"
				item["author"], item["recipient"] = "synthetic-child", "synthetic-parent"
				delete(item, "role")
				if mode == "agent_encrypted" {
					item["content"] = append(item["content"].([]any), map[string]any{"type": "encrypted_content", "encrypted_content": "synthetic-agent-opaque"})
				}
			}
			record, _ := json.Marshal(map[string]any{"ordinal": nextOrdinal, "timestamp": "2026-09-20T00:00:00Z", "type": "response_item", "payload": item})
			file, err := os.OpenFile(rollout, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal("synthetic rollout append failed")
			}
			_, err = file.Write(append(record, '\n'))
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				t.Fatal("synthetic rollout write failed")
			}
			_, resumeErr := run("resume", thread, "Continue with one synthetic answer, without tools.")
			mu.Lock()
			defer mu.Unlock()
			t.Logf("wire requests=%d agent_message_items=%d agent_message_keys=%v passthrough_items=%d parser_reason=%q resume_failed=%t", requests, agentItems, wireAgentKeys, metadataItems, rejection, resumeErr != nil)
			if requests != 2 {
				t.Fatal("unexpected request count")
			}
			if !includeAgent && (resumeErr != nil || metadataItems != 0 || rejection != "") {
				t.Fatal("rollout metadata was not safely omitted on wire")
			}
			if includeAgent && (resumeErr != nil || agentItems != 1 || metadataItems != 0 || rejection != "" || !normalizedAgentPreserved || !crossAccountAgentPreserved) {
				t.Fatal("agent_message resume or portable text normalization failed")
			}
			if mode == "agent_encrypted" && encryptedAttachments != 1 {
				t.Fatal("installed CLI did not serialize the synthetic encrypted attachment")
			}
		})
	}
}
