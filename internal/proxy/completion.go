package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
)

// deliverPersistent withholds terminal bytes until EOF validation and commit.
// A delivery error after commit cannot undo a known completed server response.
// No failure is replayed; false/panic leaves the deferred failure path in charge.
func (h *Handler) deliverPersistent(w http.ResponseWriter, resp *http.Response, lease affinity.Lease, stream bool) bool {
	code, seen, committed := "response_processing_interrupted", false, false
	reason := ""
	defer func() {
		h.mu.Lock()
		h.diagnostic.CompletionRejection = reason
		h.mu.Unlock()
	}()
	defer func() { h.responseDiagnostic(code, seen, committed) }()
	if !stream {
		body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		if err != nil {
			code = responseReadFailure(err)
			panic(http.ErrAbortHandler)
		}
		if len(body) > 4<<20 {
			code = "response_buffer_exceeded"
			panic(http.ErrAbortHandler)
		}
		ids, err := completedRefs(body)
		if err != nil {
			code = "completion_shape_invalid"
			reason = completionReason(err)
			panic(http.ErrAbortHandler)
		}
		seen = true
		if err := h.store.Finish(lease, ids, true, time.Now()); err != nil {
			code = completionStoreFailure(err)
			panic(http.ErrAbortHandler)
		}
		committed, code = true, ""
		w.WriteHeader(resp.StatusCode)
		if _, err := w.Write(body); err != nil {
			code = "client_write_failed_after_commit"
		}
		return true
	}
	w.WriteHeader(resp.StatusCode)
	observer := eventObserver{strict: true}
	gate := completionGate{}
	controller := http.NewResponseController(w)
	buf := make([]byte, 16*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			forward, ok := gate.feed(buf[:n], &observer)
			seen = observer.completed
			if !ok {
				code = observer.failureCode
				reason = observer.completionRejection
				if code == "" {
					code = "stream_buffer_exceeded"
				}
				panic(http.ErrAbortHandler)
			}
			if len(forward) > 0 {
				if _, err := w.Write(forward); err != nil {
					code = "client_write_failed"
					return false
				}
				if controller.Flush() != nil {
					code = "client_flush_failed"
					return false
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				code = responseReadFailure(err)
				panic(http.ErrAbortHandler)
			}
			if !observer.completed {
				code = "completion_missing"
				panic(http.ErrAbortHandler)
			}
			if observer.failed {
				code = observer.failureCode
				panic(http.ErrAbortHandler)
			}
			if len(gate.pending) != 0 || len(gate.frame) != 0 {
				code = "stream_trailing_frame_incomplete"
				panic(http.ErrAbortHandler)
			}
			if len(observer.responseIDs) == 0 {
				code = "completion_ids_missing"
				panic(http.ErrAbortHandler)
			}
			if err := h.store.Finish(lease, observer.responseIDs, true, time.Now()); err != nil {
				code = completionStoreFailure(err)
				panic(http.ErrAbortHandler)
			}
			committed, code = true, ""
			if _, err := w.Write(gate.terminal); err != nil {
				code = "client_write_failed_after_commit"
			} else if controller.Flush() != nil {
				code = "client_flush_failed_after_commit"
			}
			return true
		}
	}
}

func responseReadFailure(err error) string {
	if errors.Is(err, context.Canceled) {
		return "response_read_canceled"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return "response_read_timeout"
	}
	return "response_read_failed"
}
func completionStoreFailure(err error) string {
	if errors.Is(err, affinity.ErrConflict) {
		return "completion_store_conflict"
	}
	return "completion_store_failed"
}

// Preserve original SSE bytes, including CRLF and multi-line data fields.
// Only complete pre-terminal frames can be released. Terminal plus trailing
// events stay bounded and are still observed to reject duplicate/failure events.
type completionGate struct{ pending, frame, terminal []byte }

func (g *completionGate) feed(data []byte, observer *eventObserver) ([]byte, bool) {
	g.pending = append(g.pending, data...)
	var forward []byte
	for {
		if len(g.pending)+len(g.frame)+len(g.terminal) > 1<<20 {
			return nil, false
		}
		i := bytes.IndexByte(g.pending, '\n')
		if i < 0 {
			return forward, true
		}
		line := g.pending[:i+1]
		g.frame = append(g.frame, line...)
		blank := len(bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})) == 0
		g.pending = g.pending[i+1:]
		if !blank {
			continue
		}
		if !observer.feed(g.frame) {
			return nil, false
		}
		if observer.completed {
			g.terminal = append(g.terminal, g.frame...)
		} else {
			forward = append(forward, g.frame...)
		}
		g.frame = nil
	}
}
