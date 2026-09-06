package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var errShape = errors.New("unsupported_persistent_request")

func validID(id string) bool { return id != "" && len(id) <= 4000 && !strings.ContainsAny(id, "\x00:") }
func completedID(data []byte) string {
	var response struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if json.Unmarshal(data, &response) != nil || response.Status != "completed" || !validID(response.ID) {
		return ""
	}
	return response.ID
}

// Narrow contract: text, previous response, owned item references and function
// results. Conversation handles and unknown extensions still fail closed.
func continuationRefs(data []byte) ([]string, error) {
	if !uniqueJSON(data) {
		return nil, errShape
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(data, &body) != nil || body == nil {
		return nil, errShape
	}
	allowed := map[string]bool{"model": true, "input": true, "previous_response_id": true, "stream": true, "store": true, "instructions": true, "max_output_tokens": true, "temperature": true, "top_p": true, "reasoning": true, "text": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true, "metadata": true, "service_tier": true}
	for _, key := range []string{"include", "prompt_cache_key", "client_metadata"} {
		allowed[key] = true
	}
	for k := range body {
		if !allowed[k] {
			return nil, errShape
		}
	}
	var refs []string
	if raw, ok := body["previous_response_id"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var id string
		if json.Unmarshal(raw, &id) != nil || !validID(id) {
			return nil, errShape
		}
		refs = append(refs, id)
	}
	var text string
	if json.Unmarshal(body["input"], &text) == nil && !bytes.Equal(bytes.TrimSpace(body["input"]), []byte("null")) {
		return refs, nil
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(body["input"], &items) != nil || items == nil {
		return nil, errShape
	}
	for _, item := range items {
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		if kind == "additional_tools" {
			if !additionalTools(item) {
				return nil, errShape
			}
			continue
		}
		role, _ := fieldString(item, "role")
		if kind == "reasoning" || (kind == "message" && role == "assistant") {
			key, err := historyKey(item)
			if err != nil {
				return nil, err
			}
			refs = append(refs, key)
			if raw, exists := item["id"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				id, ok := fieldString(item, "id")
				if !ok || !validID(id) {
					return nil, errShape
				}
				refs = append(refs, "item:"+id)
			}
			continue
		}
		if kind == "item_reference" || kind == "function_call_output" || kind == "function_call" {
			owned, err := toolRefs(item, kind)
			if err != nil {
				return nil, err
			}
			refs = append(refs, owned...)
			continue
		}
		for k := range item {
			if k == "id" {
				if bytes.Equal(bytes.TrimSpace(item[k]), []byte("null")) {
					continue
				}
				id, ok := fieldString(item, k)
				if !ok || !validID(id) {
					return nil, errShape
				}
				continue
			}
			if k != "type" && k != "role" && k != "content" {
				return nil, errShape
			}
		}
		var typ string
		if raw, ok := item["type"]; ok && (json.Unmarshal(raw, &typ) != nil || typ != "message") {
			return nil, errShape
		}
		if json.Unmarshal(item["role"], &role) != nil || (role != "user" && role != "developer" && role != "system") {
			return nil, errShape
		}
		if json.Unmarshal(item["content"], &text) == nil && !bytes.Equal(bytes.TrimSpace(item["content"]), []byte("null")) {
			continue
		}
		var parts []map[string]json.RawMessage
		if json.Unmarshal(item["content"], &parts) != nil || parts == nil {
			return nil, errShape
		}
		for _, p := range parts {
			if len(p) != 2 || json.Unmarshal(p["type"], &typ) != nil || typ != "input_text" || json.Unmarshal(p["text"], &text) != nil || bytes.Equal(bytes.TrimSpace(p["text"]), []byte("null")) {
				return nil, errShape
			}
		}
	}
	return refs, nil
}

// Call only after continuationRefs validates full non-assistant text messages.
func inlineMessageIDs(data []byte) []string {
	var body struct {
		Input json.RawMessage `json:"input"`
	}
	json.Unmarshal(data, &body)
	var items []map[string]json.RawMessage
	json.Unmarshal(body.Input, &items)
	var ids []string
	for _, item := range items {
		kind, _ := fieldString(item, "type")
		role, _ := fieldString(item, "role")
		if role == "assistant" {
			continue
		}
		if kind != "" && kind != "message" && kind != "function_call_output" && kind != "additional_tools" {
			continue
		}
		if id, ok := fieldString(item, "id"); ok {
			ids = append(ids, "item:"+id)
		}
	}
	return ids
}

func fieldString(item map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := item[key]
	var value string
	returnValue := ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && json.Unmarshal(raw, &value) == nil
	return value, returnValue
}
func toolRefs(item map[string]json.RawMessage, kind string) ([]string, error) {
	allowed := map[string]bool{"type": true}
	switch kind {
	case "item_reference":
		allowed["id"] = true
	case "function_call_output":
		allowed["call_id"] = true
		allowed["output"] = true
		allowed["id"] = true
	case "function_call":
		for _, k := range []string{"id", "call_id", "name", "arguments", "status"} {
			allowed[k] = true
		}
	}
	for k := range item {
		if !allowed[k] {
			return nil, errShape
		}
	}
	if kind == "item_reference" {
		id, ok := fieldString(item, "id")
		if !ok || !validID(id) {
			return nil, errShape
		}
		return []string{"item:" + id}, nil
	}
	call, ok := fieldString(item, "call_id")
	if !ok || !validID(call) {
		return nil, errShape
	}
	refs := []string{"call:function:" + call}
	if kind == "function_call_output" {
		if raw, exists := item["id"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			id, ok := fieldString(item, "id")
			if !ok || !validID(id) {
				return nil, errShape
			}
		}
		if _, ok := fieldString(item, "output"); !ok {
			return nil, errShape
		}
		return refs, nil
	}
	if name, ok := fieldString(item, "name"); !ok || name == "" {
		return nil, errShape
	}
	if _, ok := fieldString(item, "arguments"); !ok {
		return nil, errShape
	}
	if _, ok := item["status"]; ok {
		status, ok := fieldString(item, "status")
		if !ok || status != "completed" {
			return nil, errShape
		}
	}
	if _, ok := item["id"]; ok {
		id, ok := fieldString(item, "id")
		if !ok || !validID(id) {
			return nil, errShape
		}
		refs = append(refs, "item:"+id)
	}
	return refs, nil
}

// Only the final completed response grants ownership. Partial item events do not.
// Prefixes separate response, item and function-call identifiers in schema v1.
func completedRefs(data []byte) ([]string, error) {
	if !uniqueJSON(data) {
		return nil, errShape
	}
	id := completedID(data)
	if id == "" {
		return nil, errShape
	}
	var response struct {
		Output []json.RawMessage `json:"output"`
	}
	if json.Unmarshal(data, &response) != nil {
		return nil, errShape
	}
	refs := []string{id}
	for _, raw := range response.Output {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			return nil, errShape
		}
		kind, ok := fieldString(item, "type")
		if !ok {
			return nil, errShape
		}
		switch kind {
		case "message", "reasoning", "function_call":
			if kind == "reasoning" || kind == "message" {
				if key, err := historyKey(item); err == nil {
					refs = append(refs, key)
				}
			}
			if _, ok := item["id"]; ok {
				itemID, ok := fieldString(item, "id")
				if !ok || !validID(itemID) {
					return nil, errShape
				}
				refs = append(refs, "item:"+itemID)
			}
			if kind == "function_call" {
				if name, ok := fieldString(item, "name"); !ok || name == "" {
					return nil, errShape
				}
				if _, ok := fieldString(item, "arguments"); !ok {
					return nil, errShape
				}
				if _, exists := item["status"]; exists {
					status, ok := fieldString(item, "status")
					if !ok || status != "completed" {
						return nil, errShape
					}
				}
				call, ok := fieldString(item, "call_id")
				if !ok || !validID(call) {
					return nil, errShape
				}
				refs = append(refs, "call:function:"+call)
			}
		default: // Unknown output is delivered but does not grant tool ownership.
		}
	}
	return refs, nil
}

// Full server history is accepted only with a digest already owned by this
// session. ID/status may be omitted by CLI serialization; content may not change.
func historyKey(item map[string]json.RawMessage) (string, error) {
	kind, _ := fieldString(item, "type")
	allowed := map[string]bool{"id": true, "status": true, "type": true}
	switch kind {
	case "message":
		for _, k := range []string{"role", "content", "phase"} {
			allowed[k] = true
		}
		if role, _ := fieldString(item, "role"); role != "assistant" {
			return "", errShape
		}
		var content []json.RawMessage
		if json.Unmarshal(item["content"], &content) != nil || content == nil {
			return "", errShape
		}
	case "reasoning":
		for _, k := range []string{"summary", "content", "encrypted_content"} {
			allowed[k] = true
		}
		if _, exists := item["summary"]; !exists {
			return "", errShape
		}
	default:
		return "", errShape
	}
	if raw, exists := item["status"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		status, ok := fieldString(item, "status")
		if !ok || status != "completed" {
			return "", errShape
		}
	}
	canonical := map[string]any{}
	for k, raw := range item {
		if !allowed[k] {
			return "", errShape
		}
		if k == "id" || k == "status" {
			continue
		}
		var v any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&v) != nil {
			return "", errShape
		}
		canonical[k] = v
	}
	body, err := json.Marshal(canonical)
	if err != nil {
		return "", errShape
	}
	return fmt.Sprintf("history:%x", sha256.Sum256(body)), nil
}

func additionalTools(item map[string]json.RawMessage) bool {
	role, _ := fieldString(item, "role")
	if role != "developer" {
		return false
	}
	for k := range item {
		if k != "type" && k != "role" && k != "id" && k != "tools" {
			return false
		}
	}
	if raw, exists := item["id"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		id, ok := fieldString(item, "id")
		if !ok || !validID(id) {
			return false
		}
	}
	var definitions []map[string]json.RawMessage
	if json.Unmarshal(item["tools"], &definitions) != nil || len(definitions) == 0 {
		return false
	}
	for _, definition := range definitions {
		if len(definition) == 0 {
			return false
		}
	}
	return true
}

// Duplicate keys can be interpreted differently by upstream JSON decoders.
func uniqueJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func() bool
	value = func() bool {
		t, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return false
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return false
				}
				seen[key] = true
				if !value() {
					return false
				}
			}
		case '[':
			for d.More() {
				if !value() {
					return false
				}
			}
		default:
			return false
		}
		_, err = d.Token()
		return err == nil
	}
	return json.Valid(data) && value()
}
