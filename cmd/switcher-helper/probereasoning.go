package main

import (
	"crypto/sha256"
	"encoding/json"
)

// Current-turn reasoning must have been observed from this conversation's
// successful upstream response. Slot/last-credential equality alone cannot
// authorize an older blob reintroduced after a new identity's first response.
// Only digests survive; restart deliberately does not restore current-turn state.
type probeReasoningOwners struct {
	credential [32]byte
	boundary   [32]byte
	keys       map[[32]byte]bool
}

func probeReasoningKey(item map[string]json.RawMessage) [32]byte {
	// CLI may change status/summary formatting; bind opaque bytes and server ID.
	if content := probeString(item, "encrypted_content"); content != "" {
		data, _ := json.Marshal([]string{"encrypted", probeString(item, "id"), content})
		return sha256.Sum256(data)
	}
	copy := make(map[string]json.RawMessage, len(item))
	for k, v := range item {
		if k != "status" {
			copy[k] = v
		}
	}
	data, _ := json.Marshal(copy)
	return sha256.Sum256(data)
}

func (o *probeReasoningOwners) permits(item map[string]json.RawMessage, credential [32]byte) bool {
	return credential != ([32]byte{}) && credential == o.credential && o.keys[probeReasoningKey(item)]
}

func (o *probeReasoningOwners) accept(keys map[[32]byte]bool, credential, boundary [32]byte) bool {
	next := map[[32]byte]bool{}
	if o.credential == credential && o.boundary == boundary {
		for key := range o.keys {
			next[key] = true
		}
	}
	for key := range keys {
		next[key] = true
	}
	if len(next) > 1024 || len(next) > 0 && credential == ([32]byte{}) {
		return false
	}
	o.keys, o.credential, o.boundary = next, credential, boundary
	return true
}
