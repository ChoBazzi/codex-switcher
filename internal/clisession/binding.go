// Package clisession binds one helper-owned CLI process to one conversation.
package clisession

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
)

var ErrBinding = errors.New("cli_session_binding_unavailable")

type Binding struct {
	mu              sync.Mutex
	router          *routing.Router
	origin          checkpoint.Origin
	secret          string
	resume          string
	ready           chan struct{}
	started, closed bool
	failed          bool
}

// New is armed by an explicit launcher invocation containing user input.
// origin comes from trusted project metadata, never from HTTP headers.
// resume is empty only for a genuinely new CLI conversation.
func New(r *routing.Router, origin checkpoint.Origin, secret string, userInput bool, resume string) (*Binding, error) {
	if r == nil || origin.Project == "" || origin.Worktree == "" || origin.Branch == "" || origin.Session != "" || len(secret) < 16 || !userInput {
		return nil, ErrBinding
	}
	if resume != "" && !validID(resume) {
		return nil, ErrBinding
	}
	return &Binding{router: r, origin: origin, secret: secret, resume: resume, ready: make(chan struct{})}, nil
}
func validID(id string) bool {
	h := http.Header{}
	h.Set("Thread-Id", id)
	h.Set("Session-Id", id)
	_, err := cliidentity.ThreadID(h)
	return err == nil
}

// Started is called only from this process's trusted thread.started event.
// Failure is terminal for this binding and never falls back to a fresh session.
func (b *Binding) Started(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.started {
		return ErrBinding
	}
	b.started = true
	defer close(b.ready)
	b.failed = true
	if !validID(id) || b.resume != "" && id != b.resume {
		return ErrBinding
	}
	b.origin.Session = id
	if b.resume == "" {
		if _, err := b.router.Register(b.origin, true, time.Now()); err != nil {
			return ErrBinding
		}
	} else {
		if _, err := b.router.Resolve(b.origin, time.Now()); err != nil {
			return ErrBinding
		}
	}
	b.failed = false
	return nil
}

// Resolve authenticates the launcher before waiting for the start event.
// It never registers a session and never infers a new one from request headers.
func (b *Binding) Resolve(req *http.Request) (proxy.Identity, error) {
	values := req.Header.Values("X-Switcher-Run")
	if len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), []byte(b.secret)) != 1 {
		return proxy.Identity{}, ErrBinding
	}
	id, err := cliidentity.ThreadID(req.Header)
	if err != nil {
		return proxy.Identity{}, ErrBinding
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-b.ready:
	case <-req.Context().Done():
		return proxy.Identity{}, ErrBinding
	case <-timer.C:
		return proxy.Identity{}, ErrBinding
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.failed || id != b.origin.Session {
		return proxy.Identity{}, ErrBinding
	}
	identity, err := b.router.Resolve(b.origin, time.Now())
	if err != nil {
		return proxy.Identity{}, ErrBinding
	}
	return identity, nil
}

func (b *Binding) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	if !b.started {
		b.started = true
		close(b.ready)
	}
}
