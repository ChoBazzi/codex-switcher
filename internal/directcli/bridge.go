// Package directcli registers user-launched CLI sessions through authenticated
// lifecycle hooks. Model HTTP headers alone never create a session.
package directcli

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
)

var ErrSession = errors.New("direct_cli_session_unavailable")

type Event struct {
	Name      string `json:"hook_event_name"`
	Session   string `json:"session_id"`
	Directory string `json:"cwd"`
	Source    string `json:"source,omitempty"`
}

// DecodeEvent deliberately discards prompt/transcript/model output fields.
func DecodeEvent(r io.Reader) (Event, error) {
	data, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return Event{}, ErrSession
	}
	var e Event
	if json.Unmarshal(data, &e) != nil {
		return Event{}, ErrSession
	}
	h := http.Header{}
	h.Set("Thread-Id", e.Session)
	h.Set("Session-Id", e.Session)
	if _, err := cliidentity.ThreadID(h); err != nil || !filepath.IsAbs(e.Directory) {
		return Event{}, ErrSession
	}
	switch e.Name {
	case "SessionStart":
		if e.Source != "startup" && e.Source != "clear" && e.Source != "resume" && e.Source != "compact" {
			return Event{}, ErrSession
		}
	case "UserPromptSubmit", "SessionEnd":
	default:
		return Event{}, ErrSession
	}
	return e, nil
}

type entry struct {
	origin            checkpoint.Origin
	directory, source string
	ready             bool
}
type Bridge struct {
	mu                      sync.Mutex
	router                  *routing.Router
	project                 checkpoint.Origin
	modelSecret, hookSecret string
	sessions                map[string]entry
}

func New(router *routing.Router, project checkpoint.Origin, modelSecret, hookSecret string) (*Bridge, error) {
	if router == nil || project.Project == "" || project.Session != "" || len(modelSecret) < 32 || len(hookSecret) < 32 || modelSecret == hookSecret {
		return nil, ErrSession
	}
	return &Bridge{router: router, project: project, modelSecret: modelSecret, hookSecret: hookSecret, sessions: map[string]entry{}}, nil
}

func authenticated(r *http.Request, name, secret string) bool {
	values := r.Header.Values(name)
	return len(values) == 1 && subtle.ConstantTimeCompare([]byte(values[0]), []byte(secret)) == 1
}

func (b *Bridge) Hook(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || !authenticated(r, "X-Switcher-Hook", b.hookSecret) {
		http.Error(w, "direct_hook_unauthorized", 401)
		return
	}
	e, err := DecodeEvent(r.Body)
	if err == nil {
		err = b.event(r.Context(), e)
	}
	if err != nil {
		http.Error(w, "direct_cli_session_unavailable", 409)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"ok":true}`)
}

func (b *Bridge) event(ctx context.Context, e Event) error {
	origin, err := projectidentity.Resolve(ctx, e.Directory)
	if err != nil || origin != b.project {
		return ErrSession
	}
	origin.Session = e.Session
	b.mu.Lock()
	defer b.mu.Unlock()
	current, exists := b.sessions[e.Session]
	switch e.Name {
	case "SessionStart":
		if exists {
			if e.Source != "compact" || current.origin != origin {
				return ErrSession
			}
			return nil
		}
		if len(b.sessions) >= 64 {
			return ErrSession
		}
		_, known := b.router.Session(e.Session)
		if (e.Source == "startup" || e.Source == "clear") == known {
			return ErrSession
		}
		b.sessions[e.Session] = entry{origin: origin, directory: e.Directory, source: e.Source}
	case "UserPromptSubmit":
		if !exists || current.origin != origin {
			return ErrSession
		}
		if !current.ready && (current.source == "startup" || current.source == "clear") {
			if _, err := b.router.Register(origin, true, time.Now()); err != nil {
				return ErrSession
			}
		} else if _, err := b.router.Resolve(origin, time.Now()); err != nil {
			return ErrSession
		}
		current.ready = true
		b.sessions[e.Session] = current
	case "SessionEnd":
		if !exists || current.origin != origin {
			return ErrSession
		}
		delete(b.sessions, e.Session)
	default:
		return ErrSession
	}
	return nil
}

func (b *Bridge) Resolve(r *http.Request) (proxy.Identity, error) {
	if !authenticated(r, "X-Switcher-Run", b.modelSecret) {
		return proxy.Identity{}, ErrSession
	}
	id, err := cliidentity.ThreadID(r.Header)
	if err != nil {
		return proxy.Identity{}, ErrSession
	}
	b.mu.Lock()
	current, ok := b.sessions[id]
	b.mu.Unlock()
	if !ok || !current.ready {
		return proxy.Identity{}, ErrSession
	}
	origin, err := projectidentity.Resolve(r.Context(), current.directory)
	if err != nil {
		return proxy.Identity{}, ErrSession
	}
	origin.Session = id
	if origin != current.origin {
		return proxy.Identity{}, ErrSession
	}
	return b.router.Resolve(origin, time.Now())
}
