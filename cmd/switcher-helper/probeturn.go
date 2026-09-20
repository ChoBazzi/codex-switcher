package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
)

// Observes bounded SSE frames, never persists/logs contents or rewrites bytes.
// An HTTP completion containing a tool call is NOT the end of a CLI turn.
type probeTurnWriter struct {
	http.ResponseWriter
	buffer, data                      []byte
	completed, invalid, called, final bool
	holdTerminal                      bool
	frame, held                       []byte
	onTerminal                        func()
	reasoning                         map[[32]byte]bool
}

func (w *probeTurnWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *probeTurnWriter) Write(b []byte) (int, error) {
	if w.holdTerminal {
		for _, c := range b {
			w.frame = append(w.frame, c)
			if len(w.frame)+len(w.held) > 1<<20 {
				w.invalid = true
				return 0, errors.New("probe_stream_buffer_exceeded")
			}
			if c != '\n' || !(bytes.HasSuffix(w.frame, []byte("\n\n")) || bytes.HasSuffix(w.frame, []byte("\r\n\r\n"))) {
				continue
			}
			wasCompleted := w.completed
			w.feed(w.frame)
			if w.invalid {
				return 0, errors.New("probe_tool_response_unsupported")
			}
			if w.completed {
				w.held = append(w.held, w.frame...)
				if !wasCompleted && w.onTerminal != nil {
					w.onTerminal()
				}
			} else if _, err := w.ResponseWriter.Write(w.frame); err != nil {
				return 0, err
			}
			w.frame = nil
		}
		return len(b), nil
	}
	w.feed(b)
	return w.ResponseWriter.Write(b)
}
func (w *probeTurnWriter) feed(b []byte) {
	if w.invalid {
		return
	}
	w.buffer = append(w.buffer, b...)
	for {
		if len(w.buffer)+len(w.data) > 1<<20 {
			w.invalid = true
			w.buffer, w.data = nil, nil
			return
		}
		i := bytes.IndexByte(w.buffer, '\n')
		if i < 0 {
			return
		}
		line := bytes.TrimSuffix(w.buffer[:i], []byte{'\r'})
		w.buffer = w.buffer[i+1:]
		if len(line) == 0 {
			if len(w.data) != 0 {
				w.event(w.data)
			}
			w.data = nil
		} else if bytes.HasPrefix(line, []byte("data:")) {
			w.data = append(w.data, bytes.TrimPrefix(line[5:], []byte(" "))...)
			w.data = append(w.data, '\n')
		}
	}
}
func (w *probeTurnWriter) event(raw []byte) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("[DONE]")) {
		return
	}
	var event struct {
		Type     string                     `json:"type"`
		Item     map[string]json.RawMessage `json:"item"`
		Response struct {
			Status string                       `json:"status"`
			Output []map[string]json.RawMessage `json:"output"`
			Error  json.RawMessage              `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &event) != nil {
		w.invalid = true
		return
	}
	switch event.Type {
	case "response.output_item.done":
		if w.completed {
			w.invalid = true
		}
		w.item(event.Item)
	case "response.completed":
		if w.completed || event.Response.Status != "completed" || (len(event.Response.Error) != 0 && string(bytes.TrimSpace(event.Response.Error)) != "null") {
			w.invalid = true
		}
		w.completed = true
		for _, item := range event.Response.Output {
			w.item(item)
		}
	case "error", "response.failed", "response.incomplete":
		w.invalid = true
	}
}
func (w *probeTurnWriter) item(item map[string]json.RawMessage) {
	switch probeString(item, "type") {
	case "function_call", "custom_tool_call":
		w.called = true
	case "message":
		phase := probeString(item, "phase")
		if probeString(item, "role") == "assistant" && (phase == "" || phase == "final_answer") {
			w.final = true
		}
	case "reasoning":
		if w.reasoning == nil {
			w.reasoning = map[[32]byte]bool{}
		}
		w.reasoning[probeReasoningKey(item)] = true
		if len(w.reasoning) > 1024 {
			w.invalid = true
		}
	default:
		w.invalid = true
	}
}
func (w *probeTurnWriter) valid() bool {
	return w.completed && !w.invalid && len(bytes.TrimSpace(w.buffer)) == 0 && len(w.data) == 0 && len(w.frame) == 0
}
func (w *probeTurnWriter) release() error {
	if !w.valid() {
		return errors.New("probe_tool_response_unsupported")
	}
	if _, err := w.ResponseWriter.Write(w.held); err != nil {
		return err
	}
	w.held = nil
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *probeTurnWriter) pending() bool { return w.called || !w.final }
