package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Full local tool pairs are portable. Server item ids, opaque old reasoning,
// and server-side compaction are not. No tool is executed by this adapter.
func probeToolBody(body []byte, slot, previousSlot, salt string) ([]byte, error) {
	return probeToolBodyWithCompaction(body, slot, previousSlot, salt, nil)
}

// Called only on a normalized, validated body. These items retain opaque state
// or original call IDs and require another ownership check at actual dispatch,
// since RequestAccess may refresh credentials after local body validation.
func probeItemsNeedTurnOwner(items []map[string]json.RawMessage) bool {
	needsOwner := false
	for _, item := range items {
		kind := probeString(item, "type")
		if (kind == "message" || kind == "") && probeString(item, "role") == "user" {
			needsOwner = false
		}
		switch kind {
		case "reasoning", "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output":
			needsOwner = true
		}
	}
	return needsOwner
}

// Request-local decoded input; never cached across requests or checkpointed.
// normalize mutates item IDs, so boundary/compact metadata is read beforehand.
type probeToolInput struct {
	fields         map[string]json.RawMessage
	items          []map[string]json.RawMessage
	parseErr       error
	needsTurnOwner bool
}

func parseProbeToolInput(body []byte) *probeToolInput {
	d := &probeToolInput{}
	if json.Unmarshal(body, &d.fields) != nil || d.fields == nil {
		d.fields = nil
		d.parseErr = &probeBodyError{Reason: "invalid_json_object", Item: -1, Part: -1}
		return d
	}
	if json.Unmarshal(d.fields["input"], &d.items) != nil || d.items == nil {
		d.items = nil
		d.parseErr = &probeBodyError{Reason: "input_not_message_array", Item: -1, Part: -1}
	}
	return d
}

func (d *probeToolInput) compactItems() []map[string]json.RawMessage {
	var items []map[string]json.RawMessage
	for _, item := range d.items {
		if probeString(item, "type") == "compaction" {
			items = append(items, item)
		}
	}
	return items
}

func probeToolBodyWithCompaction(body []byte, slot, previousSlot, salt string, allow func(map[string]json.RawMessage) bool) ([]byte, error) {
	return probeToolBodyWithOwnership(body, slot, previousSlot, salt, allow, nil)
}

func probeToolBodyWithOwnership(body []byte, slot, previousSlot, salt string, allow, reasoning func(map[string]json.RawMessage) bool) ([]byte, error) {
	return parseProbeToolInput(body).normalize(slot, previousSlot, salt, allow, reasoning)
}

func (d *probeToolInput) normalize(slot, previousSlot, salt string, allow, reasoning func(map[string]json.RawMessage) bool) ([]byte, error) {
	bad := func(reason string, i int) ([]byte, error) {
		return nil, &probeBodyError{Reason: reason, Item: i, Part: -1}
	}
	p := d.fields
	if p == nil {
		return nil, d.parseErr
	}
	for _, key := range []string{"previous_response_id", "conversation"} {
		if raw, ok := p[key]; ok && string(bytes.TrimSpace(raw)) != "null" && string(raw) != `""` {
			return bad("server_reference_"+key, -1)
		}
		delete(p, key)
	}
	if d.parseErr != nil {
		return nil, d.parseErr
	}
	items := d.items

	lastUser := -1
	for i, item := range items {
		if probeString(item, "role") == "user" && (probeString(item, "type") == "message" || probeString(item, "type") == "") {
			lastUser = i
		}
	}
	if lastUser < 0 {
		return bad("user_boundary_missing", -1)
	}
	result := make([]map[string]json.RawMessage, 0, len(items))
	pending := map[string]string{}
	seen := map[string]bool{}
	for i, item := range items {
		kind := probeString(item, "type")
		if i == lastUser && len(pending) != 0 {
			return bad("tool_pair_crosses_user_boundary", i)
		}
		switch kind {
		case "compaction":
			if allow == nil || !allow(item) {
				return bad("compaction_owner_unavailable", i)
			}
			result = append(result, item)
			continue // Keep the opaque canonical item intact, including its ID.
		case "", "message":
			// Reuse the strict text/phase contract without changing tool config.
			if err := probeTextMessage(item, i); err != nil {
				return bad("message_shape_unsupported", i)
			}
			if !probeMessageText(item["content"]) {
				return bad("message_content_unsupported", i)
			}
		case "agent_message":
			// CLI-delivered inter-agent text is not a user boundary or a
			// server continuation. Preserve attribution, never use it to route.
			if !probeAgentMessage(item) {
				return bad("agent_message_shape_unsupported", i)
			}
		case "additional_tools":
			if !probeKeys(item, "type role id tools") || !probeToolDefinitions(item["tools"]) {
				return bad("tool_declaration_unsupported", i)
			}
		case "function_call", "custom_tool_call":
			field := "arguments"
			if kind == "custom_tool_call" {
				field = "input"
			}
			if !probeKeys(item, "type id status call_id name namespace "+field) || !probeHasString(item, field) || probeString(item, "name") == "" || !probeCompleted(item) {
				return bad("tool_call_invalid", i)
			}
			call := probeString(item, "call_id")
			if call == "" || seen[call] {
				return bad("tool_call_duplicate_or_missing", i)
			}
			seen[call], pending[call] = true, kind
			if i < lastUser {
				item["call_id"] = probePortableCall(salt, slot, call)
			}
		case "function_call_output", "custom_tool_call_output":
			if !probeKeys(item, "type id status call_id output") || !probeCompleted(item) || !probeToolOutput(item["output"]) {
				return bad("tool_output_invalid", i)
			}
			call := probeString(item, "call_id")
			if pending[call]+"_output" != kind {
				return bad("tool_output_orphan_or_duplicate", i)
			}
			delete(pending, call)
			if i < lastUser {
				item["call_id"] = probePortableCall(salt, slot, call)
			}
		case "reasoning":
			if !probeKeys(item, "type id status summary content encrypted_content") || !probeCompleted(item) {
				return bad("reasoning_shape_unsupported", i)
			}
			var summary []map[string]json.RawMessage
			if json.Unmarshal(item["summary"], &summary) != nil || summary == nil {
				return bad("reasoning_shape_unsupported", i)
			}
			if raw, exists := item["encrypted_content"]; exists && string(raw) != "null" && !probeHasString(item, "encrypted_content") {
				return bad("reasoning_shape_unsupported", i)
			}
			if i < lastUser {
				continue
			} // Completed earlier turn; never export opaque state.
			if previousSlot == "" || previousSlot != slot {
				return bad("reasoning_owner_unavailable", i)
			}
			if reasoning != nil && !reasoning(item) {
				return bad("reasoning_owner_unavailable", i)
			}
			// Current-turn reasoning is returned untouched to its originating account.
			result = append(result, item)
			continue
		default:
			return bad("history_type_unsupported", i)
		}
		if i > lastUser && (kind == "function_call" || kind == "custom_tool_call" || strings.HasSuffix(kind, "_output")) && previousSlot != slot {
			return bad("tool_turn_owner_unavailable", i)
		}
		delete(item, "id")
		result = append(result, item)
	}
	if len(pending) != 0 {
		return bad("tool_output_missing", -1)
	}
	if raw, ok := p["tools"]; ok && !probeToolDefinitions(raw) {
		return bad("tool_declaration_unsupported", -1)
	}
	if raw, ok := p["additional_tools"]; ok && !probeToolDefinitions(raw) {
		return bad("tool_declaration_unsupported", -1)
	}
	d.needsTurnOwner = probeItemsNeedTurnOwner(result)
	p["input"], _ = json.Marshal(result)
	return json.Marshal(p)
}

