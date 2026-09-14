//go:build darwin && cgo

package clisession

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

func TestExplicitHandoffStartEvent(t *testing.T) {
	db, err := affinity.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err := routing.NewPersistent(source{}, 90, db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	data, _ := usage.Parse([]byte(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`))
	for _, slot := range []string{"a", "b"} {
		r.Update([]usage.Snapshot{{Slot: slot, State: "ok", LastAttempt: now, LastSuccess: &now, Usage: &data}})
	}
	o := checkpoint.Origin{Project: "p", Worktree: "w", Branch: "b", Session: id}
	if _, err := r.Register(o, true, now); err != nil {
		t.Fatal(err)
	}
	meta := checkpoint.Metadata{ID: "synthetic", Origin: o}
	snap, err := checkpoint.CheckBytes(checkpoint.Format(meta, "g", "c", "d", "t"), meta)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := r.PrepareHandoff(o, "b", snap, true, now)
	if err != nil {
		t.Fatal(err)
	}
	o.Session = ""
	if _, err := NewHandoff(r, o, secret, reservation.ID, snap, false); err == nil {
		t.Fatal("user input not required")
	}
	bad, err := NewHandoff(r, o, secret, "missing", snap, true)
	if err != nil {
		t.Fatal(err)
	}
	if bad.Started(other) == nil {
		t.Fatal("invalid reservation fell back")
	}
	bad.Close()
	if _, ok := r.Session(other); ok {
		t.Fatal("bad reservation created session")
	}
	b, err := NewHandoff(r, o, secret, reservation.ID, snap, true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, ok := r.Session(other); ok {
		t.Fatal("constructor created target session")
	}
	if err := b.Started(other); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/responses", nil)
	req.Header.Set("Thread-Id", other)
	req.Header.Set("Session-Id", other)
	req.Header.Set("X-Switcher-Run", secret)
	identity, err := b.Resolve(req)
	if err != nil || identity.Slot != "b" {
		t.Fatal("new thread not bound to target")
	}
	req.Header.Set("Thread-Id", id)
	req.Header.Set("Session-Id", id)
	if _, err := b.Resolve(req); err == nil {
		t.Fatal("source thread accepted by target process")
	}
}
