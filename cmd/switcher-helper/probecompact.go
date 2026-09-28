package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
)

// Only digests and local slot labels survive a request. Never inspect/decrypt,
// persist, log, or export an opaque compaction item to another identity.
type probeCompactOwner struct {
	slot       string
	credential [32]byte
}
type probeCompactRegistry map[[32]byte]probeCompactOwner

func probeCompactKey(item map[string]json.RawMessage) ([32]byte, bool) {
	if probeString(item, "type") != "compaction" || !probeKeys(item, "type id encrypted_content") {
		return [32]byte{}, false
	}
	content := probeString(item, "encrypted_content")
	if content == "" || len(content) > 4<<20 {
		return [32]byte{}, false
	}
	if _, ok := item["id"]; ok && !probeHasString(item, "id") {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(content)), true
}

func (owners probeCompactRegistry) permits(item map[string]json.RawMessage, slot string, credential [32]byte) bool {
	key, ok := probeCompactKey(item)
	owner, known := owners[key]
	return ok && known && credential != ([32]byte{}) && owner.slot == slot && owner.credential == credential
}

func (owners probeCompactRegistry) accept(body []byte, slot string, credential [32]byte) error {
	bad := errors.New("compaction_response_invalid")
	if credential == ([32]byte{}) {
		return bad
	}
	var response struct {
		Object string                       `json:"object"`
		Output []map[string]json.RawMessage `json:"output"`
	}
	if json.Unmarshal(body, &response) != nil || response.Object != "response.compaction" || response.Output == nil {
		return bad
	}
	keys := map[[32]byte]bool{}
	input, _ := json.Marshal(map[string]any{"input": response.Output})
	_, err := probeToolBodyWithCompaction(input, slot, slot, "compact-validation", func(item map[string]json.RawMessage) bool {
		key, ok := probeCompactKey(item)
		if !ok {
			return false
		}
		if owner, exists := owners[key]; exists && owner != (probeCompactOwner{slot, credential}) {
			return false
		}
		keys[key] = true
		return true
	})
	if err != nil || len(keys) == 0 || len(owners)+len(keys) > 1024 {
		return bad
	}
	for key := range keys {
		owners[key] = probeCompactOwner{slot, credential}
	}
	return nil
}
