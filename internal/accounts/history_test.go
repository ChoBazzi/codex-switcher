package accounts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryCredentialLoginIncarnation(t *testing.T) {
	v := &memoryVault{}
	parent := t.TempDir()
	m := New(v, parent)
	// Exactly the same synthetic token is deliberately reused across logins.
	expires := time.Now().Add(time.Hour)
	runner := runnerFunc(func(ctx context.Context, dir string, waiting func()) error {
		return os.WriteFile(filepath.Join(dir, "auth.json"), syntheticUserAuth("synthetic-workspace", "synthetic-user", expires), 0600)
	})
	if err := m.Login(context.Background(), "a", runner, nil); err != nil {
		t.Fatal(err)
	}
	first, _ := m.Access("a", time.Now())
	restored, _ := New(v, parent).Access("a", time.Now())
	if first.HistoryCredential() == ([32]byte{}) || first.HistoryCredential() != restored.HistoryCredential() {
		t.Fatal("ownership unstable across manager restart")
	}
	if err := m.Logout("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Access("a", time.Now()); err == nil {
		t.Fatal("logged out credential available")
	}
	if err := m.Login(context.Background(), "a", runner, nil); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Access("a", time.Now())
	if first.Token != next.Token || first.HistoryCredential() == next.HistoryCredential() {
		t.Fatal("slot reuse retained history ownership")
	}
	if err := m.Reauthenticate(context.Background(), "a", runner, nil); err != nil {
		t.Fatal(err)
	}
	reauth, _ := m.RequestAccess("a", time.Now())
	if next.HistoryCredential() == reauth.HistoryCredential() {
		t.Fatal("reauthentication retained prior incarnation")
	}
}

func TestHistoryCredentialIdentityAndMissingClaims(t *testing.T) {
	base := Access{Token: "synthetic-token", AccountID: "synthetic-workspace", UserID: "synthetic-user", Registration: "synthetic-registration"}
	for _, field := range []string{"token", "account", "user", "registration"} {
		next := base
		switch field {
		case "token":
			next.Token += "-other"
		case "account":
			next.AccountID += "-other"
		case "user":
			next.UserID += "-other"
		case "registration":
			next.Registration += "-other"
		}
		if base.HistoryCredential() == next.HistoryCredential() {
			t.Fatal("identity component ignored: " + field)
		}
	}
	base.UserID = ""
	if base.HistoryCredential() != ([32]byte{}) {
		t.Fatal("missing identity guessed")
	}
}

func TestHistoryCredentialRefreshPreservesRegistration(t *testing.T) {
	m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) { return renewed(t, "alpha"), nil }))
	var stored registry
	if json.Unmarshal(v.data, &stored) != nil {
		t.Fatal("synthetic vault unreadable")
	}
	stored.Accounts[0].Registration = "synthetic-registration"
	v.data, _ = json.Marshal(stored)
	before, err := m.Access("a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	after, err := m.RequestAccess("a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if before.Registration != after.Registration || before.UserID != after.UserID || before.AccountID != after.AccountID {
		t.Fatal("refresh changed identity or registration")
	}
	if before.Token == after.Token || before.HistoryCredential() == after.HistoryCredential() {
		t.Fatal("refresh did not change credential binding")
	}
	restored, err := New(v, m.tempParent).Access("a", time.Now())
	if err != nil || restored.HistoryCredential() != after.HistoryCredential() {
		t.Fatal("refreshed binding not durable")
	}
}
