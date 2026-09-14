package usage

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
)

const Interval = time.Minute
const StaleAfter = 2 * time.Minute

type AccessSource interface {
	Access(string, time.Time) (accounts.Access, error)
}
type Fetcher interface {
	Fetch(context.Context, accounts.Access) (Data, error)
}
type Snapshot struct {
	Slot        string     `json:"slot"`
	Registered  *bool      `json:"registered,omitempty"`
	State       string     `json:"state"`
	ErrorCode   string     `json:"error_code,omitempty"`
	HTTPStatus  int        `json:"http_status,omitempty"`
	LastAttempt time.Time  `json:"last_attempt"`
	LastSuccess *time.Time `json:"last_success"`
	Stale       bool       `json:"stale"`
	Usage       *Data      `json:"usage"`
}

// LocalSnapshot confirms occupancy without a remote usage request. Unknown
// storage errors must never make a slot available for a new login.
func LocalSnapshot(source AccessSource, slot string, now time.Time) Snapshot {
	s := Snapshot{Slot: slot, State: "auth_error", Stale: true}
	_, err := source.Access(slot, now)
	registered := true
	switch {
	case err == nil:
		s.State, s.Registered = "stored_unverified", &registered
	case errors.Is(err, accounts.ErrExpired):
		s.State, s.Registered = "auth_expired", &registered
	case errors.Is(err, accounts.ErrNotRegistered):
		registered = false
		s.State, s.Registered = "not_registered", &registered
	}
	return s
}

// At lets consumers age a sample even when no new polling result arrives.
func (s Snapshot) At(now time.Time) Snapshot {
	s.Stale = s.Stale || s.LastSuccess == nil || now.Sub(*s.LastSuccess) >= StaleAfter
	return s
}

type Monitor struct {
	source   AccessSource
	fetcher  Fetcher
	mu       sync.Mutex
	previous map[string]Snapshot
}

func NewMonitor(source AccessSource, fetcher Fetcher) *Monitor {
	return &Monitor{source: source, fetcher: fetcher, previous: map[string]Snapshot{}}
}

// Refresh never retries. Slots are independent; failure retains old data as stale.
func (m *Monitor) Refresh(ctx context.Context, slots []string) []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Snapshot, len(slots))
	var wg sync.WaitGroup
	for i, slot := range slots {
		old := m.previous[slot]
		wg.Add(1)
		go func(i int, slot string, s Snapshot) {
			defer wg.Done()
			now := time.Now().UTC()
			s.Slot, s.LastAttempt, s.ErrorCode, s.HTTPStatus = slot, now, "", 0
			s.Stale = true
			access, err := RequestAccess(m.source, slot, now)
			if err != nil {
				s.State, s.ErrorCode = "auth_error", "usage_credentials_unavailable"
				if errors.Is(err, accounts.ErrNotRegistered) {
					s.State, s.ErrorCode, s.Usage, s.LastSuccess = "not_registered", "", nil, nil
					registered := false
					s.Registered = &registered
				}
				if errors.Is(err, accounts.ErrExpired) {
					s.State, s.ErrorCode = "auth_expired", "usage_auth_expired"
					registered := true
					s.Registered = &registered
				}
				result[i] = s
				return
			}
			registered := true
			s.Registered = &registered
			data, err := m.fetcher.Fetch(ctx, access)
			if err != nil {
				s.State, s.ErrorCode = "fetch_error", "usage_fetch_failed"
				var e *Error
				if errors.As(err, &e) {
					s.ErrorCode, s.HTTPStatus = e.Code, e.HTTPStatus
					if e.Code == "usage_auth_required" || e.Code == "usage_auth_expired" {
						s.State = "auth_error"
					}
					if e.Code == "usage_rate_limited" {
						s.State = "rate_limited"
					}
				}
				result[i] = s
				return
			}
			success := time.Now().UTC()
			s.LastSuccess, s.Usage, s.Stale, s.State = &success, &data, false, "ok"
			if data.RemainingPercent == nil {
				s.State = "unknown"
			}
			if data.LimitReached != nil && *data.LimitReached {
				s.State = "limit_reached"
			}
			s.HTTPStatus = 200
			result[i] = s
		}(i, slot, old)
	}
	wg.Wait()
	for _, s := range result {
		m.previous[s.Slot] = s
	}
	return result
}

// Run publishes an immediate sample and then fixed 60-second samples. Missed
// ticks are dropped, never replayed as catch-up retries after sleep/slow I/O.
func (m *Monitor) Run(ctx context.Context, slots []string, publish func([]Snapshot) error) error {
	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	return m.run(ctx, slots, ticker.C, publish)
}
func (m *Monitor) run(ctx context.Context, slots []string, ticks <-chan time.Time, publish func([]Snapshot) error) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := publish(m.Refresh(ctx, slots)); err != nil {
			return err
		}
		select {
		case <-ticks:
		default:
		}
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ticks:
			if !ok {
				return nil
			}
		}
	}
}

// RequestAccess opts into helper-owned renewal without changing local inspection.
func RequestAccess(source AccessSource, slot string, now time.Time) (accounts.Access, error) {
	if renewable, ok := source.(interface {
		RequestAccess(string, time.Time) (accounts.Access, error)
	}); ok {
		return renewable.RequestAccess(slot, now)
	}
	return source.Access(slot, now)
}
