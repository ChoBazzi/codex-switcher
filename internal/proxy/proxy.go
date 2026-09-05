// Package proxy implements a single-attempt HTTP/SSE forwarding boundary.
// Account resolution is supplied by a trusted adapter; CLI auth is not implemented.
package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Identity struct{ Session, Token, AccountID string }
type Resolver func(*http.Request) (Identity, error)

type Handler struct {
	target    string
	resolve   Resolver
	transport *http.Transport
	mu        sync.Mutex
	busy      map[string]bool
	blocked   map[string]bool
}

func New(target string, resolve Resolver) (*Handler, error) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || resolve == nil {
		return nil, errors.New("invalid_proxy_configuration")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("https_required")
	}
	return &Handler{
		target: target, resolve: resolve, busy: map[string]bool{}, blocked: map[string]bool{},
		// Fresh HTTP/1 connections avoid retries on reused connections. RoundTrip
		// does not follow redirects. No environment proxy or replayable GetBody.
		transport: &http.Transport{
			Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
			ForceAttemptHTTP2: false,
			TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
		},
	}, nil
}

func (h *Handler) Close() { h.transport.CloseIdleConnections() }

func (h *Handler) begin(session string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.blocked[session] || h.busy[session] {
		return false
	}
	h.busy[session] = true
	return true
}

func (h *Handler) finish(session string, failed bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.busy, session)
	if failed {
		h.blocked[session] = true
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/responses" || r.URL.RawQuery != "" {
		reject(w, 404, "unsupported_route")
		return
	}
	if r.Header.Get("Upgrade") != "" {
		reject(w, 400, "unsupported_transport")
		return
	}
	identity, err := h.resolve(r)
	if err != nil || identity.Session == "" || identity.Token == "" {
		reject(w, 401, "session_unavailable")
		return
	}
	if !h.begin(identity.Session) {
		reject(w, 409, "session_requires_user_action")
		return
	}
	failed := true
	defer func() { h.finish(identity.Session, failed) }()
	w.Header().Set("X-Switcher-Request-ID", rand.Text())
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		reject(w, 413, "request_unreadable")
		return
	}
	if !json.Valid(body) {
		reject(w, 400, "invalid_json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	out, err := http.NewRequestWithContext(ctx, http.MethodPost, h.target, bytes.NewReader(body))
	if err != nil {
		reject(w, 502, "upstream_unavailable")
		return
	}
	out.GetBody = nil
	out.Close = true
	// Construct from scratch; never forward inbound auth, cookies, account IDs,
	// forwarding headers, or idempotency headers.
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Accept", "text/event-stream, application/json")
	out.Header.Set("Authorization", "Bearer "+identity.Token)
	if identity.AccountID != "" {
		out.Header.Set("ChatGPT-Account-ID", identity.AccountID)
	}
	resp, err := h.transport.RoundTrip(out)
	if err != nil {
		reject(w, 502, "upstream_transport_error")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		reject(w, 502, "upstream_redirect_rejected")
		return
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		reject(w, 502, "unsupported_encoding")
		return
	}
	contentType := resp.Header.Get("Content-Type")
	stream := strings.HasPrefix(strings.ToLower(contentType), "text/event-stream")
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	controller := http.NewResponseController(w)
	observer := eventObserver{}
	buf := make([]byte, 16*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if stream {
				if err := controller.Flush(); err != nil {
					return
				}
				if !observer.feed(buf[:n]) {
					panic(http.ErrAbortHandler)
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				panic(http.ErrAbortHandler)
			}
			if stream && (!observer.completed || observer.failed) {
				panic(http.ErrAbortHandler)
			}
			failed = resp.StatusCode >= 400
			return
		}
	}
}

func reject(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
}

// Observation never rewrites SSE bytes. Unknown events pass through, but a
// clean EOF without a completed event does not count as successful completion.
type eventObserver struct {
	buffer            []byte
	data              []byte
	completed, failed bool
}

func (o *eventObserver) feed(b []byte) bool {
	o.buffer = append(o.buffer, b...)
	if len(o.buffer)+len(o.data) > 1<<20 {
		return false
	}
	for {
		i := bytes.IndexByte(o.buffer, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(string(o.buffer[:i]), "\r")
		o.buffer = o.buffer[i+1:]
		if line == "" {
			var event struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(o.data, &event) == nil {
				switch event.Type {
				case "response.completed":
					o.completed = true
				case "response.failed", "response.incomplete", "error":
					o.failed = true
				}
			}
			o.data = nil
		} else if strings.HasPrefix(line, "data:") {
			o.data = append(o.data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")...)
			o.data = append(o.data, '\n')
		}
	}
	return !o.failed
}
