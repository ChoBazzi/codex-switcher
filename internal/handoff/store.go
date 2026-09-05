// Package handoff binds explicit handoffs to new conversations, never by guesswork.
package handoff

import (
	"crypto/rand"
	"errors"
	"sync"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

var (
	ErrConflict      = errors.New("handoff_conflict")
	ErrIdentity      = errors.New("handoff_identity_mismatch")
	ErrUnavailable   = errors.New("account_unavailable")
	ErrInputRequired = errors.New("user_input_required")
	ErrBoundary      = errors.New("safe_boundary_required")
)

type Session struct {
	Origin  checkpoint.Origin
	Account string
	// Wiki is empty for ordinary sessions, pinned for explicit handoffs.
	Wiki string
}

type reservation struct {
	source     checkpoint.Origin
	target     string
	snapshot   checkpoint.Snapshot
	consumedBy string
}

// Store is Phase 0 in-memory state. Callers are trusted helper components, not
// arbitrary HTTP callers. UserInput and boundary must come from verified events.
type Store struct {
	mu            sync.Mutex
	accounts      map[string]bool
	sessions      map[string]Session
	reservations  map[string]*reservation
	sourcePending map[string]string
}

func New() *Store {
	return &Store{accounts: map[string]bool{}, sessions: map[string]Session{}, reservations: map[string]*reservation{}, sourcePending: map[string]string{}}
}

func (s *Store) SetAccount(id string, available bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		return ErrIdentity
	}
	if _, exists := s.accounts[id]; !exists && len(s.accounts) >= 2 {
		return ErrConflict
	}
	s.accounts[id] = available
	return nil
}

// Register starts an ordinary session and never consumes a pending reservation.
func (s *Store) Register(origin checkpoint.Origin, account string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validOrigin(origin) {
		return Session{}, ErrIdentity
	}
	if _, exists := s.sessions[origin.Session]; exists {
		return Session{}, ErrConflict
	}
	if !s.accounts[account] {
		return Session{}, ErrUnavailable
	}
	x := Session{Origin: origin, Account: account}
	s.sessions[origin.Session] = x
	return x, nil
}

// Prepare records state only: it does not call a model or move the source session.
func (s *Store) Prepare(source, target string, snap checkpoint.Snapshot, boundary bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !boundary {
		return "", ErrBoundary
	}
	x, ok := s.sessions[source]
	if !ok || snap.Metadata.Origin != x.Origin || snap.Metadata.ID == "" || snap.Markdown == "" {
		return "", ErrIdentity
	}
	if !s.accounts[target] {
		return "", ErrUnavailable
	}
	if target == x.Account || s.sourcePending[source] != "" {
		return "", ErrConflict
	}
	id := rand.Text()
	s.reservations[id] = &reservation{source: x.Origin, target: target, snapshot: snap}
	s.sourcePending[source] = id
	return id, nil
}

// Bind requires an explicit reservation and a NEW conversation with user input.
// Subsequent requests use Session, not Bind. Concurrent claims have one winner.
func (s *Store) Bind(id string, origin checkpoint.Origin, userInput bool) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !userInput {
		return Session{}, ErrInputRequired
	}
	r, ok := s.reservations[id]
	if !ok || !validOrigin(origin) {
		return Session{}, ErrIdentity
	}
	if r.consumedBy != "" {
		return Session{}, ErrConflict
	}
	if _, exists := s.sessions[origin.Session]; exists {
		return Session{}, ErrConflict
	}
	if origin.Project != r.source.Project || origin.Worktree != r.source.Worktree || origin.Branch != r.source.Branch {
		return Session{}, ErrIdentity
	}
	if !s.accounts[r.target] {
		return Session{}, ErrUnavailable
	}
	x := Session{Origin: origin, Account: r.target, Wiki: r.snapshot.Markdown}
	s.sessions[origin.Session] = x
	r.consumedBy = origin.Session
	delete(s.sourcePending, r.source.Session)
	return x, nil
}

func (s *Store) Session(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.sessions[id]
	return x, ok
}

func validOrigin(o checkpoint.Origin) bool {
	return o.Project != "" && o.Worktree != "" && o.Branch != "" && o.Session != ""
}
