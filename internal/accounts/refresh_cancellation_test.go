package accounts

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestAccessCanceledBeforeMutation(t *testing.T) {
	m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
		t.Error("canceled request started OAuth")
		return Credentials{}, ErrRefresh
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	access, err := m.RequestAccessContext(ctx, "a", time.Now())
	if !errors.Is(err, context.Canceled) || access.Token != "" || v.writes != 0 {
		t.Fatal("pre-canceled request touched credentials")
	}
	// Test cancellation while another mutation holds the gate. A goroutine that
	// simply waits on sync.Mutex would not return until the lock is released.
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.RequestAccessContext(ctx, "a", time.Now()); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || v.writes != 0 {
			t.Fatal("canceled waiter changed credentials")
		}
	case <-time.After(time.Second):
		t.Fatal("request cannot leave mutation queue")
	}
}

func TestRequestAccessCancellationPreservesRefreshOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "network", "commit"} {
		t.Run(outcome, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var calls atomic.Int32
			next := renewed(t, "alpha")
			var vault *memoryVault
			m, v := refreshFixture(t, refreshFunc(func(ctx context.Context, _ Credentials) (Credentials, error) {
				calls.Add(1)
				close(entered)
				<-release
				if ctx.Err() != nil {
					t.Error("CLI cancellation canceled the OAuth exchange")
				}
				if outcome == "network" {
					return Credentials{}, ErrRefresh
				}
				if outcome == "commit" {
					vault.fail = true
				}
				return next, nil
			}))
			vault = v
			owner, _ := m.HistoryCredential("a")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				a, err := m.RequestAccessContext(ctx, "a", time.Now())
				if a.Token != "" {
					t.Error("canceled caller received usable authentication")
				}
				done <- err
			}()
			<-entered
			// Another waiter exits while the original exchange is still held.
			waitCtx, stopWait := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer stopWait()
			waited := make(chan error, 1)
			go func() { _, err := m.RequestAccessContext(waitCtx, "a", time.Now()); waited <- err }()
			select {
			case err := <-waited:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("queued cancellation lost")
				}
			case <-time.After(time.Second):
				t.Fatal("waiter stayed attached to refresh")
			}
			cancel()
			select {
			case <-done:
				t.Fatal("in-flight credentials abandoned before commit")
			default:
			}
			unblock()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal("initiator cancellation lost")
			}
			v.fail = false
			restored := NewRefreshing(v, m.tempParent, m.refresher)
			a, err := restored.RequestAccess("a", time.Now())
			if outcome == "success" {
				if err != nil || a.Token != next.AccessToken || a.HistoryCredential() != owner {
					t.Fatal("rotated token or ownership not committed")
				}
			} else if !errors.Is(err, ErrRefresh) {
				t.Fatal("ambiguous exchange not durably blocked")
			}
			if calls.Load() != 1 {
				t.Fatal("canceled or failed exchange replayed")
			}
		})
	}
}
