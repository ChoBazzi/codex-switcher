// Package proxy implements a single-attempt HTTP/SSE forwarding boundary.
// Account resolution is supplied by a trusted adapter; CLI auth is not implemented.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

type Identity struct {
	Session, Token, AccountID string
	Origin                    checkpoint.Origin
	Slot                      string
}
type Resolver func(*http.Request) (Identity, error)

type Handler struct {
	// Opt-in managed owner must consume Diagnostics.UsageLimit and either
	// switch accounts or emit an error. A recognized limit writes no response.
	DeferUsageLimit bool
	// BeforeAttempt is configured before serving by the trusted launcher.
	// It cannot add an upstream attempt and may reject dispatch locally.
	BeforeAttempt func() error
	// Optional native compact endpoint for a non-persistent trusted adapter.
	// Validate the complete bounded JSON window before any success bytes leave.
	ValidateCompaction func([]byte) error
	target             string
	resolve            Resolver
	transport          *http.Transport
	mu                 sync.Mutex
	busy               map[string]bool
	blocked            map[string]bool
	store              *affinity.Store
	diagnostic         Diagnostics
}

// Diagnostics contains only local constants and counters, never upstream text.
type Diagnostics struct {
	UsageLimit          bool   `json:"usage_limit,omitempty"`
	Requests            int    `json:"cli_requests"`
	Attempts            int    `json:"upstream_attempts"`
	Status              int    `json:"last_http_status"`
	Rejection           string `json:"local_rejection_code"`
	RejectionDetail     string `json:"local_rejection_detail,omitempty"`
	ResponseFailure     string `json:"response_failure_code"`
	ResponseFormat      string `json:"response_format"`
	CompletionRejection string `json:"completion_rejection_code"`
	CompletionSeen      bool   `json:"completion_seen"`
	CompletionCommitted bool   `json:"completion_committed"`
}

func (h *Handler) responseDiagnostic(code string, seen, committed bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.diagnostic.ResponseFailure = code
	h.diagnostic.CompletionSeen = seen
	h.diagnostic.CompletionCommitted = committed
}

func (h *Handler) Diagnostics() Diagnostics {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.diagnostic
}

func (h *Handler) reject(w http.ResponseWriter, status int, code string) {
	h.mu.Lock()
	h.diagnostic.Status, h.diagnostic.Rejection = status, code
	h.mu.Unlock()
	reject(w, status, code)
}

// NewPersistent requires a trusted resolver with verified origin and slot.
// It does not register unknown sessions or manage the store's lifetime.
func NewPersistent(target string, resolve Resolver, store *affinity.Store) (*Handler, error) {
	if store == nil {
		return nil, errors.New("persistent_store_required")
	}
	h, err := New(target, resolve)
	if err != nil {
		return nil, err
	}
	h.store = store
	return h, nil
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

// Do not log the raw media type: it is untrusted upstream text.
func responseFormat(value string) string {
	if strings.TrimSpace(value) == "" {
		return "missing"
	}
	media, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "invalid"
	}
	switch media {
	case "text/event-stream":
		return "sse"
	case "application/json":
		return "json"
	default:
		return "other"
	}
}

type bufferedResponseBody struct {
	*bufio.Reader
	io.Closer
}

