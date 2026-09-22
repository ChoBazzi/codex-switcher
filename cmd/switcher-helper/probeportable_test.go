package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

func TestQuotaPortableHistory(t *testing.T) {
	body := `{"input":[{"role":"user","content":"finish task"},{"type":"reasoning","id":"old-reason","summary":[],"encrypted_content":"old-opaque"},{"type":"function_call","id":"old-item","call_id":"old-call","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"old-call","output":"already completed"}]}`
	parsed := parseProbeToolInput([]byte(body))
	parsed.portable = true
	normalized, err := parsed.normalize("b", "", "synthetic-salt", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"old-opaque", "old-reason", "old-item", "old-call"} {
		if strings.Contains(string(normalized), forbidden) {
			t.Fatal("old account reference escaped")
		}
	}
	if !strings.Contains(string(normalized), "already completed") || !strings.Contains(string(normalized), "finish task") || parsed.needsTurnOwner {
		t.Fatal("portable context invalid")
	}
	var out struct{ Input []map[string]json.RawMessage }
	json.Unmarshal(normalized, &out)
	if probeString(out.Input[1], "call_id") != probeString(out.Input[2], "call_id") {
		t.Fatal("tool pair broken")
	}
	for _, bad := range []string{
		`{"input":[{"role":"user","content":"x"},{"type":"compaction","encrypted_content":"secret"}]}`,
		`{"input":[{"role":"user","content":"x"},{"type":"function_call","name":"read","call_id":"unfinished","arguments":"{}"}]}`,
		`{"previous_response_id":"secret","input":[{"role":"user","content":"x"}]}`,
		`{"input":[{"role":"user","content":"x"},{"type":"reasoning","summary":null}]}`,
	} {
		p := parseProbeToolInput([]byte(bad))
		p.portable = true
		if _, err := p.normalize("b", "", "salt", nil, nil); err == nil {
			t.Fatal("unsafe portable history accepted")
		}
	}
}

func TestQuotaRejectionOverridesInflightObservation(t *testing.T) {
	limits := &probeLimitState{}
	before := time.Now()
	allowed, reached, remaining := true, false, 80.0
	samples := []usage.Snapshot{{Slot: "a", State: "ok", LastAttempt: before, LastSuccess: &before, Usage: &usage.Data{Allowed: &allowed, LimitReached: &reached, RemainingPercent: &remaining}}}
	limits.mark("a")
	after := time.Now()
	samples[0].LastSuccess = &after
	blocked := limits.apply(samples)
	if candidate, _ := probeQuotaSelection("", blocked, time.Now()); candidate != "" {
		t.Fatal("inflight quota round restored rejected account")
	}
	if *samples[0].Usage.RemainingPercent != 80 {
		t.Fatal("shared original mutated")
	}
	samples[0].LastAttempt = time.Now()
	if candidate, _ := probeQuotaSelection("", limits.apply(samples), time.Now()); candidate != "a" {
		t.Fatal("fresh later observation did not restore account")
	}
}
