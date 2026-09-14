package main

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
)

func TestProbeCompactOwnership(t *testing.T) {
	owners := probeCompactRegistry{}
	credential := sha256.Sum256([]byte("synthetic-token-a"))
	window := []byte(`{"object":"response.compaction","output":[{"type":"message","role":"user","content":"synthetic request"},{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-opaque"}]}`)
	if err := owners.accept(window, "a", credential); err != nil {
		t.Fatal(err)
	}
	var item map[string]json.RawMessage
	json.Unmarshal([]byte(`{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-opaque"}`), &item)
	if !owners.permits(item, "a", credential) {
		t.Fatal("owner rejected")
	}
	if owners.permits(item, "b", credential) || owners.permits(item, "a", sha256.Sum256([]byte("new-identity"))) {
		t.Fatal("opaque state crossed identity")
	}
	if owners.accept(window, "b", credential) == nil {
		t.Fatal("owner replaced")
	}
	request := []byte(`{"input":[{"type":"message","role":"user","content":"synthetic request"},{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-opaque"},{"role":"user","content":"next"}]}`)
	allow := func(item map[string]json.RawMessage) bool { return owners.permits(item, "a", credential) }
	result, err := probeToolBodyWithCompaction(request, "a", "a", "salt", allow)
	if err != nil || !strings.Contains(string(result), `"id":"cmp_synthetic"`) {
		t.Fatal("canonical opaque item changed")
	}
	if _, err := probeToolBody(request, "b", "a", "salt"); err == nil {
		t.Fatal("unchecked opaque item accepted")
	}
	for _, raw := range []string{
		`{"object":"response.compaction","output":[]}`,
		`{"object":"response.compaction","output":[{"role":"user","content":"x"},{"type":"compaction","encrypted_content":""}]}`,
		`{"object":"response.compaction","output":[{"role":"user","content":"x"},{"type":"compaction","encrypted_content":"uncommitted"},{"type":"item_reference","id":"unsafe"}]}`,
	} {
		if owners.accept([]byte(raw), "a", credential) == nil {
			t.Fatal("invalid compact window accepted")
		}
	}
	json.Unmarshal([]byte(`{"type":"compaction","encrypted_content":"uncommitted"}`), &item)
	if owners.permits(item, "a", credential) {
		t.Fatal("partial invalid response registered")
	}
}