// Only missing headers on successful persistent responses use this fallback.
// Peek preserves bytes and read errors; a prefix selects parsing, not success.
func missingHeaderSSE(resp *http.Response) bool {
	r := bufio.NewReader(resp.Body)
	resp.Body = bufferedResponseBody{Reader: r, Closer: resp.Body}
	first, err := r.Peek(1)
	if err != nil {
		return false
	}
	var prefix string
	switch first[0] {
	case ':':
		return true
	case 'd':
		prefix = "data:"
	case 'e':
		prefix = "event:"
	default:
		return false
	}
	b, _ := r.Peek(len(prefix))
	return string(b) == prefix
}

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
	h.mu.Lock()
	h.diagnostic.Requests++
	h.diagnostic.UsageLimit = false
	h.diagnostic.Status, h.diagnostic.Rejection = 0, ""
	h.diagnostic.ResponseFailure = ""
	h.diagnostic.ResponseFormat = ""
	h.diagnostic.CompletionRejection = ""
	h.diagnostic.CompletionSeen, h.diagnostic.CompletionCommitted = false, false
	h.mu.Unlock()
	compact := r.URL.Path == "/responses/compact" && h.ValidateCompaction != nil && h.store == nil
	if r.Method != http.MethodPost || (r.URL.Path != "/responses" && !compact) || r.URL.RawQuery != "" {
		h.reject(w, 404, "unsupported_route")
		return
	}
	if r.Header.Get("Upgrade") != "" {
		h.reject(w, 400, "unsupported_transport")
		return
	}
	if r.Context().Err() != nil {
		h.reject(w, 408, "request_canceled")
		return
	}
	identity, err := h.resolve(r)
	if r.Context().Err() != nil {
		h.reject(w, 408, "request_canceled")
		return
	}
	if err != nil || identity.Session == "" || identity.Token == "" {
		status, code := identityRejection(err)
		h.reject(w, status, code)
		return
	}
	if !h.begin(identity.Session) {
		h.reject(w, 409, "session_requires_user_action")
		return
	}
	failed := true
	defer func() { h.finish(identity.Session, failed) }()
	w.Header().Set("X-Switcher-Request-ID", rand.Text())
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.reject(w, 413, "request_unreadable")
		return
	}
	if !json.Valid(body) {
		h.reject(w, 400, "invalid_json")
		return
	}
	var lease affinity.Lease
	if h.store != nil {
		refs, err := continuationRefs(body)
		if err != nil {
			h.reject(w, 400, "unsupported_persistent_request")
			return
		}
		s, err := h.store.Lookup(identity.Session)
		if err != nil || identity.Origin.Session != identity.Session || s.Origin != identity.Origin || s.Account != identity.Slot {
			h.reject(w, 409, "session_owner_unverified")
			return
		}
		lease, err = h.store.BeginWithInputs(identity.Origin, refs, inlineMessageIDs(body), time.Now())
		if err != nil {
			h.reject(w, 409, "continuation_or_session_unavailable")
			return
		}
		defer func() {
			if failed {
				_ = h.store.Finish(lease, nil, false, time.Now())
			}
		}()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	target := h.target
	if compact {
		target = strings.TrimRight(target, "/") + "/compact"
	}
	out, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		h.reject(w, 502, "upstream_unavailable")
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
	if h.BeforeAttempt != nil {
		if err := h.BeforeAttempt(); err != nil {
			h.reject(w, 409, "request_dispatch_rejected")
			return
		}
	}
	if ctx.Err() != nil {
		h.reject(w, 408, "request_canceled")
		return
	}
	h.mu.Lock()
	h.diagnostic.Attempts++
	h.mu.Unlock()
	resp, err := h.transport.RoundTrip(out)
	if err != nil {
		h.reject(w, 502, "upstream_transport_error")
		return
	}
	defer resp.Body.Close()
	h.mu.Lock()
	h.diagnostic.Status = resp.StatusCode
	h.mu.Unlock()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		h.reject(w, 502, "upstream_redirect_rejected")
		return
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		h.reject(w, 502, "unsupported_encoding")
		return
	}
	if h.DeferUsageLimit && !compact && h.store == nil && inspectUsageLimit(resp) {
		h.mu.Lock()
		h.diagnostic.Status = http.StatusTooManyRequests
		h.diagnostic.UsageLimit = true
		h.diagnostic.Rejection = "upstream_usage_limit"
		h.mu.Unlock()
		return
	}
	contentType := resp.Header.Get("Content-Type")
	format := responseFormat(contentType)
	stream := format == "sse"
	if format == "missing" && h.store != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && missingHeaderSSE(resp) {
		stream, format = true, "missing_sse"
	}
	h.mu.Lock()
	h.diagnostic.ResponseFormat = format
	h.mu.Unlock()
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Cache-Control", "no-store")
	if compact {
		if resp.StatusCode != http.StatusOK {
			h.reject(w, resp.StatusCode, "compaction_upstream_failed")
			return
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		if err != nil || len(data) > 4<<20 || (format != "json" && format != "missing") || !json.Valid(data) || h.ValidateCompaction(data) != nil {
			h.reject(w, 502, "compaction_response_invalid")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(data); err != nil {
			return
		}
		failed = false
		h.responseDiagnostic("", true, true)
		return
	}
	if h.store != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		failed = !h.deliverPersistent(w, resp, lease, stream)
		return
	}
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
			failed = resp.StatusCode < 200 || resp.StatusCode >= 300
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
	buffer              []byte
	data                []byte
	completed, failed   bool
	strict              bool
	responseIDs         []string
	failureCode         string
	completionRejection string
	outputRefs          []string
	outputBytes         int
}

func (o *eventObserver) feed(b []byte) bool {
	o.buffer = append(o.buffer, b...)
	if len(o.buffer)+len(o.data) > 1<<20 {
		o.failureCode = "stream_buffer_exceeded"
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
				Type     string          `json:"type"`
				Response json.RawMessage `json:"response"`
				Item     json.RawMessage `json:"item"`
			}
			if json.Unmarshal(o.data, &event) == nil {
				switch event.Type {
				case "response.output_item.done":
					if o.strict {
						o.outputBytes += len(event.Item)
						if o.completed || o.outputBytes > 1<<20 {
							o.failed, o.failureCode = true, "stream_output_invalid"
							break
						}
						// Validate using the same output contract as the final response.
						wrapper := append([]byte(`{"id":"synthetic-output-validation","status":"completed","output":[`), event.Item...)
						wrapper = append(wrapper, ']', '}')
						refs, err := completedRefs(wrapper)
						if err != nil {
							o.failed, o.failureCode = true, "completion_shape_invalid"
							o.completionRejection = completionReason(err)
						} else {
							o.outputRefs = append(o.outputRefs, refs[1:]...)
						}
					}
				case "response.completed":
					if o.strict {
						ids, err := completedRefs(event.Response)
						if err != nil || o.completed {
							o.failed = true
							if o.failureCode == "" {
								if o.completed {
									o.failureCode = "duplicate_completion"
								} else {
									o.failureCode = "completion_shape_invalid"
									o.completionRejection = completionReason(err)
								}
							}
						} else {
							o.responseIDs = mergeResponseRefs(ids, o.outputRefs)
						}
					}
					o.completed = true
				case "response.failed", "response.incomplete", "error":
					o.failed = true
					if o.failureCode == "" {
						switch event.Type {
						case "response.failed":
							o.failureCode = "upstream_response_failed"
						case "response.incomplete":
							o.failureCode = "upstream_response_incomplete"
						case "error":
							o.failureCode = "upstream_error_event"
						}
					}
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
