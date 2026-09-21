package accounts

import (
	"context"
	"encoding/json"
	"errors"
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

func TestHistoryCredentialVerifiedRefresh(t *testing.T) {
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
	if before.Token == after.Token || before.HistoryCredential() != after.HistoryCredential() {
		t.Fatal("verified refresh lost history ownership")
	}
	restored, err := New(v, m.tempParent).Access("a", time.Now())
	if err != nil || restored.HistoryCredential() != after.HistoryCredential() {
		t.Fatal("refreshed binding not durable")
	}
	// A second verified rotation preserves the original owner across restarts.
	m = NewRefreshing(v, m.tempParent, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
		return ParseAuth(syntheticAuth("alpha", time.Now().Add(4*time.Hour)))
	}))
	second, err := m.RequestAccess("a", time.Now().Add(2*time.Hour))
	if err != nil || second.Token == after.Token || second.HistoryCredential() != before.HistoryCredential() {
		t.Fatal("second verified refresh lost original ownership")
	}
	// A copied Access cannot reuse the binding with unverified new credentials.
	for _, field := range []string{"token", "account", "user", "registration", "missing-user"} {
		changed := second
		switch field {
		case "token":
			changed.Token += "-other"
		case "account":
			changed.AccountID += "-other"
		case "user":
			changed.UserID += "-other"
		case "registration":
			changed.Registration += "-other"
		case "missing-user":
			changed.UserID = ""
		}
		if changed.HistoryCredential() == before.HistoryCredential() {
			t.Fatal("refresh binding reused after unverified change: " + field)
		}
	}
	if err := m.Reauthenticate(context.Background(), "a", writer("alpha"), nil); err != nil {
		t.Fatal(err)
	}
	reauth, _ := m.Access("a", time.Now())
	if reauth.history != nil || reauth.HistoryCredential() == before.HistoryCredential() {
		t.Fatal("reauthentication retained refresh lineage")
	}
}

func TestHistoryCredentialExpiredLocalRead(t *testing.T) {
	var calls int
	m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
		calls++
		return renewed(t, "alpha"), nil
	}))
	var stored registry
	_ = json.Unmarshal(v.data, &stored)
	c, err := ParseAuth(syntheticAuth("alpha", time.Now().Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	stored.Accounts[0].Credentials = c
	v.data, _ = json.Marshal(stored)
	if _, err := m.Access("a", time.Now()); !errors.Is(err, ErrExpired) {
		t.Fatal("expired authentication available")
	}
	owner, err := m.HistoryCredential("a")
	if err != nil || owner == ([32]byte{}) || calls != 0 || v.writes != 0 {
		t.Fatal("local ownership read unavailable or performed refresh/migration")
	}
	access, err := m.RequestAccess("a", time.Now())
	if err != nil || calls != 1 || access.HistoryCredential() != owner {
		t.Fatal("expired credential lost ownership during verified refresh")
	}
	for _, slot := range []string{"invalid", "b"} {
		if owner, err := m.HistoryCredential(slot); err == nil || owner != ([32]byte{}) {
			t.Fatal("unknown slot has an owner")
		}
	}
}

func TestHistoryCredentialStaleVaultBinding(t *testing.T) {
	m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) { return renewed(t, "alpha"), nil }))
	first, err := m.RequestAccess("a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var stored registry
	_ = json.Unmarshal(v.data, &stored)
	// Replace the token outside the verified exchange, retaining old metadata.
	stored.Accounts[0].Credentials, err = ParseAuth(syntheticAuth("alpha", time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	stored.Accounts[0].Credentials.AccessToken += "-unverified"
	v.data, _ = json.Marshal(stored)
	changed, err := New(v, m.tempParent).HistoryCredential("a")
	if err != nil || changed == first.HistoryCredential() || changed == ([32]byte{}) {
		t.Fatal("stale vault binding authorized an unverified replacement")
	}
	after, err := m.RequestAccess("a", time.Now())
	if err != nil || after.HistoryCredential() != changed || after.HistoryCredential() == first.HistoryCredential() {
		t.Fatal("subsequent refresh resurrected stale ownership")
	}
}
