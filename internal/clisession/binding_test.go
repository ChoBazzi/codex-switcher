package clisession

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

const id = "11111111-1111-4111-8111-111111111111"
const other = "22222222-2222-4222-8222-222222222222"
const secret = "synthetic-run-secret"

type source struct{}

func (source) Access(slot string, now time.Time) (accounts.Access, error) {
	return accounts.Access{Token: "synthetic-token", AccountID: "synthetic-" + slot, ExpiresAt: now.Add(time.Hour)}, nil
}
func setup(t *testing.T) (*routing.Router, checkpoint.Origin) {
	t.Helper()
	r, err := routing.New(source{}, 90)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	data, err := usage.Parse([]byte(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Update([]usage.Snapshot{{Slot: "a", State: "ok", LastAttempt: now, LastSuccess: &now, Usage: &data}})
	return r, checkpoint.Origin{Project: "synthetic", Worktree: "synthetic", Branch: "dev"}
}
func TestBindingRequiresEventAndAuthentication(t *testing.T) {
	r, o := setup(t)
	b, err := New(r, o, secret, true, "")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	req := httptest.NewRequest("POST", "/responses", nil)
	req.Header.Set("Thread-Id", id)
	req.Header.Set("Session-Id", id)
	if _, err = b.Resolve(req); err == nil {
		t.Fatal("unauthenticated request accepted")
	}
	req.Header.Set("X-Switcher-Run", secret)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = b.Resolve(req.WithContext(ctx)); err == nil {
		t.Fatal("unstarted request accepted")
	}
	if _, ok := r.Session(id); ok {
		t.Fatal("HTTP implicitly registered a session")
	}
	if err = b.Started(id); err != nil {
		t.Fatal(err)
	}
	got, err := b.Resolve(req)
	if err != nil || got.Slot != "a" {
		t.Fatal("verified request rejected")
	}
	if err = b.Started(other); err == nil {
		t.Fatal("second start accepted")
	}
	req.Header.Set("Thread-Id", other)
	req.Header.Set("Session-Id", other)
	if _, err = b.Resolve(req); err == nil {
		t.Fatal("another thread accepted")
	}
	b.Close()
	if _, err = b.Resolve(req); err == nil {
		t.Fatal("closed binding accepted")
	}
}
func TestResumeNeverCreatesOrRebinds(t *testing.T) {
	r, o := setup(t)
	b, _ := New(r, o, secret, true, id)
	if err := b.Started(id); err == nil {
		t.Fatal("unknown resume accepted")
	}
	if _, ok := r.Session(id); ok {
		t.Fatal("unknown resume registered")
	}
	if err := b.Started(other); err == nil {
		t.Fatal("failed binding retried")
	}
	fresh, _ := New(r, o, secret, true, "")
	if err := fresh.Started(id); err != nil {
		t.Fatal(err)
	}
	fresh.Close()
	resume, _ := New(r, o, secret, true, id)
	defer resume.Close()
	if err := resume.Started(id); err != nil {
		t.Fatal("known resume rejected")
	}
	wrong := o
	wrong.Worktree = "different"
	x, _ := New(r, wrong, secret, true, id)
	if err := x.Started(id); err == nil {
		t.Fatal("wrong worktree resumed")
	}
	if _, err := New(r, o, secret, false, ""); err == nil {
		t.Fatal("missing user input accepted")
	}
}
