// Package cliprobe contains synthetic-only fixtures for testing the installed
// Codex executable. It never selects a live upstream or reads account tokens.
package cliprobe

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

const Reply = "Synthetic proxy demo; no model called."

type Upstream struct {
	Scenario          string
	Calls             atomic.Int64
	CompactCompletion bool
	EmptyLogprobs     bool
}

func ValidScenario(s string) bool {
	return s == "success" || s == "rate-limit" || s == "server-error" || s == "partial"
}

func (u *Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.Calls.Add(1)
	defer r.Body.Close()
	if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 4<<20)); err != nil {
		w.WriteHeader(413)
		return
	}
	if u.Scenario == "rate-limit" || u.Scenario == "server-error" {
		w.Header().Set("Content-Type", "application/json")
		status := 429
		if u.Scenario == "server-error" {
			status = 503
		}
		w.WriteHeader(status)
		io.WriteString(w, `{"error":{"type":"synthetic_failure","code":"synthetic_failure","message":"Synthetic failure; no model called."}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	sequence := 0
	emit := func(kind string, fields map[string]any) {
		fields["type"] = kind
		fields["sequence_number"] = sequence
		sequence++
		data, _ := json.Marshal(fields)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
		_ = http.NewResponseController(w).Flush()
	}
	part := map[string]any{"type": "output_text", "text": Reply, "annotations": []any{}}
	if u.EmptyLogprobs {
		part["logprobs"] = []any{}
	}
	item := map[string]any{"id": "msg_synthetic", "type": "message", "role": "assistant", "status": "completed", "content": []any{part}}
	emit("response.created", map[string]any{"response": map[string]any{"id": "resp_synthetic", "object": "response", "status": "in_progress", "output": []any{}}})
	emit("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_synthetic", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}})
	emit("response.content_part.added", map[string]any{"item_id": "msg_synthetic", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
	emit("response.output_text.delta", map[string]any{"item_id": "msg_synthetic", "output_index": 0, "content_index": 0, "delta": Reply})
	if u.Scenario == "partial" {
		return
	}
	emit("response.output_text.done", map[string]any{"item_id": "msg_synthetic", "output_index": 0, "content_index": 0, "text": Reply})
	emit("response.content_part.done", map[string]any{"item_id": "msg_synthetic", "output_index": 0, "content_index": 0, "part": part})
	emit("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	output := []any{item}
	if u.CompactCompletion {
		output = []any{}
	}
	emit("response.completed", map[string]any{"response": map[string]any{"id": "resp_synthetic", "object": "response", "status": "completed", "output": output, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
}
