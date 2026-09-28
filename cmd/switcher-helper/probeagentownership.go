package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"sort"
)

// Shared only by the root and children of one connection. Callers hold the
// connection mutex. No raw message, routing identifier or credential is stored.
// Checkpoints retain only these digests, never infer an owner on restart.
type probeAgentOwners struct {
	keys map[probeAgentOwnerKey]bool
}

type probeAgentOwnerKey struct {
	message, credential [32]byte
}

const probeAgentOwnerLimit = 1024

type probeCheckpointAgentOwner struct {
	Message, Credential [32]byte
}

func (o *probeAgentOwners) snapshot() []probeCheckpointAgentOwner {
	var result []probeCheckpointAgentOwner
	for key := range o.keys {
		result = append(result, probeCheckpointAgentOwner{key.message, key.credential})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Message != result[j].Message {
			return bytes.Compare(result[i].Message[:], result[j].Message[:]) < 0
		}
		return bytes.Compare(result[i].Credential[:], result[j].Credential[:]) < 0
	})
	return result
}

func (o *probeAgentOwners) permits(content string, credential [32]byte) bool {
	return content != "" && credential != ([32]byte{}) && o.keys[probeAgentOwnerKey{sha256.Sum256([]byte(content)), credential}]
}

func (o *probeAgentOwners) accept(messages map[[32]byte]bool, credential [32]byte) bool {
	if len(messages) == 0 {
		return true
	}
	if credential == ([32]byte{}) {
		return false
	}
	count := len(o.keys)
	for message := range messages {
		if !o.keys[probeAgentOwnerKey{message, credential}] {
			count++
		}
	}
	if count > probeAgentOwnerLimit {
		return false
	}
	if o.keys == nil {
		o.keys = make(map[probeAgentOwnerKey]bool)
	}
	for message := range messages {
		o.keys[probeAgentOwnerKey{message, credential}] = true
	}
	return true
}

// The CLI copies the opaque message argument into agent_message.content.
// Observe only known collaboration calls from a successful upstream response;
// never learn ownership from a request's claimed author or recipient.
func probeAgentOutputMessage(item map[string]json.RawMessage) string {
	if probeString(item, "type") != "function_call" || !probeCompleted(item) {
		return ""
	}
	switch probeString(item, "namespace") {
	case "collaboration", "multi_agent_v1":
	default:
		return ""
	}
	switch probeString(item, "name") {
	case "spawn_agent", "send_message", "followup_task":
	default:
		return ""
	}
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(probeString(item, "arguments")), &args) != nil {
		return ""
	}
	return probeString(args, "message")
}
