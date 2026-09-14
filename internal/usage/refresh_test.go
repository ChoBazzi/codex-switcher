package usage

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
)

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
