package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestProbeParsedInputOwnershipMetadata(t *testing.T) {
	for _, tc := range []struct {
		body    string
		needs   bool
		compact int
	}{
		{historyFirst, false, 0},
		{historyFollowup, true, 0},
		{historyCompact, false, 1},
		{probeToolHistory, false, 0},
		{`{"input":[{"role":"user","content":"run"},{"type":"function_call","id":"old-id","call_id":"c","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"done"}]}`, true, 0},
	} {
		d := parseProbeToolInput([]byte(tc.body))
		boundary, hasUser := probeItemsUserBoundary(d.items)
		before, beforeOK := probeUserBoundary([]byte(tc.body))
		if boundary != before || hasUser != beforeOK || !hasUser {
			t.Fatal("recovery boundary changed before validation")
		}
		compacts := d.compactItems()
		beforeCompact, _ := json.Marshal(compacts)
		body, err := d.normalize("a", "a", "synthetic", func(map[string]json.RawMessage) bool { return true }, func(map[string]json.RawMessage) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		if d.needsTurnOwner != tc.needs || len(compacts) != tc.compact {
			t.Fatal("dispatch ownership metadata changed")
		}
		afterCompact, _ := json.Marshal(compacts)
		if string(beforeCompact) != string(afterCompact) {
			t.Fatal("canonical compact input mutated")
		}
		if strings.Contains(string(body), "old-id") || strings.Contains(string(body), "old-opaque") {
			t.Fatal("portable history retained old server references")
		}
	}
}

func TestProbeParsedInputRejectsInvalidShapes(t *testing.T) {
	for _, tc := range []struct{ body, reason string }{
		{`null`, "invalid_json_object"},
		{`{"input":[]} trailing`, "invalid_json_object"},
		{`{"input":null}`, "input_not_message_array"},
		{`{"input":42}`, "input_not_message_array"},
		{`{"input":[],"conversation":"server-secret"}`, "server_reference_conversation"},
		{`{"input":null,"conversation":"server-secret"}`, "server_reference_conversation"},
		{`{"input":[{"role":"user","content":"ok","phase":"commentary"}]}`, "message_shape_unsupported"},
		{`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"synthetic"}]}]}`, "message_shape_unsupported"},
		{historyFollowup, "reasoning_owner_unavailable"},
		{historyCompact, "compaction_owner_unavailable"},
	} {
		d := parseProbeToolInput([]byte(tc.body))
		_, err := d.normalize("a", "", "synthetic", nil, nil)
		var parsed *probeBodyError
		if !errors.As(err, &parsed) || parsed.Reason != tc.reason {
			t.Fatalf("unexpected rejection for %s: %v", tc.reason, err)
		}
	}
}

func TestProbeCheckpointRegistryComparison(t *testing.T) {
	a := &probeCheckpoint{Version: 1, Slot: "a", Owners: []probeCheckpointOwner{
		{Key: sha256.Sum256([]byte("synthetic-one")), Slot: "a"},
		{Key: sha256.Sum256([]byte("synthetic-two")), Slot: "b"},
	}, Auxiliary: []probeAuxiliaryBinding{{Thread: "synthetic-child", Root: "synthetic-root", Slot: "a"}}}
	registry := func(c *probeCheckpoint) probeCompactRegistry {
		owners := probeCompactRegistry{}
		for _, o := range c.Owners {
			owners[o.Key] = probeCompactOwner{o.Slot, o.Credential}
		}
		return owners
	}
	compare := func(next probeCheckpoint, expected bool) {
		t.Helper()
		owners := registry(&next)
		want := sameProbeCheckpoint(a, &next)
		next.Owners = nil // production does not build the array for an unchanged poll
		if want != expected || sameProbeCheckpointRegistry(a, &next, owners) != want {
			t.Fatal("registry comparison differs from full persisted snapshot")
		}
	}
	compare(*a, true)
	// Every scalar field participates, including failure/replay gates and auth.
	for i := 0; i < reflect.TypeOf(*a).NumField(); i++ {
		b := *a
		field := reflect.ValueOf(&b).Elem().Field(i)
		switch field.Kind() {
		case reflect.Bool:
			field.SetBool(!field.Bool())
		case reflect.String:
			field.SetString(field.String() + "-changed")
		case reflect.Int:
			field.SetInt(field.Int() + 1)
		case reflect.Uint64:
			field.SetUint(field.Uint() + 1)
		case reflect.Array:
			field.Index(0).SetUint(field.Index(0).Uint() ^ 1)
		default:
			continue
		}
		compare(b, false)
	}
	b := *a
	b.Owners = []probeCheckpointOwner{a.Owners[1], a.Owners[0]}
	compare(b, true)
	b.Owners = append([]probeCheckpointOwner(nil), a.Owners...)
	b.Owners[0].Credential[0] = 1
	compare(b, false)
	b.Owners = a.Owners[:1]
	compare(b, false)
	b = *a
	b.Auxiliary = []probeAuxiliaryBinding{{Thread: "different", Root: "synthetic-root", Slot: "a"}}
	compare(b, false)
	if sameProbeCheckpointRegistry(nil, a, registry(a)) {
		t.Fatal("initial write skipped")
	}
}
