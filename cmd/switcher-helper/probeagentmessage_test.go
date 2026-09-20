package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const syntheticAgentMessage = `{"type":"agent_message","id":"msg_synthetic_agent","author":"synthetic-child","recipient":"synthetic-parent","content":[{"type":"input_text","text":"Synthetic child result."}]}`

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
