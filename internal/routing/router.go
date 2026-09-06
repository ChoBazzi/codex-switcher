// Package routing selects accounts only at explicit, trusted new-session events.
// It does not infer new sessions or user input from arbitrary HTTP headers.
package routing

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/handoff"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

var (
	ErrUnavailable = errors.New("routing_account_unavailable")
	ErrIdentity    = errors.New("routing_session_unknown_or_mismatched")
	ErrPolicy      = errors.New("routing_invalid_threshold")
)

// Router owns the Phase 0 handoff store. No credentials are cached here.
type Router struct {
	mu        sync.Mutex
	sessions  *handoff.Store
	access    usage.AccessSource
	threshold float64
	samples   map[string]usage.Snapshot
}

// New accepts a used-percent threshold in [80,95]; pass 90 for the default.
func New(access usage.AccessSource, threshold float64) (*Router, error) {
	if access == nil || math.IsNaN(threshold) || threshold < 80 || threshold > 95 {
		return nil, ErrPolicy
	}
	return &Router{sessions: handoff.New(), access: access, threshold: threshold, samples: map[string]usage.Snapshot{}}, nil
}

// Update copies observations so callers cannot mutate routing decisions later.
// Older attempts cannot overwrite a newer result (including a newer failure).
func (r *Router) Update(samples []usage.Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range samples {
		if s.Slot != "a" && s.Slot != "b" {
			continue
		}
		if old, ok := r.samples[s.Slot]; ok && !s.LastAttempt.After(old.LastAttempt) {
			continue
		}
		if s.LastSuccess != nil {
			v := *s.LastSuccess
			s.LastSuccess = &v
		}
		if s.Usage != nil {
			d := *s.Usage
			d.Primary = copyWindow(d.Primary)
			d.Secondary = copyWindow(d.Secondary)
			d.RemainingPercent = copyPtr(d.RemainingPercent)
			d.Allowed = copyPtr(d.Allowed)
			d.LimitReached = copyPtr(d.LimitReached)
			s.Usage = &d
		}
		r.samples[s.Slot] = s
	}
}
func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func copyWindow(w usage.Window) usage.Window {
	w.UsedPercent = copyPtr(w.UsedPercent)
	w.RemainingPercent = copyPtr(w.RemainingPercent)
	w.LimitSeconds = copyPtr(w.LimitSeconds)
	w.ResetAt = copyPtr(w.ResetAt)
	return w
}

func (r *Router) remaining(slot string, now time.Time) (float64, bool) {
	s, ok := r.samples[slot]
	if !ok || s.At(now).Stale || s.State != "ok" || s.ErrorCode != "" || s.LastSuccess.After(now) || s.LastAttempt.After(now) || s.Usage == nil {
		return 0, false
	}
	d := s.Usage
	if d.Allowed == nil || !*d.Allowed || d.LimitReached == nil || *d.LimitReached {
		return 0, false
	}
	// Recompute from both source windows, not a caller-supplied aggregate.
	remaining := 100.0
	for _, w := range []usage.Window{d.Primary, d.Secondary} {
		if w.UsedPercent == nil || math.IsNaN(*w.UsedPercent) || *w.UsedPercent < 0 || *w.UsedPercent > 100 {
			return 0, false
		}
		if w.ResetAt != nil && !w.ResetAt.After(now) {
			return 0, false
		}
		remaining = math.Min(remaining, 100-*w.UsedPercent)
	}
	return remaining, remaining > 0
}

// Register must only be called after the bridge verifies an ordinary NEW
// conversation and user input. Resumes and handoffs must not use this method.
// It never makes a model request. Ties prefer slot a, otherwise most remaining.
func (r *Router) Register(origin checkpoint.Origin, userInput bool, now time.Time) (handoff.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !userInput {
		return handoff.Session{}, handoff.ErrInputRequired
	}
	if _, ok := r.sessions.Session(origin.Session); ok {
		return handoff.Session{}, handoff.ErrConflict
	}
	chosen := ""
	best := -1.0
	for _, slot := range []string{"a", "b"} {
		left, ok := r.remaining(slot, now)
		if !ok || left <= 100-r.threshold {
			continue
		}
		// Local credential validation only, not an upstream attempt or retry.
		if _, err := r.access.Access(slot, now); err != nil {
			continue
		}
		if left > best {
			chosen, best = slot, left
		}
	}
	if chosen == "" {
		return handoff.Session{}, ErrUnavailable
	}
	if err := r.sessions.SetAccount(chosen, true); err != nil {
		return handoff.Session{}, err
	}
	return r.sessions.Register(origin, chosen)
}

// Resolve never registers or rebinds. Thresholds apply only to new sessions;
// existing sessions remain pinned, but require fresh usable quota and auth.
func (r *Router) Resolve(origin checkpoint.Origin, now time.Time) (proxy.Identity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions.Session(origin.Session)
	if !ok || s.Origin != origin {
		return proxy.Identity{}, ErrIdentity
	}
	if _, ok := r.remaining(s.Account, now); !ok {
		return proxy.Identity{}, ErrUnavailable
	}
	a, err := r.access.Access(s.Account, now)
	if err != nil {
		return proxy.Identity{}, ErrUnavailable
	}
	return proxy.Identity{Session: origin.Session, Token: a.Token, AccountID: a.AccountID}, nil
}

func (r *Router) Session(id string) (handoff.Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions.Session(id)
}
