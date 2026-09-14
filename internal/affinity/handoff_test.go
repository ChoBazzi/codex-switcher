//go:build darwin && cgo

package affinity

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistentHandoffAtomicBinding(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	db := openTest(t, dir)
	now := time.Now()
	register(t, db, "source", "a", now)
	l, err := db.Begin(origin("source"), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Finish(l, []string{"synthetic-response"}, true, now); err != nil {
		t.Fatal(err)
	}
	r, err := db.PrepareHandoff(origin("source"), "b", "checkpoint", strings.Repeat("a", 64), true, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Begin(origin("source"), nil, now); err == nil {
		t.Fatal("source changed after snapshot reservation")
	}
	if _, err := db.PrepareHandoff(origin("source"), "b", "other", strings.Repeat("a", 64), true, now); err == nil {
		t.Fatal("duplicate reservation accepted")
	}
	db.Close()
	db = openTest(t, dir)
	restored, err := db.Handoff(r.ID)
	if err != nil || restored != r {
		t.Fatal("reservation lost on restart")
	}
	if _, err := db.BindHandoff(r.ID, origin("no-input"), false, now); err == nil {
		t.Fatal("bound without user input")
	}
	wrong := origin("foreign")
	wrong.Worktree = "other"
	if _, err := db.BindHandoff(r.ID, wrong, true, now); err == nil {
		t.Fatal("foreign worktree accepted")
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := db.BindHandoff(r.ID, origin(fmt.Sprint("new-", i)), true, now); err == nil {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("reservation consumed more than once")
	}
	r, err = db.Handoff(r.ID)
	if err != nil || r.ConsumedBy == "" {
		t.Fatal("consumption not persisted")
	}
	resolved, found, err := db.FindHandoff(origin("source"), "checkpoint")
	if err != nil || !found || resolved != r {
		t.Fatal("consumed reservation lookup failed")
	}
	foreign := origin("source")
	foreign.Worktree = "foreign"
	if _, _, err := db.FindHandoff(foreign, "checkpoint"); err == nil {
		t.Fatal("foreign origin resolved")
	}
	newSession, err := db.Lookup(r.ConsumedBy)
	if err != nil || newSession.Account != "b" {
		t.Fatal("wrong target account")
	}
	old, err := db.Lookup("source")
	if err != nil || old.Account != "a" {
		t.Fatal("source account moved")
	}
	if _, err := db.Begin(newSession.Origin, []string{"synthetic-response"}, now); err == nil {
		t.Fatal("source continuation transferred")
	}
	if db.CancelHandoff(r.ID, origin("source")) == nil {
		t.Fatal("consumed reservation cancelled")
	}
	db.Close()
	db = openTest(t, dir)
	if _, err := db.BindHandoff(r.ID, origin("another"), true, now); err == nil {
		t.Fatal("consumed reservation replayed after restart")
	}
}

func TestLogoutInvalidatesOnlyRelatedRouting(t *testing.T) {
	db := openTest(t, filepath.Join(t.TempDir(), "db"))
	defer db.Close()
	now := time.Now()
	register(t, db, "source", "a", now)
	register(t, db, "other", "b", now)
	r, err := db.PrepareHandoff(origin("source"), "b", "cp", strings.Repeat("a", 64), true, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InvalidateSlot("a"); err != nil {
		t.Fatal(err)
	}
	if err := db.Ready(origin("source")); err == nil {
		t.Fatal("old source can resume")
	}
	if err := db.Ready(origin("other")); err != nil {
		t.Fatal("unrelated session blocked")
	}
	if _, err := db.Handoff(r.ID); err == nil {
		t.Fatal("pending handoff retained")
	}
}

func TestHandoffCancellationAndMigration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	db := openTest(t, dir)
	now := time.Now()
	register(t, db, "source", "a", now)
	if _, err := db.db.query("DROP TABLE handoffs"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.query("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = openTest(t, dir)
	if _, err := db.PrepareHandoff(origin("source"), "b", "cp", strings.Repeat("b", 64), false, now); err == nil {
		t.Fatal("unsafe boundary accepted")
	}
	if _, err := db.PrepareHandoff(origin("source"), "a", "cp", strings.Repeat("b", 64), true, now); err == nil {
		t.Fatal("same account accepted")
	}
	r, err := db.PrepareHandoff(origin("source"), "b", "cp", strings.Repeat("b", 64), true, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CancelHandoff(r.ID, origin("source")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindHandoff(r.ID, origin("new"), true, now); err == nil {
		t.Fatal("cancelled reservation revived")
	}
	if _, err := db.Begin(origin("source"), nil, now); err != nil {
		t.Fatal("cancelled source remained blocked")
	}
}
