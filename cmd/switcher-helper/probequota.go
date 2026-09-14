package main

import (
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

const probeQuotaReservePercent = 5.0

// Called only at request admission, never while an upstream request is running.
// Empty current means initial selection; otherwise retain it above the reserve.
func probeQuotaSelection(current string, samples []usage.Snapshot, now time.Time) (string, string) {
	eligible := map[string]float64{}
	exhausted := map[string]bool{}
	for _, s := range samples {
		if !accountslot.Valid(s.Slot) {
			continue
		}
		s = s.At(now)
		if s.State == "not_registered" && s.Registered != nil && !*s.Registered {
			exhausted[s.Slot] = true
			continue
		}
		if s.Stale || s.LastSuccess.After(now) || s.Usage == nil || (s.State != "ok" && s.State != "limit_reached") {
			continue
		}
		d := s.Usage
		if (d.LimitReached != nil && *d.LimitReached) || (d.Allowed != nil && !*d.Allowed) || (d.RemainingPercent != nil && *d.RemainingPercent >= 0 && *d.RemainingPercent <= probeQuotaReservePercent) {
			exhausted[s.Slot] = true
			continue
		}
		if d.Allowed != nil && *d.Allowed && d.LimitReached != nil && !*d.LimitReached && d.RemainingPercent != nil && *d.RemainingPercent > probeQuotaReservePercent && *d.RemainingPercent <= 100 {
			eligible[s.Slot] = *d.RemainingPercent
		}
	}
	if current != "" {
		if _, ok := eligible[current]; ok {
			return current, "retained"
		}
		if !exhausted[current] {
			return "", "probe_usage_unavailable"
		}
	}
	best := ""
	for _, slot := range accountslot.All() {
		if remaining, ok := eligible[slot]; ok && (best == "" || remaining > eligible[best]) {
			best = slot
		}
	}
	if best == "" {
		return "", "probe_no_available_account"
	}
	if current == "" {
		return best, "initial_usage_selection"
	}
	return best, "quota_exhausted_switch"
}
