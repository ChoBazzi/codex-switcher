package main

import (
	"io"
	"sync"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

// One daemon owns one exclusive OS activity lease, shared by all its turns.
// External login/logout still cannot overlap any model or local-tool turn.
type sharedTurns struct {
	mu      sync.Mutex
	acquire func() (io.Closer, error)
	lease   io.Closer
	count   int
}

type sharedTurn struct {
	once  sync.Once
	owner *sharedTurns
}

func (s *sharedTurns) begin() (io.Closer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count == 0 {
		lease, err := s.acquire()
		if err != nil {
			return nil, err
		}
		s.lease = lease
	}
	s.count++
	return &sharedTurn{owner: s}, nil
}

func (t *sharedTurn) Close() error {
	t.once.Do(func() {
		s := t.owner
		s.mu.Lock()
		defer s.mu.Unlock()
		s.count--
		if s.count == 0 && s.lease != nil {
			s.lease.Close()
			s.lease = nil
		}
	})
	return nil
}

type multiAccountAccess struct {
	*accounts.Manager
	turns sharedTurns
}

func (a *multiAccountAccess) BeginTurn() (io.Closer, error) { return a.turns.begin() }

// Synchronous publication: secondary admission reads the latest snapshot,
// including credential invalidation, without a second poller or delayed queue.
type probeUsageCache struct {
	mu      sync.RWMutex
	samples []usage.Snapshot
}

func (u *probeUsageCache) store(samples []usage.Snapshot) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.samples = append([]usage.Snapshot(nil), samples...)
}
func (u *probeUsageCache) load() []usage.Snapshot {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.samples != nil {
		return append([]usage.Snapshot(nil), u.samples...)
	}
	samples := make([]usage.Snapshot, accountslot.Capacity)
	for i, slot := range accountslot.All() {
		samples[i] = usage.Snapshot{Slot: slot, State: "unknown", Stale: true}
	}
	return samples
}
