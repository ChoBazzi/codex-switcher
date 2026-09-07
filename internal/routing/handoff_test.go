//go:build darwin && cgo

package routing

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

func TestHandoffRechecksTargetAndSnapshot(t *testing.T) {
	db, err := affinity.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	r, err := NewPersistent(&source{}, 90, db)
	if err != nil {
		t.Fatal(err)
	}
	r.Update([]usage.Snapshot{sample("a", 0, now), sample("b", 20, now)})
	if _, err := r.Register(origin("source"), true, now); err != nil {
		t.Fatal(err)
	}
	meta := checkpoint.Metadata{ID: "synthetic-checkpoint", Origin: origin("source")}
	snap, err := checkpoint.CheckBytes(checkpoint.Format(meta, "g", "c", "d", "t"), meta)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := r.PrepareHandoff(origin("source"), "b", snap, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Session("target"); ok {
		t.Fatal("prepare created a target session")
	}
	if _, err := r.BindHandoff(reserved.ID, origin("target"), snap, false, now); err == nil {
		t.Fatal("model input inferred")
	}
	changed := snap
	changed.Markdown += "changed"
	if _, err := r.BindHandoff(reserved.ID, origin("target"), changed, true, now); err == nil {
		t.Fatal("snapshot mutation accepted")
	}
	later := now.Add(time.Second)
	r.Update([]usage.Snapshot{sample("b", 100, later)})
	if _, err := r.BindHandoff(reserved.ID, origin("target"), snap, true, later); err == nil {
		t.Fatal("unavailable target accepted")
	}
	if _, ok := r.Session("target"); ok {
		t.Fatal("failed handoff fell back to ordinary session")
	}
	later = later.Add(time.Second)
	r.Update([]usage.Snapshot{sample("b", 10, later)})
	s, err := r.BindHandoff(reserved.ID, origin("target"), snap, true, later)
	if err != nil || s.Account != "b" || s.Wiki != "" {
		t.Fatalf("invalid binding: %v", err)
	}
	old, _ := r.Session("source")
	if old.Account != "a" {
		t.Fatal("source moved")
	}
}
