package routing

import (
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/handoff"
)

// Target checks cached quota and local credentials, without a model request.
func (r *Router) target(slot string, now time.Time) bool {
	if !accountslot.Valid(slot) {
		return false
	}
	left, ok := r.remaining(slot, now)
	if !ok || left <= 100-r.threshold {
		return false
	}
	_, err := r.access.Access(slot, now)
	return err == nil
}

func (r *Router) PrepareHandoff(source checkpoint.Origin, target string, snap checkpoint.Snapshot, boundary bool, now time.Time) (affinity.Reservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disk == nil {
		return affinity.Reservation{}, ErrIdentity
	}
	if snap.Metadata.Origin != source {
		return affinity.Reservation{}, ErrIdentity
	}
	checked, err := checkpoint.CheckBytes([]byte(snap.Markdown), snap.Metadata)
	if err != nil || checked.SHA256 != snap.SHA256 {
		return affinity.Reservation{}, ErrIdentity
	}
	if !r.target(target, now) {
		return affinity.Reservation{}, ErrUnavailable
	}
	return r.disk.PrepareHandoff(source, target, snap.Metadata.ID, snap.SHA256, boundary, now)
}

// BindHandoff verifies the pinned snapshot again and targets only the reserved
// slot. It never falls back to ordinary registration or reuses source history.
func (r *Router) BindHandoff(id string, origin checkpoint.Origin, snap checkpoint.Snapshot, userInput bool, now time.Time) (handoff.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !userInput {
		return handoff.Session{}, handoff.ErrInputRequired
	}
	if r.disk == nil {
		return handoff.Session{}, ErrIdentity
	}
	reservation, err := r.disk.Handoff(id)
	if err != nil {
		return handoff.Session{}, err
	}
	if reservation.ConsumedBy != "" || snap.Metadata.Origin != reservation.Source || snap.Metadata.ID != reservation.CheckpointID || snap.SHA256 != reservation.Digest {
		return handoff.Session{}, ErrIdentity
	}
	checked, err := checkpoint.CheckBytes([]byte(snap.Markdown), snap.Metadata)
	if err != nil || checked.SHA256 != reservation.Digest {
		return handoff.Session{}, ErrIdentity
	}
	if !r.target(reservation.Target, now) {
		return handoff.Session{}, ErrUnavailable
	}
	return r.disk.BindHandoff(id, origin, true, now)
}
