package proxy

import (
	"errors"
	"testing"
)

func TestCompletionShapeReasons(t *testing.T) {
	for _, tc := range []struct{ body, reason string }{
		{`data: synthetic-secret`, "completion_json_invalid"},
		{`{"id":"one","id":"two"}`, "completion_json_invalid"},
		{`null`, "completion_object_invalid"},
		{`[]`, "completion_object_invalid"},
		{`{"error":{"message":"synthetic-secret"}}`, "completion_error_present"},
		{`{"id":"synthetic"}`, "completion_status_invalid"},
		{`{"status":"completed"}`, "completion_id_invalid"},
		{`{"id":"synthetic","status":"completed","output":{}}`, "completion_output_invalid"},
		{`{"id":"synthetic","status":"completed","output":[null]}`, "completion_item_invalid"},
		{`{"id":"synthetic","status":"completed","output":[{}]}`, "completion_item_type_invalid"},
		{`{"id":"synthetic","status":"completed","output":[{"type":"message","role":"assistant","content":[],"id":null}]}`, "completion_item_id_invalid"},
		{`{"id":"synthetic","status":"completed","output":[{"type":"message","role":"assistant","content":[],"unknown":true}]}`, "completion_history_invalid"},
		{`{"id":"synthetic","status":"completed","error":{"message":"synthetic-secret"}}`, "completion_error_present"},
	} {
		_, err := completedRefs([]byte(tc.body))
		if !errors.Is(err, errShape) || completionReason(err) != tc.reason {
			t.Errorf("want %s, got %v", tc.reason, err)
		}
	}
	if _, err := completedRefs([]byte(`{"id":"synthetic","status":"completed","error":null,"output":[]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestResponseFormat(t *testing.T) {
	for _, tc := range []struct{ header, format string }{
		{"text/event-stream", "sse"},
		{" Text/Event-Stream; charset=utf-8 ", "sse"},
		{"text/event-stream-invalid", "other"},
		{"text/event-stream; bad", "invalid"},
		{"application/json; charset=utf-8", "json"},
		{"text/html", "other"},
		{"", "missing"},
	} {
		if got := responseFormat(tc.header); got != tc.format {
			t.Errorf("want %s, got %s", tc.format, got)
		}
	}
}
