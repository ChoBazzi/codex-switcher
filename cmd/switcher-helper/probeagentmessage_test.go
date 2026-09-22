package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const syntheticAgentMessage = `{"type":"agent_message","id":"msg_synthetic_agent","author":"synthetic-child","recipient":"synthetic-parent","content":[{"type":"input_text","text":"Synthetic child result."}]}`

func TestProbeToolsAgentMessageEncryptedAttachment(t *testing.T) {
	message := strings.Replace(syntheticAgentMessage, `}]}`, `},{"type":"encrypted_content","encrypted_content":"synthetic-parent-opaque"}]}`, 1)
	body := []byte(`{"input":[{"role":"user","content":"inspect"},` + message + `]}`)
	for _, allowed := range []bool{false, true} {
		parsed := parseProbeToolInput(body)
		parsed.agentOwner = func(content string) bool { return allowed && content == "synthetic-parent-opaque" }
		out, err := parsed.normalize("a", "", "synthetic-salt", nil, nil)
		if !allowed {
			var detail *probeBodyError
			if !errors.As(err, &detail) || detail.Reason != "agent_message_owner_unavailable" {
				t.Fatal("unowned task accepted or wrong rejection")
			}
			continue
		}
		if err != nil || !parsed.needsAgentOwner {
			t.Fatal("owned task rejected or dispatch pin missing")
		}
		var payload struct{ Input []map[string]json.RawMessage }
		if json.Unmarshal(out, &payload) != nil || len(payload.Input) != 2 {
			t.Fatal("agent task lost")
		}
		item := payload.Input[1]
		if probeString(item, "author") != "synthetic-child" || probeString(item, "recipient") != "synthetic-parent" || item["id"] != nil || item["role"] != nil {
			t.Fatal("attribution or item isolation changed")
		}
		var parts []map[string]json.RawMessage
		if json.Unmarshal(item["content"], &parts) != nil || len(parts) != 2 || probeString(parts[1], "encrypted_content") != "synthetic-parent-opaque" {
			t.Fatal("encrypted task removed or changed")
		}
		before, ok := probeUserBoundary(body)
		after, valid := probeUserBoundary(out)
		if !ok || !valid || before != after {
			t.Fatal("user boundary changed")
		}
	}
	// A nonempty envelope is not evidence that it contains the task. Preserve
	// owned opaque-only instructions and reject every portable/cross-account path.
	for _, content := range []string{
		`[{"type":"encrypted_content","encrypted_content":"synthetic-parent-opaque"}]`,
		`[{"type":"input_text","text":"TASK Payload:"},{"type":"encrypted_content","encrypted_content":"synthetic-parent-opaque"}]`,
	} {
		var item map[string]json.RawMessage
		_ = json.Unmarshal([]byte(syntheticAgentMessage), &item)
		item["content"] = json.RawMessage(content)
		encoded, _ := json.Marshal(item)
		input := []byte(`{"input":[{"role":"user","content":"context"},` + string(encoded) + `]}`)
		parsed := parseProbeToolInput(input)
		parsed.agentOwner = func(string) bool { return true }
		out, err := parsed.normalize("a", "", "salt", nil, nil)
		if err != nil || !strings.Contains(string(out), "synthetic-parent-opaque") {
			t.Fatal("owned opaque instruction lost")
		}
		parsed = parseProbeToolInput(input)
		parsed.portable = true
		parsed.agentOwner = func(string) bool { return true }
		if _, err := parsed.normalize("b", "", "salt", nil, nil); err == nil {
			t.Fatal("portable normalization exported task")
		}
	}
	for _, suffix := range []string{
		`,{"type":"reasoning","summary":[],"encrypted_content":"synthetic-opaque"}`,
		`,{"type":"function_call","call_id":"synthetic-call","name":"read","arguments":"{}"}`,
	} {
		parsed := parseProbeToolInput([]byte(`{"input":[{"role":"user","content":"inspect"},` + message + suffix + `]}`))
		parsed.agentOwner = func(string) bool { return true }
		if _, err := parsed.normalize("b", "", "salt", nil, nil); err == nil {
			t.Fatal("agent permission bypassed reasoning/tool checks")
		}
	}
}

func TestProbeToolsAgentMessageTextHistory(t *testing.T) {
	for _, tc := range []struct{ name, slot, previous, suffix string }{
		{"current", "a", "a", ""},
		{"previous", "b", "a", `,{"role":"user","content":"continue"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"input":[{"role":"user","content":"inspect"},` + syntheticAgentMessage + tc.suffix + `]}`
			out, err := probeToolBody([]byte(body), tc.slot, tc.previous, "synthetic-salt")
			if err != nil {
				t.Fatal("text agent message rejected")
			}
			var payload struct{ Input []map[string]json.RawMessage }
			if json.Unmarshal(out, &payload) != nil || len(payload.Input) < 2 {
				t.Fatal("normalized history missing")
			}
			item := payload.Input[1]
			if probeString(item, "type") != "agent_message" || probeString(item, "author") != "synthetic-child" || probeString(item, "recipient") != "synthetic-parent" || !strings.Contains(string(item["content"]), "Synthetic child result.") {
				t.Fatal("agent text or attribution changed")
			}
			if _, exists := item["id"]; exists {
				t.Fatal("server item id escaped")
			}
			if _, exists := item["role"]; exists {
				t.Fatal("agent message became an ordinary message")
			}
		})
	}
}

