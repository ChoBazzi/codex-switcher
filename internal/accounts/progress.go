package accounts

import (
	"context"
	"sync"

	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
)

// AuthenticationStatus contains only local slot labels and current activity.
// It never reads credentials or starts work, and is independent of vault locks.
type AuthenticationStatus struct {
	Slot       string `json:"slot"`
	Waiting    int    `json:"waiting"`
	Refreshing bool   `json:"refreshing"`
	Canceled   bool   `json:"canceled"`
}

type authenticationProgress struct {
	mu         sync.Mutex
	waiting    [accountslot.Capacity]int
	refreshing [accountslot.Capacity]*authenticationRefresh
}

type authenticationRefresh struct{ ctx context.Context }

func (p *authenticationProgress) begin(slot string, ctx context.Context) (refreshing func(), finish func()) {
	i := -1
	for n, candidate := range accountslot.All() {
		if candidate == slot {
			i = n
			break
		}
	}
	if i < 0 {
		return func() {}, func() {}
	}
	p.mu.Lock()
	p.waiting[i]++
	p.mu.Unlock()
	started := false // callbacks belong only to the initiating request goroutine
	active := &authenticationRefresh{ctx: ctx}
	return func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.waiting[i]--
			p.refreshing[i] = active
			started = true
		}, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if started {
				if p.refreshing[i] == active {
					p.refreshing[i] = nil
				}
			} else {
				p.waiting[i]--
			}
		}
}

func (m *Manager) AuthenticationStatus() []AuthenticationStatus {
	p := &m.authentication
	p.mu.Lock()
	defer p.mu.Unlock()
	status := make([]AuthenticationStatus, 0, accountslot.Capacity)
	for i, slot := range accountslot.All() {
		active := p.refreshing[i]
		if p.waiting[i] == 0 && active == nil {
			continue
		}
		status = append(status, AuthenticationStatus{Slot: slot, Waiting: p.waiting[i], Refreshing: active != nil, Canceled: active != nil && active.ctx.Err() != nil})
	}
	return status
}
