package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
)

type progressVault struct {
	*memoryVault
	committing, release chan struct{}
}

type progressIntentFailure struct{ *memoryVault }

func (v *progressIntentFailure) Write([]byte) error { return credentialstore.ErrUnavailable }

func (v *progressVault) Write(data []byte) error {
	if v.writes == 1 {
		close(v.committing)
		<-v.release
	}
	return v.memoryVault.Write(data)
}

func TestAuthenticationProgressThroughCommit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	commit, commitRelease := make(chan struct{}), make(chan struct{})
	var exchangeOnce, commitOnce sync.Once
	unblock := func() { exchangeOnce.Do(func() { close(release) }) }
	unblockCommit := func() { commitOnce.Do(func() { close(commitRelease) }) }
	defer unblock()
	defer unblockCommit()
	next := renewed(t, "alpha")
	m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
		close(entered)
		<-release
		return next, nil
	}))
	m.vault = &progressVault{v, commit, commitRelease}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.RequestAccessContext(ctx, "a", time.Now()); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("exchange did not start")
	}
	check := func(waiting int, canceled bool) {
		t.Helper()
		observed := make(chan []AuthenticationStatus, 1)
		go func() { observed <- m.AuthenticationStatus() }()
		select {
		case s := <-observed:
			if len(s) != 1 || s[0] != (AuthenticationStatus{Slot: "a", Waiting: waiting, Refreshing: true, Canceled: canceled}) {
				t.Fatalf("unexpected authentication progress: %+v", s)
			}
			encoded, _ := json.Marshal(s)
			if strings.Contains(string(encoded), "alpha") || strings.Contains(string(encoded), "synthetic") {
				t.Fatal("authentication progress contains credential data")
			}
		case <-time.After(time.Second):
			t.Fatal("status waited on credential lock")
		}
	}
	check(0, false)
	waitCtx, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	observed := &observedWaitContext{Context: waitCtx, waiting: make(chan struct{})}
	waitDone := make(chan error, 1)
	go func() { _, err := m.RequestAccessContext(observed, "a", time.Now()); waitDone <- err }()
	select {
	case <-observed.waiting:
	case <-time.After(time.Second):
		t.Fatal("waiter did not start")
	}
	check(1, false)
	cancelWait()
	if err := <-waitDone; !errors.Is(err, context.Canceled) {
		t.Fatal("waiter did not cancel")
	}
	check(0, false)
	cancel()
	check(0, true)
	unblock()
	select {
	case <-commit:
	case <-time.After(time.Second):
		t.Fatal("commit did not start")
	}
	// The vault write holds mu; progress must remain readable and active.
	check(0, true)
	unblockCommit()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("initiator cancellation lost")
	}
	if len(m.AuthenticationStatus()) != 0 {
		t.Fatal("completed authentication left stale activity")
	}
	a, err := m.RequestAccess("a", time.Now())
	if err != nil || a.Token != next.AccessToken || len(m.AuthenticationStatus()) != 0 {
		t.Fatal("committed credentials lost")
	}
}

func TestAuthenticationProgressClearsOnFailure(t *testing.T) {
	for _, stage := range []string{"store", "intent", "exchange", "commit", "canceled", "invalid"} {
		t.Run(stage, func(t *testing.T) {
			var vault *memoryVault
			next := renewed(t, "alpha")
			m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
				if stage == "commit" {
					vault.fail = true
					return next, nil
				}
				return Credentials{}, ErrRefresh
			}))
			vault = v
			if stage == "store" {
				v.fail = true
			}
			if stage == "intent" {
				m.vault = &progressIntentFailure{v}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "canceled" {
				cancel()
			}
			slot := "a"
			if stage == "invalid" {
				slot = "synthetic-secret"
			}
			if _, err := m.RequestAccessContext(ctx, slot, time.Now()); err == nil {
				t.Fatal("expected failure")
			}
			if s := m.AuthenticationStatus(); s == nil || len(s) != 0 {
				t.Fatal("failure retained authentication progress")
			}
		})
	}
}

func TestAuthenticationProgressLateCleanup(t *testing.T) {
	m := New(nil, "")
	startOld, finishOld := m.authentication.begin("a", context.Background())
	startOld()
	startNext, finishNext := m.authentication.begin("a", context.Background())
	startNext()
	finishOld()
	if s := m.AuthenticationStatus(); len(s) != 1 || !s[0].Refreshing {
		t.Fatal("old cleanup erased newer progress")
	}
	finishNext()
	if len(m.AuthenticationStatus()) != 0 {
		t.Fatal("new progress not cleared")
	}
}
