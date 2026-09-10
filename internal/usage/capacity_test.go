package usage

import (
	"context"
	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"sync/atomic"
	"testing"
	"time"
)

func TestFiveSlotsOccupancyAndLocalRefresh(t *testing.T) {
	var calls atomic.Int32
	source := sourceFunc(func(slot string, _ time.Time) (accounts.Access, error) {
		switch slot {
		case "a", "e":
			return access(), nil
		case "b":
			return accounts.Access{}, accounts.ErrExpired
		case "c":
			return accounts.Access{}, accounts.ErrStore
		default:
			return accounts.Access{}, accounts.ErrNotRegistered
		}
	})
	fetch := fetchFunc(func(context.Context, accounts.Access) (Data, error) { calls.Add(1); return Data{}, nil })
	samples := NewMonitor(source, fetch).Refresh(context.Background(), accountslot.All())
	if len(samples) != 5 || calls.Load() != 2 {
		t.Fatal("unregistered slots fetched remotely")
	}
	for i, slot := range accountslot.All() {
		local := LocalSnapshot(source, slot, time.Now())
		if slot == "c" {
			if local.Registered != nil || samples[i].Registered != nil || local.State == "not_registered" {
				t.Fatal("storage failure freed slot")
			}
		} else {
			want := slot != "d"
			if local.Registered == nil || *local.Registered != want || samples[i].Registered == nil || *samples[i].Registered != want {
				t.Fatal("occupancy mismatch")
			}
		}
	}
	if calls.Load() != 2 {
		t.Fatal("local occupancy check fetched usage")
	}
}
