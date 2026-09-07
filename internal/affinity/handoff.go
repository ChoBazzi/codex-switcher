package affinity

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/handoff"
)

// Reservation contains local identity only. Wiki bytes must be separately
// pinned and verified against Digest before the launcher supplies them.
type Reservation struct {
	ID           string
	Source       checkpoint.Origin
	Target       string
	CheckpointID string
	Digest       string
	ConsumedBy   string
}

func validDigest(d string) bool {
	b, err := hex.DecodeString(d)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == d
}

// PrepareHandoff does not create a target session or transfer any response ID.
// The trusted caller must have validated/pinned the snapshot and safe boundary.
func (s *Store) PrepareHandoff(source checkpoint.Origin, target, checkpointID, digest string, boundary bool, now time.Time) (Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !boundary {
		return Reservation{}, handoff.ErrBoundary
	}
	if !validOrigin(source) || !valid(checkpointID) || !validDigest(digest) || (target != "a" && target != "b") {
		return Reservation{}, ErrConflict
	}
	r := Reservation{ID: rand.Text(), Source: source, Target: target, CheckpointID: checkpointID, Digest: digest}
	err := s.transaction(func() error {
		x, state, _, err := s.lookup(source.Session)
		if err != nil {
			return err
		}
		if x.Origin != source || x.Account == target {
			return ErrConflict
		}
		if state != "ready" {
			return ErrBlocked
		}
		rows, err := s.db.query("SELECT id FROM handoffs WHERE source=? AND consumed_by=''", source.Session)
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			return ErrConflict
		}
		_, err = s.db.query("INSERT INTO handoffs(id,source,target,checkpoint,digest,created) VALUES(?,?,?,?,?,?)", r.ID, source.Session, target, checkpointID, digest, stamp(now))
		return err
	})
	if err != nil {
		return Reservation{}, err
	}
	return r, nil
}

func (s *Store) reservation(id string) (Reservation, error) {
	if s.db == nil {
		return Reservation{}, ErrStorage
	}
	rows, err := s.db.query("SELECT source,target,checkpoint,digest,consumed_by FROM handoffs WHERE id=?", id)
	if err != nil {
		return Reservation{}, err
	}
	if len(rows) != 1 {
		return Reservation{}, ErrUnknown
	}
	v := rows[0]
	x, _, _, err := s.lookup(v[0])
	if err != nil {
		return Reservation{}, err
	}
	return Reservation{id, x.Origin, v[1], v[2], v[3], v[4]}, nil
}

func (s *Store) Handoff(id string) (Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reservation(id)
}

func (s *Store) CheckpointReserved(source, checkpointID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return false, ErrStorage
	}
	rows, err := s.db.query("SELECT id FROM handoffs WHERE source=? AND checkpoint=?", source, checkpointID)
	return len(rows) != 0, err
}

// CancelHandoff releases a pending source; a consumed reservation is immutable.
func (s *Store) CancelHandoff(id string, source checkpoint.Origin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transaction(func() error {
		r, err := s.reservation(id)
		if err != nil {
			return err
		}
		if r.Source != source || r.ConsumedBy != "" {
			return ErrConflict
		}
		_, err = s.db.query("DELETE FROM handoffs WHERE id=?", id)
		return err
	})
}

// BindHandoff atomically registers a NEW conversation and consumes the explicit
// reservation. The caller verifies target availability and pinned Wiki bytes.
func (s *Store) BindHandoff(id string, origin checkpoint.Origin, userInput bool, now time.Time) (handoff.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !userInput {
		return handoff.Session{}, handoff.ErrInputRequired
	}
	if !validOrigin(origin) {
		return handoff.Session{}, ErrConflict
	}
	var result handoff.Session
	err := s.transaction(func() error {
		r, err := s.reservation(id)
		if err != nil {
			return err
		}
		if r.ConsumedBy != "" || origin.Session == r.Source.Session || origin.Project != r.Source.Project || origin.Worktree != r.Source.Worktree || origin.Branch != r.Source.Branch {
			return ErrConflict
		}
		_, state, _, err := s.lookup(r.Source.Session)
		if err != nil {
			return err
		}
		if state != "ready" {
			return ErrBlocked
		}
		rows, err := s.db.query("SELECT id FROM sessions WHERE id=?", origin.Session)
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			return ErrConflict
		}
		if _, err := s.db.query("INSERT INTO sessions(id,project,worktree,branch,slot,last_seen,state) VALUES(?,?,?,?,?,?,'ready')", origin.Session, origin.Project, origin.Worktree, origin.Branch, r.Target, stamp(now)); err != nil {
			return err
		}
		if _, err := s.db.query("UPDATE handoffs SET consumed_by=? WHERE id=? AND consumed_by=''", origin.Session, id); err != nil {
			return err
		}
		result = handoff.Session{Origin: origin, Account: r.Target}
		return nil
	})
	if err != nil {
		return handoff.Session{}, err
	}
	return result, nil
}
