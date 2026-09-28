package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Signal when a cancellable lock reaches its wait, without timing sleeps.
type observedWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestRefreshAllowsOtherValidAccount(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var exchanges atomic.Int32
			next := renewed(t, "alpha")
			m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
				exchanges.Add(1)
				close(entered)
				<-release
				if failure {
					return Credentials{}, ErrRefresh
				}
				return next, nil
			}))
			r, err := m.read()
			if err != nil {
				t.Fatal(err)
			}
			beta := record{Slot: "b", Credentials: renewed(t, "beta"), Registration: "synthetic-beta-registration"}
			r.Accounts = append(r.Accounts, beta)
			v.data, _ = json.Marshal(r)
			done := make(chan error, 1)
			go func() { _, err := m.RequestAccess("a", time.Now()); done <- err }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("refresh did not start")
			}
			// B must be usable before A's OAuth completes, with B's own owner.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			access, err := m.RequestAccessContext(ctx, "b", time.Now())
			if err != nil || access.Token != beta.Credentials.AccessToken || access.HistoryCredential() != beta.access().HistoryCredential() {
				t.Fatal("valid account waited for another account's refresh or lost ownership")
			}
			if v.writes != 1 || exchanges.Load() != 1 {
				t.Fatal("read-only access wrote credentials or started another exchange")
			}
			// Brief contention with a local reader must not send B into A's
			// long-lived mutation queue once the local reader releases its lock.
			m.mu.Lock()
			localCtx, stopLocal := context.WithTimeout(context.Background(), time.Second)
			defer stopLocal()
			observed := &observedWaitContext{Context: localCtx, waiting: make(chan struct{})}
			localDone := make(chan error, 1)
			go func() {
				a, err := m.RequestAccessContext(observed, "b", time.Now())
				if err == nil && a.Token != beta.Credentials.AccessToken {
					t.Error("local contention changed account authentication")
				}
				localDone <- err
			}()
			select {
			case <-observed.waiting:
				m.mu.Unlock()
			case <-localCtx.Done():
				m.mu.Unlock()
				t.Fatal("local wait was not cancellable")
			}
			if err := <-localDone; err != nil {
				t.Fatal("local contention queued valid account behind another refresh")
			}
			// A still joins its own exchange, and may cancel without exposing
			// the pre-refresh token or consuming the refresh token again.
			waitCtx, stopWait := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer stopWait()
			if a, err := m.RequestAccessContext(waitCtx, "a", time.Now()); !errors.Is(err, context.DeadlineExceeded) || a.Token != "" {
				t.Fatal("same-slot request did not wait for the committed outcome")
			}
			unblock()
			if err := <-done; (err != nil) != failure {
				t.Fatal("unexpected refresh outcome")
			}
			after, err := m.RequestAccess("b", time.Now())
			if err != nil || after.HistoryCredential() != access.HistoryCredential() || after.Token != access.Token || exchanges.Load() != 1 {
				t.Fatal("other account changed after refresh completed")
			}
		})
	}
}

func TestRequestAccessFastPathGuards(t *testing.T) {
	for _, state := range []string{"valid", "threshold", "blocked", "missing", "store", "canceled"} {
		t.Run(state, func(t *testing.T) {
			m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
				t.Error("read-only path started exchange")
				return Credentials{}, ErrRefresh
			}))
			now := time.Now().Truncate(time.Second)
			c, _ := ParseAuth(syntheticAuth("alpha", now.Add(time.Hour)))
			if state == "threshold" {
				c, _ = ParseAuth(syntheticAuth("alpha", now.Add(2*time.Minute)))
			}
			v.data, _ = json.Marshal(registry{Version: 1, Accounts: []record{{Slot: "a", Credentials: c, RefreshBlocked: state == "blocked"}}})
			v.fail = state == "store"
			slot := "a"
			if state == "missing" {
				slot = "b"
			}
			m.operationMu.Lock()
			defer m.operationMu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if state == "canceled" {
				cancel()
			}
			a, err := m.RequestAccessContext(ctx, slot, now)
			want := map[string]error{"threshold": context.DeadlineExceeded, "blocked": ErrRefresh, "missing": ErrNotRegistered, "store": ErrStore, "canceled": context.Canceled}[state]
			if !errors.Is(err, want) || v.writes != 0 {
				t.Fatal("fast path ignored guard or wrote credentials")
			}
			if state == "valid" && a.Token != c.AccessToken || state != "valid" && a.Token != "" {
				t.Fatal("unexpected credential returned")
			}
		})
	}
}

func TestRequestAccessFastPathDuringReauthentication(t *testing.T) {
	m, _ := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
		t.Error("reauthenticated account unexpectedly refreshed")
		return Credentials{}, ErrRefresh
	}))
	before, _ := m.Access("a", time.Now())
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- m.Reauthenticate(context.Background(), "a", runnerFunc(func(ctx context.Context, dir string, waiting func()) error {
			close(entered)
			<-release
			return writer("alpha").Run(ctx, dir, waiting)
		}), nil)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reauthentication did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	request := make(chan error, 1)
	go func() {
		a, err := m.RequestAccessContext(ctx, "a", time.Now())
		if a.Token != "" {
			t.Error("credentials escaped during reauthentication")
		}
		request <- err
	}()
	select {
	case err := <-request:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("reauthentication wait was bypassed")
		}
	case <-time.After(time.Second):
		t.Fatal("local read mutex made authentication wait non-cancellable")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, err := m.RequestAccess("a", time.Now())
	if err != nil || after.Registration == before.Registration || after.HistoryCredential() == before.HistoryCredential() {
		t.Fatal("request reused previous registration after reauthentication")
	}
	if err := m.Logout("a"); err != nil {
		t.Fatal(err)
	}
	if a, err := m.RequestAccess("a", time.Now()); !errors.Is(err, ErrNotRegistered) || a.Token != "" {
		t.Fatal("request reused credentials after logout")
	}
}
