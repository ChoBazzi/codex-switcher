package usage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
)

type canceledRefreshSource struct{ renewableSource }

func (s *canceledRefreshSource) RequestAccessContext(ctx context.Context, _ string, _ time.Time) (accounts.Access, error) {
	s.remote.Add(1)
	<-ctx.Done()
	return accounts.Access{}, ctx.Err()
}

func TestRequestAccessContextPropagation(t *testing.T) {
	s := &canceledRefreshSource{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := RequestAccessContext(ctx, s, "a", time.Now()); !errors.Is(err, context.DeadlineExceeded) || s.remote.Load() != 1 || s.local.Load() != 0 {
		t.Fatal("context-aware source not used")
	}
	if _, err := RequestAccessContext(ctx, s, "a", time.Now()); !errors.Is(err, context.DeadlineExceeded) || s.remote.Load() != 1 {
		t.Fatal("pre-canceled request queried source")
	}
}

type renewableSource struct {
	local, remote atomic.Int32
	fail          bool
}

func (s *renewableSource) Access(string, time.Time) (accounts.Access, error) {
	s.local.Add(1)
	return accounts.Access{}, accounts.ErrExpired
}
func (s *renewableSource) RequestAccess(string, time.Time) (accounts.Access, error) {
	s.remote.Add(1)
	if s.fail {
		return accounts.Access{}, accounts.ErrRefresh
	}
	return accounts.Access{Token: "synthetic-renewed", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func TestMonitorRenewalOnlyForRemoteRead(t *testing.T) {
	source := &renewableSource{}
	LocalSnapshot(source, "a", time.Now())
	if source.remote.Load() != 0 || source.local.Load() != 1 {
		t.Fatal("local inspection renewed credentials")
	}
	var calls int
	monitor := NewMonitor(source, fetchFunc(func(_ context.Context, a accounts.Access) (Data, error) {
		calls++
		if a.Token != "synthetic-renewed" {
			t.Error("old access used")
		}
		return Data{}, nil
	}))
	monitor.Refresh(context.Background(), []string{"a"})
	if calls != 1 || source.remote.Load() != 1 {
		t.Fatal("usage read did not use renewed credentials")
	}
	source.fail = true
	result := monitor.Refresh(context.Background(), []string{"a"})
	if calls != 1 || result[0].State != "auth_expired" || result[0].Registered == nil || !*result[0].Registered {
		t.Fatal("renewal failure dispatched usage or lost occupancy")
	}
}