func probeString(item map[string]json.RawMessage, key string) string {
	var s string
	_ = json.Unmarshal(item[key], &s)
	return s
}
func probeHasString(item map[string]json.RawMessage, key string) bool {
	var s string
	raw, ok := item[key]
	return ok && len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil
}
func probeKeys(item map[string]json.RawMessage, keys string) bool {
	allowed := " " + keys + " "
	for key := range item {
		if !strings.Contains(allowed, " "+key+" ") {
			return false
		}
	}
	return item != nil
}
func probeCompleted(item map[string]json.RawMessage) bool {
	raw, ok := item["status"]
	return !ok || string(raw) == "null" || probeString(item, "status") == "completed"
}
func probePortableCall(salt, slot, call string) json.RawMessage {
	digest := sha256.Sum256([]byte(salt + "\x00" + slot + "\x00" + call))
	b, _ := json.Marshal("call_" + hex.EncodeToString(digest[:16]))
	return b
}
func probeToolOutput(raw json.RawMessage) bool {
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		return json.Unmarshal(raw, &s) == nil
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || parts == nil {
		return false
	}
	for _, p := range parts {
		if probeString(p, "type") == "input_image" {
			if !probeInlineToolImage(p) {
				return false
			}
			continue
		}
		if !probeKeys(p, "type text") || (probeString(p, "type") != "input_text" && probeString(p, "type") != "output_text") || !probeHasString(p, "text") {
			return false
		}
	}
	return true
}

func probeMessageText(raw json.RawMessage) bool {
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		return json.Unmarshal(raw, &s) == nil
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || parts == nil {
		return false
	}
	for _, part := range parts {
		if !probeKeys(part, "type text annotations logprobs") || !probeHasString(part, "text") {
			return false
		}
		for _, key := range []string{"annotations", "logprobs"} {
			if v, exists := part[key]; exists {
				var list []json.RawMessage
				if json.Unmarshal(v, &list) != nil || list == nil || len(list) != 0 {
					return false
				}
			}
		}
	}
	return true
}

func probeAgentMessage(item map[string]json.RawMessage) bool {
	if !probeKeys(item, "type id author recipient content") || probeString(item, "author") == "" || probeString(item, "recipient") == "" {
		return false
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(item["content"], &parts) != nil || parts == nil {
		return false
	}
	for _, part := range parts {
		if !probeKeys(part, "type text") || probeString(part, "type") != "input_text" || !probeHasString(part, "text") {
			return false
		}
	}
	return true
}

func probeToolDefinitions(raw json.RawMessage) bool {
	var definitions []map[string]json.RawMessage
	if json.Unmarshal(raw, &definitions) != nil || definitions == nil {
		return false
	}
	for _, d := range definitions {
		switch probeString(d, "type") {
		case "function", "custom":
			if probeString(d, "name") == "" {
				return false
			}
		case "namespace":
			if probeString(d, "name") == "" || !probeToolDefinitions(d["tools"]) {
				return false
			}
		default:
			return false // No server-hosted tools/remote state in this mode.
		}
	}
	return true
}
