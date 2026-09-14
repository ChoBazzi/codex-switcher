package main

import (
	"bufio"
	"encoding/json"
	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/cliprobe"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type syntheticProbeAccess struct{}

func (syntheticProbeAccess) Access(slot string, now time.Time) (accounts.Access, error) {
	return accounts.Access{Token: "synthetic-" + slot, AccountID: "synthetic-" + slot, ExpiresAt: now.Add(time.Hour)}, nil
}

func TestProbeManagedSelection(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	fixture := &cliprobe.Upstream{Scenario: "success"}
	up := httptest.NewServer(fixture)
	defer up.Close()
	input, writer := io.Pipe()
	output, sink := io.Pipe()
	defer writer.Close()
	defer input.Close()
	defer output.Close()
	defer sink.Close()
	done := make(chan error, 1)
	go func() {
		done <- switchProbeWithAccess([]string{"--allow-live", "--managed"}, input, sink, syntheticProbeAccess{}, up.URL)
	}()
	events := make(chan map[string]any, 16)
	go func() {
		s := bufio.NewScanner(output)
		for s.Scan() {
			var v map[string]any
			if json.Unmarshal(s.Bytes(), &v) == nil {
				events <- v
			}
		}
	}()
	next := func(event string) map[string]any {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case v := <-events:
				if v["event"] == event {
					return v
				}
			case <-timer.C:
				t.Fatal("managed event timeout")
				return nil
			}
		}
	}
	next("probe_ready")
	state := next("probe_state")
	if state["slot"] != "a" || state["revision"] != float64(0) {
		t.Fatal("bad initial state")
	}
	io.WriteString(writer, "{\"action\":\"select\",\"slot\":\"b\",\"revision\":0}\n")
	ack := next("probe_selection")
	if ack["accepted"] != true || ack["slot"] != "b" {
		t.Fatal("selection rejected")
	}
	io.WriteString(writer, "{\"action\":\"select\",\"slot\":\"a\",\"revision\":0}\n")
	ack = next("probe_selection")
	if ack["accepted"] != false || ack["slot"] != "b" {
		t.Fatal("stale selection accepted")
	}
	io.WriteString(writer, "a\n")
	if next("probe_selection")["accepted"] != false {
		t.Fatal("managed accepted raw command")
	}
	if fixture.Calls.Load() != 0 {
		t.Fatal("selection invoked model")
	}
	for i, slot := range []string{"c", "d", "e"} {
		command, _ := json.Marshal(map[string]any{"action": "select", "slot": slot, "revision": i + 1})
		writer.Write(append(command, '\n'))
		selection := next("probe_selection")
		if selection["accepted"] != true || selection["slot"] != slot {
			t.Fatal("additional slot selection failed")
		}
	}
	if fixture.Calls.Load() != 0 {
		t.Fatal("five-slot selection invoked model")
	}
	encoded, _ := json.Marshal(ack)
	if strings.Contains(string(encoded), "synthetic-") {
		t.Fatal("credentials leaked")
	}
	writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("EOF did not stop server")
	}
}

func TestProbeSelectionBoundary(t *testing.T) {
	if !probeSelectionAllowed("b", 2, 2, false, false) {
		t.Fatal("valid selection rejected")
	}
	for _, tc := range []struct {
		target             string
		expected, revision uint64
		busy, failed       bool
	}{
		{"f", 2, 2, false, false}, {"b", 1, 2, false, false}, {"b", 2, 2, true, false}, {"b", 2, 2, false, true},
	} {
		if probeSelectionAllowed(tc.target, tc.expected, tc.revision, tc.busy, tc.failed) {
			t.Fatal("unsafe selection accepted")
		}
	}
}