func TestProbeToolsAgentMessageRejectsUnsafeShape(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"author_missing", "author", ""},
		{"author_empty", "author", `""`},
		{"author_number", "author", `1`},
		{"recipient_missing", "recipient", ""},
		{"recipient_null", "recipient", `null`},
		{"recipient_object", "recipient", `{}`},
		{"content_null", "content", `null`},
		{"content_string", "content", `"synthetic-private"`},
		{"content_image", "content", `[{"type":"input_image","text":"synthetic-private"}]`},
		{"content_output_text", "content", `[{"type":"output_text","text":"synthetic-private"}]`},
		{"content_no_type", "content", `[{"text":"synthetic-private"}]`},
		{"content_no_text", "content", `[{"type":"input_text"}]`},
		{"content_null_text", "content", `[{"type":"input_text","text":null}]`},
		{"content_reference", "content", `[{"type":"input_text","text":"x","file_id":"synthetic-private"}]`},
		{"opaque_null", "content", `[{"type":"input_text","text":"x"},{"type":"encrypted_content","encrypted_content":null}]`},
		{"opaque_number", "content", `[{"type":"input_text","text":"x"},{"type":"encrypted_content","encrypted_content":1}]`},
		{"opaque_missing", "content", `[{"type":"input_text","text":"x"},{"type":"encrypted_content"}]`},
		{"opaque_reference", "content", `[{"type":"input_text","text":"x"},{"type":"encrypted_content","encrypted_content":"synthetic-private","file_id":"synthetic-private"}]`},
		{"opaque", "encrypted_content", `"synthetic-private"`},
		{"reference", "previous_response_id", `"synthetic-private"`},
		{"role", "role", `"user"`},
		{"metadata", "internal_chat_message_metadata_passthrough", `{"turn_id":"synthetic-private"}`},
		{"unknown_field", "synthetic-private", `"synthetic-private"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var item map[string]json.RawMessage
			_ = json.Unmarshal([]byte(syntheticAgentMessage), &item)
			if tc.value == "" {
				delete(item, tc.key)
			} else {
				item[tc.key] = json.RawMessage(tc.value)
			}
			encoded, _ := json.Marshal(item)
			body := `{"input":[{"role":"user","content":"inspect"},` + string(encoded) + `]}`
			_, err := probeToolBody([]byte(body), "a", "a", "synthetic-salt")
			var detail *probeBodyError
			if !errors.As(err, &detail) || detail.Reason != "agent_message_shape_unsupported" || detail.Item != 1 {
				t.Fatal("unsafe agent message was not rejected with a fixed reason")
			}
			diagnostic, _ := json.Marshal(detail)
			if strings.Contains(string(diagnostic), "synthetic-private") {
				t.Fatal("diagnostic leaked a private field or value")
			}
		})
	}
}

func TestProbeToolsAgentMessageDoesNotChangeUserBoundary(t *testing.T) {
	before := `{"input":[{"role":"user","content":"inspect"}]}`
	after := `{"input":[{"role":"user","content":"inspect"},` + syntheticAgentMessage + `]}`
	boundary, ok := probeUserBoundary([]byte(before))
	withAgent, agentOK := probeUserBoundary([]byte(after))
	if !ok || !agentOK || boundary != withAgent {
		t.Fatal("agent message satisfied recovery's new user input requirement")
	}
	if _, ok := probeUserBoundary([]byte(`{"input":[` + syntheticAgentMessage + `]}`)); ok {
		t.Fatal("agent message treated as a user boundary")
	}
	for _, history := range []string{
		`{"type":"reasoning","summary":[],"encrypted_content":"synthetic-opaque"},` + syntheticAgentMessage,
		`{"type":"function_call","call_id":"synthetic-call","name":"read","arguments":"{}"},` + syntheticAgentMessage + `,{"type":"function_call_output","call_id":"synthetic-call","output":"synthetic result"}`,
	} {
		body := []byte(`{"input":[{"role":"user","content":"inspect"},` + history + `]}`)
		if _, err := probeToolBody(body, "a", "a", "synthetic-salt"); err != nil {
			t.Fatal("same-account tool or reasoning history rejected")
		}
		if _, err := probeToolBody(body, "b", "a", "synthetic-salt"); err == nil {
			t.Fatal("agent message bypassed current-turn account ownership")
		}
	}
}
