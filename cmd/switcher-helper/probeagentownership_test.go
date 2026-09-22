package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func syntheticAgentCall(message string) map[string]json.RawMessage {
	args, _ := json.Marshal(map[string]string{"message": message, "task_name": "synthetic-child"})
	item, _ := json.Marshal(map[string]string{"type": "function_call", "name": "spawn_agent", "namespace": "collaboration", "call_id": "synthetic-call", "arguments": string(args)})
	var result map[string]json.RawMessage
	_ = json.Unmarshal(item, &result)
	return result
}

func TestProbeAgentRestoredFailureDiagnostic(t *testing.T) {
	for _, restored := range []bool{false, true} {
		a := &probeAuxiliary{failed: true, restored: restored}
		var mu sync.Mutex
		w := httptest.NewRecorder()
		a.serve(w, httptest.NewRequest("POST", "/responses", strings.NewReader(`{}`)), &mu, syntheticProbeAccess{}, "http://127.0.0.1:1", "synthetic", nil, nil)
		expected := "probe_previous_request_failed"
		if restored {
			expected = "probe_auxiliary_restart_required"
		}
		if w.Code != 409 || strings.TrimSpace(w.Body.String()) != expected || a.handler != nil {
			t.Fatal("restored failure indistinguishable or dispatched")
		}
	}
}

func TestProbeAgentOwnershipObservation(t *testing.T) {
	credential := sha256.Sum256([]byte("synthetic-account-a"))
	other := sha256.Sum256([]byte("synthetic-account-b"))
	item := syntheticAgentCall("synthetic-opaque-task")
	encoded, _ := json.Marshal(item)
	w := &probeTurnWriter{ResponseWriter: httptest.NewRecorder(), holdTerminal: true}
	_, err := w.Write([]byte(`data: {"type":"response.completed","response":{"status":"completed","output":[` + string(encoded) + `]}}` + "\n\n"))
	if err != nil || !w.valid() || len(w.agentMessages) != 1 {
		t.Fatal("collaboration message not observed")
	}
	var owners probeAgentOwners
	if owners.permits("synthetic-opaque-task", credential) {
		t.Fatal("unobserved task authorized")
	}
	if !owners.accept(w.agentMessages, credential) || !owners.permits("synthetic-opaque-task", credential) {
		t.Fatal("successful response task not authorized")
	}
	if owners.permits("synthetic-opaque-task", other) || owners.permits("synthetic-opaque-task", [32]byte{}) || owners.permits("unknown", credential) {
		t.Fatal("ownership escaped credential boundary")
	}
	var otherConnection probeAgentOwners
	if otherConnection.permits("synthetic-opaque-task", credential) {
		t.Fatal("ownership escaped connection/restart")
	}
	if (&probeAgentOwners{}).accept(w.agentMessages, [32]byte{}) {
		t.Fatal("missing credential accepted")
	}
	for _, field := range []string{"namespace", "name", "type", "status"} {
		bad := syntheticAgentCall("synthetic-opaque-task")
		bad[field] = json.RawMessage(`"unsupported"`)
		if probeAgentOutputMessage(bad) != "" {
			t.Fatal("unrelated output registered")
		}
	}
	// Original call arguments are another carrier of the same opaque payload.
	body := []byte(`{"input":[{"role":"user","content":"work"},` + string(encoded) + `,{"type":"function_call_output","call_id":"synthetic-call","output":"started"},{"role":"user","content":"continue"}]}`)
	for _, identity := range [][32]byte{credential, other} {
		parsed := parseProbeToolInput(body)
		parsed.agentOwner = func(message string) bool { return owners.permits(message, identity) }
		out, err := parsed.normalize("a", "a", "synthetic", nil, nil)
		if identity == credential {
			if err != nil || !parsed.needsAgentOwner || !strings.Contains(string(out), "synthetic-opaque-task") {
				t.Fatal("owned tool argument changed")
			}
		} else if err == nil {
			t.Fatal("call ID rewriting exported opaque tool argument")
		}
	}
}

func TestProbeAgentOwnershipCapacity(t *testing.T) {
	credential := sha256.Sum256([]byte("synthetic-account"))
	var owners probeAgentOwners
	keys := map[[32]byte]bool{}
	for i := 0; i < probeAgentOwnerLimit; i++ {
		keys[sha256.Sum256([]byte(fmt.Sprint(i)))] = true
	}
	if !owners.accept(keys, credential) || !owners.accept(keys, credential) {
		t.Fatal("bounded or repeated observation rejected")
	}
	extra := map[[32]byte]bool{sha256.Sum256([]byte("extra")): true}
	if owners.accept(extra, credential) || len(owners.keys) != probeAgentOwnerLimit {
		t.Fatal("registry evicted ownership or grew without bound")
	}
}

func TestProbeAgentOwnershipCheckpoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if privateServiceDir(dir) != nil {
		t.Fatal("private directory unavailable")
	}
	credential := sha256.Sum256([]byte("synthetic-credential"))
	var registry probeAgentOwners
	message := "synthetic-encrypted-task"
	if !registry.accept(map[[32]byte]bool{sha256.Sum256([]byte(message)): true}, credential) {
		t.Fatal("setup failed")
	}
	c := &probeCheckpoint{Version: 1, Address: "127.0.0.1:12345", Home: filepath.Join(dir, "synthetic-home"), Secret: strings.Repeat("x", 43), Slot: "a", AgentOwners: registry.snapshot()}
	if writeProbeCheckpoint(dir, c) != nil {
		t.Fatal("save failed")
	}
	loaded, err := readProbeCheckpoint(dir)
	if err != nil || !reflect.DeepEqual(loaded.AgentOwners, c.AgentOwners) {
		t.Fatal("ownership not restored")
	}
	var restored probeAgentOwners
	for _, owner := range loaded.AgentOwners {
		restored.accept(map[[32]byte]bool{owner.Message: true}, owner.Credential)
	}
	if !restored.permits(message, credential) || restored.permits(message, sha256.Sum256([]byte("other-account"))) {
		t.Fatal("restored ownership changed")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if strings.Contains(string(data), message) || strings.Contains(string(data), "synthetic-credential") {
		t.Fatal("checkpoint leaked content")
	}
	for _, invalid := range [][]probeCheckpointAgentOwner{
		{{Message: [32]byte{1}}}, {{Credential: credential}},
		{c.AgentOwners[0], c.AgentOwners[0]}, make([]probeCheckpointAgentOwner, probeAgentOwnerLimit+1),
	} {
		c.AgentOwners = invalid
		if writeProbeCheckpoint(dir, c) != nil {
			t.Fatal("invalid fixture write failed")
		}
		if _, err := readProbeCheckpoint(dir); err == nil {
			t.Fatal("invalid ownership checkpoint accepted")
		}
	}
	// Combined registries must fit the bounded checkpoint reader at capacity.
	c.AgentOwners = nil
	for i := 0; i < probeAgentOwnerLimit; i++ {
		c.AgentOwners = append(c.AgentOwners, probeCheckpointAgentOwner{sha256.Sum256([]byte(fmt.Sprint(i))), credential})
	}
	for i := 0; i < 1024; i++ {
		c.Owners = append(c.Owners, probeCheckpointOwner{Key: sha256.Sum256([]byte(fmt.Sprint(i))), Credential: credential, Slot: "a"})
	}
	if writeProbeCheckpoint(dir, c) != nil {
		t.Fatal("capacity save failed")
	}
	if _, err := readProbeCheckpoint(dir); err != nil {
		t.Fatal("capacity checkpoint unreadable")
	}
}

// Exercises the real auxiliary resolver without opening a port: a changed
// credential must stop the request before the handler makes any attempt.
func TestProbeAgentDispatchGuard(t *testing.T) {
	for _, mode := range []string{"unknown", "account", "registration", "dispatch", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			source := newHistoryAccess()
			credential, err := probeHistoryCredential(source, "a")
			if err != nil {
				t.Fatal(err)
			}
			owners := &probeAgentOwners{}
			message := "synthetic-parent-opaque"
			if mode != "unknown" {
				owners.accept(map[[32]byte]bool{sha256.Sum256([]byte(message)): true}, credential)
			}
			switch mode {
			case "account", "registration":
				source.change(mode, false)
			case "dispatch":
				source.change("account", true)
			}
			a := &probeAuxiliary{binding: probeAuxiliaryBinding{Thread: "synthetic-child", Slot: "a"}, agentOwners: owners}
			defer func() {
				if a.handler != nil {
					a.handler.Close()
				}
			}()
			body := `{"input":[{"role":"user","content":"repository context"},{"type":"agent_message","author":"parent","recipient":"child","content":[{"type":"input_text","text":"TASK Payload:"},{"type":"encrypted_content","encrypted_content":"synthetic-parent-opaque"}]}]}`
			r := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
			if mode == "canceled" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			var mu sync.Mutex
			w := httptest.NewRecorder()
			a.serve(w, r, &mu, source, "http://127.0.0.1:1", "synthetic", func() bool { return true }, nil)
			if w.Code == 200 || !a.failed || a.busy || a.handler != nil && a.handler.Diagnostics().Attempts != 0 {
				t.Fatal("unsafe task reached network or failed without locking")
			}
			if mode == "dispatch" && (a.handler == nil || a.handler.Diagnostics().Rejection != "auxiliary_credential_changed") {
				t.Fatal("dispatch identity change was not checked")
			}
		})
	}
}
