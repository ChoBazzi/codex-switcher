package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

var errShape = errors.New("unsupported_persistent_request")

func validID(id string) bool { return id != "" && len(id) <= 4096 && !strings.ContainsRune(id, 0) }
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

// Deliberately narrow initial contract: previous_response_id plus text messages.
// Conversation handles, item references, tool results and unknown top-level
// fields are rejected rather than guessed or forwarded without ownership checks.
func continuationRefs(data []byte) ([]string, error) {
	if !uniqueJSON(data) {
		return nil, errShape
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(data, &body) != nil || body == nil {
		return nil, errShape
	}
	allowed := map[string]bool{"model": true, "input": true, "previous_response_id": true, "stream": true, "store": true, "instructions": true, "max_output_tokens": true, "temperature": true, "top_p": true, "reasoning": true, "text": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true, "metadata": true, "service_tier": true}
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
		for k := range item {
			if k != "type" && k != "role" && k != "content" {
				return nil, errShape
			}
		}
		var typ, role string
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
