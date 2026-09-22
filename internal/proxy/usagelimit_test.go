package proxy

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUsageLimitClassification(t *testing.T) {
	const limit = `{"error":{"type":"usage_limit_reached","message":"synthetic message"}}`
	for _, tc := range []struct {
		name         string
		status       int
		format, body string
		want         bool
	}{
		{"type", 429, "application/json", limit, true},
		{"code", 429, "application/json", `{"error":{"code":"usage_limit_reached"}}`, true},
		{"unknown429", 429, "application/json", `{"error":{"code":"rate_limit_exceeded","message":"usage_limit_reached"}}`, false},
		{"conflict", 429, "application/json", `{"error":{"code":"rate_limit_exceeded","type":"usage_limit_reached"}}`, false},
		{"quota_budget", 429, "application/json", `{"error":{"code":"insufficient_quota"}}`, false},
		{"plain", 429, "text/plain", "usage_limit_reached", false},
		{"truncated", 429, "application/json", `{"error":{"type":"usage_limit_reached"}`, false},
		{"large", 429, "application/json", limit + strings.Repeat(" ", usageLimitProbeBytes), false},
		{"server_error", 500, "application/json", limit, false},
		{"auth", 401, "application/json", limit, false},
		{"sse", 200, "text/event-stream", "data: {\"type\":\"error\",\"code\":\"usage_limit_reached\"}\n\n", true},
		{"sse_nested", 200, "text/event-stream", "data: {\"type\":\"response.created\",\"response\":{\"output\":[]}}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"usage_limit_reached\"}}}\n\n", true},
		{"sse_output", 200, "text/event-stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"error\",\"code\":\"usage_limit_reached\"}\n\n", false},
		{"sse_tool", 200, "text/event-stream", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\"}}\n\ndata: {\"type\":\"error\",\"code\":\"usage_limit_reached\"}\n\n", false},
		{"sse_populated_created", 200, "text/event-stream", "data: {\"type\":\"response.created\",\"response\":{\"output\":[{}]}}\n\ndata: {\"type\":\"error\",\"code\":\"usage_limit_reached\"}\n\n", false},
		{"sse_large", 200, "text/event-stream", ":" + strings.Repeat("x", usageLimitProbeBytes) + "\n\ndata: {\"type\":\"error\",\"code\":\"usage_limit_reached\"}\n\n", false},
		{"sse_truncated", 200, "text/event-stream", "data: {\"type\":\"error\",\"code\":\"usage_limit_reached\"}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": []string{tc.format}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			if got := inspectUsageLimit(response); got != tc.want {
				t.Fatalf("classification %v", got)
			}
			restored, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || string(restored) != tc.body {
				t.Fatal("inspection changed body")
			}
		})
	}
}

type usageLimitInterrupted struct{ sent bool }

func (r *usageLimitInterrupted) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("synthetic read error")
	}
	r.sent = true
	return copy(p, `{"error":{"type":"usage_limit_reached"}}`), errors.New("synthetic read error")
}
func (*usageLimitInterrupted) Close() error { return nil }
func TestUsageLimitReadFailureNeverQualifies(t *testing.T) {
	response := &http.Response{StatusCode: 429, Header: http.Header{}, Body: &usageLimitInterrupted{}}
	if inspectUsageLimit(response) {
		t.Fatal("incomplete response allowed retry")
	}
}
