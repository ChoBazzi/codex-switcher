package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
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
	reasoning             probeReasoningOwners
	report                func(any)
}

func (a *probeAuxiliary) serve(w http.ResponseWriter, r *http.Request, mu *sync.Mutex, access probeAccess, upstream, salt string, changed func() bool, begin func() error) {
	reject := func(code string, status int) {
		if a.report != nil {
			a.report(map[string]any{"event": "probe_auxiliary_finished", "code": code, "last_http_status": status})
		}
		http.Error(w, code, status)
	}
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
			reject("probe_request_wait_timeout", 409)
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
		reject(code, 409)
		return
	}
	if r.URL.Path != "/responses" {
		mu.Unlock()
		reject("probe_auxiliary_compaction_unsupported", 409)
		return
	}
	if begin != nil {
		if err := begin(); err != nil {
			mu.Unlock()
			reject("account_operation_busy", 409)
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
		reject("proxy_checkpoint_unavailable", 503)
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
	body, err := readProbeBody(w, r)
	if err != nil {
		status, code := probeBodyRejection(err)
		reject(code, status)
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
		reject("probe_duplicate_followup", 409)
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
	boundary, _ := probeUserBoundary(body)
	body, err = probeToolBodyWithOwnership(body, a.binding.Slot, previous, salt+":"+a.binding.Thread, nil, func(item map[string]json.RawMessage) bool {
		mu.Lock()
		defer mu.Unlock()
		return a.reasoning.permits(item, a.credential)
	})
	if err != nil {
		detail := ""
		var parsed *probeBodyError
		if errors.As(err, &parsed) {
			detail = parsed.Reason
		}
		if a.report != nil {
			a.report(struct {
				Event string `json:"event"`
				proxy.Diagnostics
			}{"probe_auxiliary_finished", proxy.Diagnostics{Status: 409, Rejection: "probe_tool_history_unsupported", RejectionDetail: detail}})
		}
		http.Error(w, "probe_tool_history_unsupported", 409)
		return
	}
	mu.Lock()
	if a.handler == nil {
		a.handler, err = proxy.New(upstream, func(_ *http.Request) (proxy.Identity, error) {
			c, e := usage.RequestAccess(access, a.binding.Slot, time.Now())
			if e != nil {
				return proxy.Identity{}, probeAccessError(e)
			}
			hash := c.HistoryCredential()
			mu.Lock()
			defer mu.Unlock()
			if hash == ([32]byte{}) || a.credential != ([32]byte{}) && a.credential != hash {
				return proxy.Identity{}, proxy.ErrAuxiliaryCredential
			}
			a.credential = hash
			return proxy.Identity{Session: a.binding.Thread, Token: c.Token, AccountID: c.AccountID}, nil
		})
	}
	h := a.handler
	mu.Unlock()
	if err != nil {
		reject("probe_auxiliary_unavailable", 503)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	turn := &probeTurnWriter{ResponseWriter: w, holdTerminal: true}
	h.ServeHTTP(turn, r)
	d := h.Diagnostics()
	success = d.Status == 200 && d.ResponseFailure == "" && d.Rejection == "" && turn.valid()
	mu.Lock()
	if success && !a.reasoning.accept(turn.reasoning, a.credential, boundary) {
		success = false
	}
	a.pending = success && turn.pending()
	a.finishing = success
	mu.Unlock()
	if success {
		if turn.release() != nil {
			success = false
			d.ResponseFailure = "probe_client_write_failed"
		}
	}
	if !success && a.report != nil {
		if d.Status == 200 && d.ResponseFailure == "" && d.Rejection == "" {
			d.ResponseFailure = "probe_tool_response_unsupported"
		}
		a.report(struct {
			Event string `json:"event"`
			proxy.Diagnostics
		}{"probe_auxiliary_finished", d})
	}
}
