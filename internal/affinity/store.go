// Package affinity persists internal slot ownership, never credentials or bodies.
package affinity

import (
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/applock"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/handoff"
)

var (
	ErrStorage  = errors.New("affinity_storage_unavailable")
	ErrUnknown  = errors.New("affinity_owner_unknown")
	ErrConflict = errors.New("affinity_conflict")
	ErrBlocked  = errors.New("affinity_requires_user_action")
)

const Retention = 14 * 24 * time.Hour

type database interface {
	query(string, ...string) ([][]string, error)
	close() error
}
type Store struct {
	mu   sync.Mutex
	db   database
	lock *applock.Lock
}
type Lease struct {
	Session handoff.Session
	Request string
}

// Open requires a dedicated private directory. Only one helper may own it.
func Open(dir string) (*Store, error) {
	lock, err := applock.Acquire(dir)
	if err != nil {
		return nil, ErrStorage
	}
	db, err := openDatabase(dir)
	if err != nil {
		lock.Close()
		return nil, ErrStorage
	}
	s := &Store{db: db, lock: lock}
	if err = s.initialize(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) initialize() error {
	for _, q := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=DELETE", "PRAGMA synchronous=FULL"} {
		if _, err := s.db.query(q); err != nil {
			return err
		}
	}
	rows, err := s.db.query("PRAGMA user_version")
	if err != nil || len(rows) != 1 {
		return ErrStorage
	}
	if rows[0][0] != "0" && rows[0][0] != "1" {
		return ErrStorage
	}
	return s.transaction(func() error {
		for _, q := range []string{
			`CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, project TEXT NOT NULL, worktree TEXT NOT NULL, branch TEXT NOT NULL, slot TEXT NOT NULL CHECK(slot IN ('a','b')), last_seen INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('ready','inflight','blocked')), request TEXT NOT NULL DEFAULT '')`,
			`CREATE TABLE IF NOT EXISTS responses (id TEXT PRIMARY KEY, session TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE)`,
			`CREATE INDEX IF NOT EXISTS sessions_last_seen ON sessions(last_seen)`,
			`UPDATE sessions SET state='blocked',request='' WHERE state='inflight'`,
			`PRAGMA user_version=1`,
		} {
			if _, err := s.db.query(q); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.close()
	s.db = nil
	lockErr := s.lock.Close()
	if err != nil || lockErr != nil {
		return ErrStorage
	}
	return nil
}
func (s *Store) transaction(f func() error) error {
	if s.db == nil {
		return ErrStorage
	}
	if _, err := s.db.query("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer s.db.query("ROLLBACK")
	if err := f(); err != nil {
		return err
	}
	_, err := s.db.query("COMMIT")
	return err
}
func valid(v string) bool { return v != "" && len(v) <= 4096 && !strings.ContainsRune(v, 0) }
func validOrigin(o checkpoint.Origin) bool {
	return valid(o.Session) && valid(o.Project) && valid(o.Worktree) && valid(o.Branch)
}
func stamp(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

// Register is exclusively for verified new conversations, never unknown resumes.
func (s *Store) Register(o checkpoint.Origin, slot string, now time.Time) (handoff.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validOrigin(o) || (slot != "a" && slot != "b") {
		return handoff.Session{}, ErrConflict
	}
	err := s.transaction(func() error {
		rows, err := s.db.query("SELECT id FROM sessions WHERE id=?", o.Session)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			return ErrConflict
		}
		_, err = s.db.query("INSERT INTO sessions(id,project,worktree,branch,slot,last_seen,state) VALUES(?,?,?,?,?,?,'ready')", o.Session, o.Project, o.Worktree, o.Branch, slot, stamp(now))
		return err
	})
	if err != nil {
		return handoff.Session{}, err
	}
	return handoff.Session{Origin: o, Account: slot}, nil
}
func (s *Store) lookup(id string) (handoff.Session, string, string, error) {
	if s.db == nil {
		return handoff.Session{}, "", "", ErrStorage
	}
	rows, err := s.db.query("SELECT project,worktree,branch,slot,state,request FROM sessions WHERE id=?", id)
	if err != nil {
		return handoff.Session{}, "", "", err
	}
	if len(rows) != 1 {
		return handoff.Session{}, "", "", ErrUnknown
	}
	r := rows[0]
	return handoff.Session{Origin: checkpoint.Origin{Project: r[0], Worktree: r[1], Branch: r[2], Session: id}, Account: r[3]}, r[4], r[5], nil
}

// Lookup is diagnostic and preserves blocked ownership. Admission uses Begin.
func (s *Store) Lookup(id string) (handoff.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, _, _, err := s.lookup(id)
	return x, err
}
func (s *Store) Ready(o checkpoint.Origin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, state, _, err := s.lookup(o.Session)
	if err != nil {
		return err
	}
	if x.Origin != o {
		return ErrUnknown
	}
	if state != "ready" {
		return ErrBlocked
	}
	return nil
}

// Begin durably records intent BEFORE upstream I/O. Unknown or cross-session
// continuation references are denied, even when two sessions use the same slot.
func (s *Store) Begin(o checkpoint.Origin, refs []string, now time.Time) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lease Lease
	err := s.transaction(func() error {
		x, state, _, err := s.lookup(o.Session)
		if err != nil {
			return err
		}
		if x.Origin != o {
			return ErrUnknown
		}
		if state != "ready" {
			return ErrBlocked
		}
		for _, id := range refs {
			if !valid(id) {
				return ErrUnknown
			}
			rows, err := s.db.query("SELECT session FROM responses WHERE id=?", id)
			if err != nil {
				return err
			}
			if len(rows) != 1 || rows[0][0] != o.Session {
				return ErrUnknown
			}
		}
		lease = Lease{Session: x, Request: rand.Text()}
		_, err = s.db.query("UPDATE sessions SET state='inflight',request=?,last_seen=MAX(last_seen,?) WHERE id=?", lease.Request, stamp(now), o.Session)
		return err
	})
	if err != nil {
		return Lease{}, err
	}
	return lease, nil
}

// Finish accepts IDs only from a trusted response observer. Failure permanently
// blocks the original session. A DB error leaves intent inflight (fail closed).
func (s *Store) Finish(lease Lease, ids []string, success bool, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transaction(func() error {
		x, state, request, err := s.lookup(lease.Session.Origin.Session)
		if err != nil {
			return err
		}
		if state != "inflight" || request != lease.Request || x != lease.Session {
			return ErrConflict
		}
		next := "blocked"
		if success {
			for _, id := range ids {
				if !valid(id) {
					return ErrConflict
				}
				rows, err := s.db.query("SELECT session FROM responses WHERE id=?", id)
				if err != nil {
					return err
				}
				if len(rows) > 0 {
					if rows[0][0] != x.Origin.Session {
						return ErrConflict
					}
					continue
				}
				if _, err = s.db.query("INSERT INTO responses(id,session) VALUES(?,?)", id, x.Origin.Session); err != nil {
					return err
				}
			}
			next = "ready"
		}
		_, err = s.db.query("UPDATE sessions SET state=?,request='',last_seen=MAX(last_seen,?) WHERE id=?", next, stamp(now), x.Origin.Session)
		return err
	})
}

// GC never deletes active intent; response ownership is removed in the same tx.
func (s *Store) GC(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transaction(func() error {
		_, err := s.db.query("DELETE FROM sessions WHERE state!='inflight' AND last_seen < ?", stamp(now.Add(-Retention)))
		return err
	})
}
