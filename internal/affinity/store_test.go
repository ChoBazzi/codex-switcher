//go:build darwin && cgo

package affinity

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

func origin(id string) checkpoint.Origin {
	return checkpoint.Origin{Project: "synthetic-project", Worktree: "synthetic-tree", Branch: "dev", Session: id}
}
func openTest(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func register(t *testing.T, s *Store, id, slot string, now time.Time) {
	t.Helper()
	if _, err := s.Register(origin(id), slot, now); err != nil {
		t.Fatal(err)
	}
}

func TestRestartOwnershipAndCrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	now := time.Now()
	s := openTest(t, dir)
	register(t, s, "one", "a", now)
	register(t, s, "two", "b", now)
	l, err := s.Begin(origin("one"), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(l, []string{"synthetic-response"}, true, now); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openTest(t, dir)
	x, err := s.Lookup("one")
	if err != nil || x.Account != "a" {
		t.Fatal("affinity not restored")
	}
	if _, err = s.Begin(origin("two"), []string{"synthetic-response"}, now); !errors.Is(err, ErrUnknown) {
		t.Fatal("cross-account reference accepted")
	}
	if _, err = s.Begin(origin("one"), []string{"unknown"}, now); !errors.Is(err, ErrUnknown) {
		t.Fatal("unknown reference accepted")
	}
	l, err = s.Begin(origin("one"), []string{"synthetic-response"}, now)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openTest(t, dir)
	if err = s.Ready(origin("one")); !errors.Is(err, ErrBlocked) {
		t.Fatal("crashed intent was replayable")
	}
	if err = s.Finish(l, nil, true, now); !errors.Is(err, ErrConflict) {
		t.Fatal("old lease revived")
	}
	x, err = s.Lookup("one")
	if err != nil || x.Account != "a" {
		t.Fatal("blocked mapping lost")
	}
}
func TestFailureAndAtomicResponseOwnership(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "private"))
	now := time.Now()
	register(t, s, "one", "a", now)
	register(t, s, "two", "a", now)
	l, _ := s.Begin(origin("one"), nil, now)
	if err := s.Finish(l, []string{"owned"}, true, now); err != nil {
		t.Fatal(err)
	}
	l, _ = s.Begin(origin("two"), nil, now)
	if err := s.Finish(l, []string{"new", "owned"}, true, now); !errors.Is(err, ErrConflict) {
		t.Fatal("overwrote response owner")
	}
	if err := s.Ready(origin("two")); !errors.Is(err, ErrBlocked) {
		t.Fatal("partial transaction released intent")
	}
	if err := s.Finish(l, nil, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(origin("two"), nil, now); !errors.Is(err, ErrBlocked) {
		t.Fatal("failure retried")
	}
	if _, err := s.Begin(origin("one"), []string{"new"}, now); !errors.Is(err, ErrUnknown) {
		t.Fatal("rolled-back ID persisted")
	}
}
func TestGCAndConcurrentAdmission(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "private"))
	now := time.Now().Truncate(time.Second)
	register(t, s, "old", "a", now.Add(-Retention-time.Second))
	register(t, s, "edge", "a", now.Add(-Retention))
	register(t, s, "active", "b", now.Add(-Retention-time.Second))
	l, _ := s.Begin(origin("old"), nil, now.Add(-Retention-time.Second))
	s.Finish(l, []string{"old-response"}, true, now.Add(-Retention-time.Second))
	var wg sync.WaitGroup
	var wins atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Begin(origin("active"), nil, now.Add(-Retention-time.Second)); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("multiple admissions")
	}
	if err := s.GC(now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup("old"); !errors.Is(err, ErrUnknown) {
		t.Fatal("expired session retained")
	}
	for _, id := range []string{"edge", "active"} {
		if _, err := s.Lookup(id); err != nil {
			t.Fatal("GC removed protected session")
		}
	}
	if _, err := s.Begin(origin("edge"), []string{"old-response"}, now); !errors.Is(err, ErrUnknown) {
		t.Fatal("orphan owner survived GC")
	}
}
func TestPrivateFilesAndLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	s := openTest(t, dir)
	if _, err := Open(dir); err == nil {
		t.Fatal("second owner allowed")
	}
	info, err := os.Stat(filepath.Join(dir, "affinity.sqlite3"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe mode")
	}
	s.Close()
	if err := s.Ready(origin("none")); !errors.Is(err, ErrStorage) {
		t.Fatal("closed store accepted")
	}
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		bad := filepath.Join(t.TempDir(), "private")
		os.Mkdir(bad, 0700)
		target := filepath.Join(t.TempDir(), "target")
		os.WriteFile(target, []byte("synthetic"), 0600)
		os.Symlink(target, filepath.Join(bad, "affinity.sqlite3"+suffix))
		if db, err := Open(bad); err == nil {
			db.Close()
			t.Fatal("symlink accepted")
		}
	}
}

func TestCorruptionAndFutureSchemaFailClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	s := openTest(t, dir)
	if _, err := s.db.query("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if db, err := Open(dir); err == nil {
		db.Close()
		t.Fatal("future schema accepted")
	}
	dir = filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "affinity.sqlite3")
	if err := os.WriteFile(path, []byte("synthetic-corrupt-database"), 0600); err != nil {
		t.Fatal(err)
	}
	if db, err := Open(dir); err == nil {
		db.Close()
		t.Fatal("corrupt DB replaced")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "synthetic-corrupt-database" {
		t.Fatal("corrupt file changed")
	}
}
