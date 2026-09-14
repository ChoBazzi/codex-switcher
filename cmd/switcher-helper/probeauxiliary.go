package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

// An auxiliary conversation never borrows the parent's handler or failure state.
// Persist its binding before dispatch; after service restart it is a tombstone.
// This prevents replay of interrupted background work without user input.
type probeAuxiliaryBinding struct{ Thread, Root, Slot string }
type probeAuxiliary struct {
	binding               probeAuxiliaryBinding
	handler               *proxy.Handler
	busy, pending, failed bool
	finishing             bool
	done                  chan struct{}
	credential            [32]byte
	lastBody              [32]byte
}

func (a *probeAuxiliary) serve(w http.ResponseWriter, r *http.Request, mu *sync.Mutex, access probeAccess, upstream, salt string, changed func() bool, begin func() error) {
	waited := false
	mu.Lock()
	if a.busy && a.finishing {
		done := a.done
		mu.Unlock()
		select {
		case <-done:
			waited = true
		case <-r.Context().Done():
			return
		case <-time.After(30 * time.Second):
			http.Error(w, "probe_request_wait_timeout", 409)
			return
		}
		mu.Lock()
	}
	if a.failed || a.busy {
		code := "probe_previous_request_failed"
		if a.busy {
			code = "probe_request_in_progress"
		}
		mu.Unlock()
		http.Error(w, code, 409)
		return
	}
	if r.URL.Path != "/responses" {
		mu.Unlock()
		http.Error(w, "probe_auxiliary_compaction_unsupported", 409)
		return
	}
	if begin != nil {
		if err := begin(); err != nil {
			mu.Unlock()
			http.Error(w, "account_operation_busy", 409)
			return
		}
	}
	a.busy = true
	a.done = make(chan struct{})
	a.finishing = false
	if !changed() {
		a.busy = false
		close(a.done)

		mu.Unlock()
		http.Error(w, "proxy_checkpoint_unavailable", 503)
		return
	}
	mu.Unlock()
	success := false
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		a.busy = false
		a.finishing = false
		close(a.done)
		if !success {
			a.failed = true
			a.pending = false
		}

		changed()
	}()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	r.Body.Close()
	if err != nil {
		http.Error(w, "probe_tool_history_unsupported", 409)
		return
	}
	hash := sha256.Sum256(body)
	mu.Lock()
	duplicate := waited && hash == a.lastBody
	if !duplicate {
		a.lastBody = hash
	}
	mu.Unlock()
	if duplicate {
		success = true
		http.Error(w, "probe_duplicate_followup", 409)
		return
	}
	// A new branch may contain parent reasoning; reject it unless this handler has
	// already verified its own credential. Full local tool pairs remain portable.
	previous := ""
	mu.Lock()
	if a.handler != nil {
		previous = a.binding.Slot
	}
	mu.Unlock()
	body, err = probeToolBody(body, a.binding.Slot, previous, salt+":"+a.binding.Thread)
	if err != nil {
		http.Error(w, "probe_tool_history_unsupported", 409)
		return
	}
	mu.Lock()
	if a.handler == nil {
		a.handler, err = proxy.New(upstream, func(_ *http.Request) (proxy.Identity, error) {
			c, e := usage.RequestAccess(access, a.binding.Slot, time.Now())
			if e != nil {
				return proxy.Identity{}, e
			}
			hash := sha256.Sum256([]byte(c.Token))
			mu.Lock()
			defer mu.Unlock()
			if a.credential != ([32]byte{}) && a.credential != hash {
				return proxy.Identity{}, errors.New("auxiliary_credential_changed")
			}
			a.credential = hash
			return proxy.Identity{Session: a.binding.Thread, Token: c.Token, AccountID: c.AccountID}, nil
		})
	}
	h := a.handler
	mu.Unlock()
	if err != nil {
		http.Error(w, "probe_auxiliary_unavailable", 503)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	turn := &probeTurnWriter{ResponseWriter: w, holdTerminal: true}
	h.ServeHTTP(turn, r)
	d := h.Diagnostics()
	success = d.Status == 200 && d.ResponseFailure == "" && d.Rejection == "" && turn.valid()
	mu.Lock()
	a.pending = success && turn.pending()
	a.finishing = success
	mu.Unlock()
	if success {
		if turn.release() != nil {
			success = false
		}
	}
}
