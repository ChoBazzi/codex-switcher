package main

import (
	"crypto/sha256"
	"encoding/json"
	"strconv"
)

func probeAbandonAllowed(expected, revision uint64, busy, waiting, turnPending, failed bool) bool {
	return expected == revision && !busy && !waiting && turnPending && !failed
}

// Only a changed final user message/boundary can start after explicit recovery.
// No prompt text is retained, logged, or synthesized.
func probeUserBoundary(body []byte) ([32]byte, bool) {
	var p struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &p) != nil {
		return [32]byte{}, false
	}
	for i := len(p.Input) - 1; i >= 0; i-- {
		item := p.Input[i]
		if probeString(item, "role") != "user" || (probeString(item, "type") != "" && probeString(item, "type") != "message") {
			continue
		}
		var content any
		if json.Unmarshal(item["content"], &content) != nil || content == nil {
			return [32]byte{}, false
		}
		canonical, _ := json.Marshal(content)
		return sha256.Sum256(append([]byte(strconv.Itoa(i)+":"), canonical...)), true
	}
	return [32]byte{}, false
}
