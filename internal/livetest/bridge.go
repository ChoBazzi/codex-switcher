// Package livetest provides a one-request smoke test, not a general session router.
package livetest

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
)

// The backend URL is fixed; the live command cannot override its destination.
const Upstream = "https://chatgpt.com/backend-api/codex/responses"

type Bridge struct {
	proxy      *proxy.Handler
	secret     string
	access     accounts.Access
	accepted   atomic.Bool
	Requests   atomic.Int64
	Forwarded  atomic.Int64
	LastStatus atomic.Int64
	rejection  atomic.Value
}

// target is injectable only for synthetic package tests, not through the CLI.
func New(target, secret string, access accounts.Access) (*Bridge, error) {
	if len(secret) < 16 || access.Token == "" || access.AccountID == "" {
		return nil, errors.New("invalid_live_test_configuration")
	}
	b := &Bridge{secret: secret, access: access}
	h, err := proxy.New(target, func(r *http.Request) (proxy.Identity, error) {
		id, err := cliidentity.ThreadID(r.Header)
		if err != nil || !access.ExpiresAt.After(time.Now().Add(30*time.Second)) {
			return proxy.Identity{}, errors.New("live_credentials_unavailable")
		}
		return proxy.Identity{Session: id, Token: access.Token, AccountID: access.AccountID}, nil
	})
	if err != nil {
		return nil, err
	}
	b.proxy = h
	return b, nil
}
func (b *Bridge) Close() { b.proxy.Close() }

// Only locally defined constant codes are stored, never request or upstream text.
func (b *Bridge) RejectionCode() string {
	if value := b.rejection.Load(); value != nil {
		return value.(string)
	}
	return ""
}

func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w = &statusWriter{ResponseWriter: w, status: &b.LastStatus}
	b.Requests.Add(1)
	deny := func(status int, code string) {
		b.rejection.Store(code)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
	}
	if r.Method != "POST" || r.URL.Path != "/responses" || r.URL.RawQuery != "" || r.Header.Get("Upgrade") != "" {
		deny(400, "unsupported_live_test_request")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Switcher-Run")), []byte(b.secret)) != 1 {
		deny(401, "live_test_run_required")
		return
	}
	if _, err := cliidentity.ThreadID(r.Header); err != nil {
		deny(400, "invalid_conversation_identity")
		return
	}
	// All attempts sharing this launcher, including new thread IDs, share one budget.
	if !b.accepted.CompareAndSwap(false, true) {
		deny(409, "live_test_request_already_used")
		return
	}
	if !b.access.ExpiresAt.After(time.Now().Add(30 * time.Second)) {
		deny(401, "account_reauth_required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	data, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		deny(413, "request_unreadable")
		return
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(data, &body) != nil || body == nil {
		deny(400, "invalid_json")
		return
	}
	for _, key := range []string{"previous_response_id", "conversation"} {
		if raw, ok := body[key]; ok && string(raw) != "null" {
			deny(400, "continuation_not_allowed")
			return
		}
	}
	var input []json.RawMessage
	if json.Unmarshal(body["input"], &input) != nil {
		deny(400, "unsupported_input")
		return
	}
	for _, raw := range input {
		if !allowedInput(raw) {
			deny(400, "continuation_not_allowed")
			return
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	b.Forwarded.Add(1) // Entered proxy; not proof that the upstream processed it.
	b.proxy.ServeHTTP(w, r)
}

func allowedInput(raw json.RawMessage) bool {
	var item struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return false
	}
	if item.Type == "additional_tools" {
		// Codex's default model supplies inline tool definitions, not past
		// conversation. An ID-only reference must never pass this exception.
		if item.Role != "developer" {
			return false
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return false
		}
		for key := range fields {
			if key != "type" && key != "role" && key != "id" && key != "tools" {
				return false
			}
		}
		var id string
		if value, ok := fields["id"]; ok && string(bytes.TrimSpace(value)) != "null" {
			if json.Unmarshal(value, &id) != nil || len(id) > 256 {
				return false
			}
		}
		var tools []map[string]json.RawMessage
		if json.Unmarshal(fields["tools"], &tools) != nil || len(tools) == 0 {
			return false
		}
		for _, tool := range tools {
			if len(tool) == 0 {
				return false
			}
		}
		return true
	}
	return (item.Type == "message" || item.Type == "") && (item.Role == "user" || item.Role == "developer" || item.Role == "system")
}

type statusWriter struct {
	http.ResponseWriter
	status *atomic.Int64
}

func (w *statusWriter) WriteHeader(code int) {
	w.status.Store(int64(code))
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
