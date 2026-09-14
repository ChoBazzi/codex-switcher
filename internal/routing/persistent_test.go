//go:build darwin && cgo

package routing

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

func TestPersistentRoutingRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	db, err := affinity.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r, _ := NewPersistent(&source{}, 90, db)
	r.Update([]usage.Snapshot{sample("a", 0, now), sample("b", 20, now)})
	if _, err = r.Register(origin("one"), true, now); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = affinity.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, _ = NewPersistent(&source{}, 90, db)
	if _, err = r.Resolve(origin("one"), now); err == nil {
		t.Fatal("quota inferred after restart")
	}
	r.Update([]usage.Snapshot{sample("a", 95, now), sample("b", 0, now)})
	x, err := r.Resolve(origin("one"), now)
	if err != nil || x.AccountID != "synthetic-account-a" {
		t.Fatal("restored session rebound")
	}
	if _, err = r.Register(origin("one"), true, now); err == nil {
		t.Fatal("restored session overwritten")
	}
	lease, err := db.Begin(origin("one"), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(lease, nil, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Resolve(origin("one"), now); err == nil {
		t.Fatal("blocked session resolved")
	}
}
