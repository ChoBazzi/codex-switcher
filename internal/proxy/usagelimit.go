package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

const usageLimitProbeBytes = 64 << 10

// Deliberately exclude rate_limit_exceeded (RPM/TPM), 429 alone and message
// text. Only this explicit Codex account-usage code authorizes another account.
func usageLimitError(raw json.RawMessage) bool {
	var e struct {
		Type string `json:"type"`
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return false
	}
	return (e.Code == "usage_limit_reached" && (e.Type == "" || e.Type == "usage_limit_reached" || e.Type == "error")) || (e.Type == "usage_limit_reached" && e.Code == "")
}
func usageLimitJSON(data []byte) bool {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	return json.Unmarshal(data, &e) == nil && usageLimitError(e.Error)
}

// Inspect only a bounded prefix before forwarding anything. Always reconstruct
// unclassified bytes, including on malformed, oversized or interrupted input.
// No model output event, even reasoning or a tool delta, may precede failover.
func inspectUsageLimit(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		source := resp.Body
		timer := time.AfterFunc(3*time.Second, func() { source.Close() })
		data, err := io.ReadAll(io.LimitReader(source, usageLimitProbeBytes+1))
		timer.Stop()
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(data), source), source}
		return err == nil && len(data) <= usageLimitProbeBytes && usageLimitJSON(data)
	}
	if resp.StatusCode != http.StatusOK || responseFormat(resp.Header.Get("Content-Type")) != "sse" {
		return false
	}
	source := resp.Body
	reader := bufio.NewReader(io.LimitReader(source, usageLimitProbeBytes+1))
	var consumed, frame bytes.Buffer
	defer func() {
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(consumed.Bytes()), reader, source), source}
	}()
	for consumed.Len() <= usageLimitProbeBytes {
		line, err := reader.ReadBytes('\n')
		consumed.Write(line)
		if err != nil || consumed.Len() > usageLimitProbeBytes {
			return false
		}
		line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})
		if len(line) > 0 {
			if bytes.HasPrefix(line, []byte("data:")) {
				frame.Write(bytes.TrimPrefix(line[5:], []byte(" ")))
				frame.WriteByte('\n')
			}
			continue
		}
		if frame.Len() == 0 {
			continue
		}
		var event struct {
			Type     string          `json:"type"`
			Error    json.RawMessage `json:"error"`
			Code     string          `json:"code"`
			Response struct {
				Error  json.RawMessage   `json:"error"`
				Output []json.RawMessage `json:"output"`
			} `json:"response"`
		}
		if json.Unmarshal(frame.Bytes(), &event) != nil {
			return false
		}
		switch event.Type {
		case "error":
			return usageLimitError(event.Error) || (event.Code == "usage_limit_reached" && (len(event.Error) == 0 || string(event.Error) == "null"))
		case "response.failed":
			return len(event.Response.Output) == 0 && usageLimitError(event.Response.Error)
		case "response.created", "response.in_progress":
			if len(event.Response.Output) > 0 || len(event.Response.Error) > 0 && string(event.Response.Error) != "null" {
				return false
			}
		default:
			return false
		}
		frame.Reset()
	}
	return false
}
