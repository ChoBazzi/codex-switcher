package main

import (
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

func TestProbeQuotaSelection(t *testing.T) {
	now := time.Now()
	sample := func(slot string, remaining float64) usage.Snapshot {
		allowed, reached := remaining > 0, remaining == 0
		state := "ok"
		if reached {
			state = "limit_reached"
		}
		return usage.Snapshot{Slot: slot, State: state, LastSuccess: &now, Usage: &usage.Data{Allowed: &allowed, LimitReached: &reached, RemainingPercent: &remaining}}
	}
	many := []usage.Snapshot{sample("a", 5), sample("b", 2), sample("c", 40), sample("d", 60), sample("e", 90)}
	if got, _ := probeQuotaSelection("a", many, now); got != "e" {
		t.Fatal("fifth account not selected")
	}
	if got, _ := probeQuotaSelection("c", many, now); got != "c" {
		t.Fatal("usable current account changed")
	}
	many[4] = sample("e", 60)
	if got, _ := probeQuotaSelection("a", many, now); got != "d" {
		t.Fatal("slot-order tie broken")
	}
	registered := false
	many[0] = usage.Snapshot{Slot: "a", State: "not_registered", Registered: &registered, Stale: true}
	if got, _ := probeQuotaSelection("a", many, now); got != "d" {
		t.Fatal("logged-out current slot retained")
	}
	for _, tc := range []struct {
		name, current string
		a, b          float64
		want, reason  string
	}{
		{"initial", "", 20, 80, "b", "initial_usage_selection"},
		{"tie", "", 80, 80, "a", "initial_usage_selection"},
		{"retain", "a", 5.01, 100, "a", "retained"},
		{"at threshold", "a", 5, 80, "b", "quota_exhausted_switch"},
		{"below threshold", "a", 4.99, 80, "b", "quota_exhausted_switch"},
		{"reverse threshold", "b", 80, 5, "a", "quota_exhausted_switch"},
		{"both reserve", "a", 5, 4, "", "probe_no_available_account"},
		{"initial reserve", "", 5, 5, "", "probe_no_available_account"},
		{"switch", "a", 0, 80, "b", "quota_exhausted_switch"},
		{"reverse", "b", 80, 0, "a", "quota_exhausted_switch"},
		{"both empty", "a", 0, 0, "", "probe_no_available_account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := probeQuotaSelection(tc.current, []usage.Snapshot{sample("a", tc.a), sample("b", tc.b)}, now)
			if got != tc.want || reason != tc.reason {
				t.Fatalf("got %q %q", got, reason)
			}
		})
	}
	for _, mode := range []string{"stale", "old", "future", "missing_time", "missing_usage", "missing_allowed", "missing_remaining", "fetch_error"} {
		t.Run(mode, func(t *testing.T) {
			a := sample("a", 20)
			switch mode {
			case "stale":
				a.Stale = true
			case "old":
				date := now.Add(-usage.StaleAfter)
				a.LastSuccess = &date
			case "future":
				date := now.Add(time.Second)
				a.LastSuccess = &date
			case "missing_time":
				a.LastSuccess = nil
			case "missing_usage":
				a.Usage = nil
			case "missing_allowed":
				a.Usage.Allowed = nil
			case "missing_remaining":
				a.Usage.RemainingPercent = nil
			case "fetch_error":
				a.State = "fetch_error"
			}
			if got, reason := probeQuotaSelection("a", []usage.Snapshot{a, sample("b", 100)}, now); got != "" || reason != "probe_usage_unavailable" {
				t.Fatalf("got %q %q", got, reason)
			}
		})
	}
}
