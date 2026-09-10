package accounts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
)

func TestFiveAccountsLogoutAndReuse(t *testing.T) {
	v := &memoryVault{}
	m := New(v, t.TempDir())
	ctx := context.Background()
	for _, slot := range accountslot.All() {
		if err := m.Login(ctx, slot, writer("synthetic-"+slot), nil); err != nil {
			t.Fatal(err)
		}
	}
	statuses, err := m.Status()
	if err != nil || len(statuses) != 5 {
		t.Fatal("capacity status")
	}
	for _, status := range statuses {
		if !status.Registered {
			t.Fatal("registered slot missing")
		}
	}
	never := runnerFunc(func(context.Context, string, func()) error { t.Fatal("unexpected OAuth"); return nil })
	if !errors.Is(m.Login(ctx, "f", never, nil), ErrSlot) {
		t.Fatal("sixth slot accepted")
	}
	if !errors.Is(m.Login(ctx, "e", never, nil), ErrOccupied) {
		t.Fatal("occupied slot replaced")
	}
	// A failed vault write must not create a vacancy.
	v.fail = true
	if m.Logout("c") == nil {
		t.Fatal("logout failure hidden")
	}
	v.fail = false
	if _, err := m.Access("c", time.Now()); err != nil {
		t.Fatal("failed logout removed account")
	}
	if err := m.Logout("c"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Access("c", time.Now()); !errors.Is(err, ErrNotRegistered) {
		t.Fatal("slot not released")
	}
	if !errors.Is(m.Login(ctx, "c", writer("synthetic-e"), nil), ErrDuplicate) {
		t.Fatal("duplicate fifth account accepted")
	}
	if err := m.Login(ctx, "c", writer("synthetic-replacement"), nil); err != nil {
		t.Fatal(err)
	}
	// Reload the same registry; old A/B and other slots remain untouched.
	m = New(v, t.TempDir())
	for _, slot := range accountslot.All() {
		access, err := m.Access(slot, time.Now())
		want := "synthetic-" + slot
		if slot == "c" {
			want = "synthetic-replacement"
		}
		if err != nil || access.AccountID != want {
			t.Fatal("account lost or renumbered")
		}
	}
}
