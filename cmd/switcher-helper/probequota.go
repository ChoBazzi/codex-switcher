package main

import (
	"encoding/json"
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"net/http"
	"sync"
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

// Exclude every account attempted for this CLI request, even if its cached
// percentage has not caught up with the explicit upstream exhaustion response.
func probeQuotaAlternative(samples []usage.Snapshot, tried map[string]bool, now time.Time) string {
	candidates := make([]usage.Snapshot, 0, len(samples))
	for _, sample := range samples {
		if !tried[sample.Slot] {
			candidates = append(candidates, sample)
		}
	}
	next, _ := probeQuotaSelection("", candidates, now)
	return next
}
func writeProbeUsageLimit(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": "usage_limit_reached", "code": "usage_limit_reached", "message": "Usage limit reached; no safe automatic account switch is available."}})
}

// An explicit model limit outranks quota observations started before that
// rejection. All connections share this small, nonpersistent overlay.
type probeLimitState struct {
	mu       sync.Mutex
	rejected map[string]time.Time
}

func (l *probeLimitState) mark(slot string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rejected == nil {
		l.rejected = map[string]time.Time{}
	}
	l.rejected[slot] = time.Now()
}
func (l *probeLimitState) apply(samples []usage.Snapshot) []usage.Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := append([]usage.Snapshot(nil), samples...)
	for i, s := range result {
		at, limited := l.rejected[s.Slot]
		if !limited {
			continue
		}
		if s.LastAttempt.After(at) && !s.Stale && s.Usage != nil && (s.State == "ok" || s.State == "limit_reached") {
			delete(l.rejected, s.Slot)
			continue
		}
		denied, reached, remaining := false, true, 0.0
		result[i].State = "limit_reached"
		result[i].Usage = &usage.Data{Allowed: &denied, LimitReached: &reached, RemainingPercent: &remaining}
		// This is a local observation of exhaustion, not permission to use an
		// unknown or stale alternative. Selection still validates alternatives.
		now := time.Now()
		result[i].LastSuccess = &now
		result[i].Stale = false
	}
	return result
}
