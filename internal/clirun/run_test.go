package clirun

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestEvents(t *testing.T) {
	start := "{\"type\":\"thread.started\",\"thread_id\":\"synthetic\"}\n"
	end := "{\"type\":\"turn.completed\"}\n"
	for _, tc := range []struct {
		name, events    string
		reject, success bool
	}{
		{"success", start + "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"synthetic answer\"}}\n" + end, false, true},
		{"missing start", end, false, false},
		{"duplicate start", start + start + end, false, false},
		{"incomplete", start, false, false},
		{"failure", start + "{\"type\":\"turn.failed\"}\n", false, false},
		{"invalid json", start + "invalid\n", false, false},
		{"rejected registration", start + end, true, false},
		{"oversize", start + strings.Repeat("x", 1<<20), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := consume(strings.NewReader(tc.events), func(string) error {
				if tc.reject {
					return errors.New("synthetic")
				}
				return nil
			}, &out)
			if (err == nil) != tc.success {
				t.Fatalf("unexpected result %v", err)
			}
			if tc.success && out.String() != "synthetic answer\n" {
				t.Fatal("answer missing")
			}
		})
	}
}

func TestFailureStageRedaction(t *testing.T) {
	err := consume(strings.NewReader("{\"type\":\"error\",\"message\":\"synthetic-secret\"}\n"), func(string) error { return nil }, &bytes.Buffer{})
	if FailureStage(err) != "cli_error_event" || !errors.Is(err, ErrRun) {
		t.Fatal("missing typed failure")
	}
	if strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("event leaked")
	}
	if FailureStage(errors.New("synthetic-secret")) != "run_or_cleanup_failed" {
		t.Fatal("raw error exposed")
	}
}
