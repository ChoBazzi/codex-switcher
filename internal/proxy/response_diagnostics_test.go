//go:build darwin && cgo

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
)

func TestResponseDiagnostics(t *testing.T) {
	complete := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-final\",\"status\":\"completed\"}}\n\n"
	for _, tc := range []struct {
		name, body, code string
		seen, committed  bool
	}{
		{"success", complete, "", true, true},
		{"shape", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-secret\"}}\n\n", "completion_shape_invalid", true, false},
		{"failed", "data: {\"type\":\"response.failed\",\"error\":{\"message\":\"synthetic-secret\"}}\n\n", "upstream_response_failed", false, false},
		{"incomplete", "data: {\"type\":\"response.incomplete\"}\n\n", "upstream_response_incomplete", false, false},
		{"error", "data: {\"type\":\"error\",\"code\":\"synthetic-secret\"}\n\n", "upstream_error_event", false, false},
		{"missing", "data: {\"type\":\"response.created\"}\n\n", "completion_missing", false, false},
		{"duplicate", complete + complete, "duplicate_completion", true, false},
		{"trailing", complete + "data: unfinished", "stream_trailing_frame_incomplete", true, false},
		{"oversize", strings.Repeat("x", (1<<20)+1), "stream_buffer_exceeded", false, false},
		{"store", complete, "completion_store_conflict", true, false},
		{"json_shape", `{"error":{"message":"synthetic-secret"}}`, "completion_shape_invalid", false, false},
		{"json_success", `{"id":"synthetic-final","status":"completed"}`, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := affinity.Open(filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			o := persistentOrigin("synthetic")
			if _, err := db.Register(o, "b", time.Now()); err != nil {
				t.Fatal(err)
			}
			lease, err := db.Begin(o, nil, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "store" {
				lease.Request = "synthetic-invalid-lease"
			}
			h := persistentHandler(t, db, "http://127.0.0.1:1", "synthetic", "b")
			aborted := false
			func() {
				defer func() {
					if p := recover(); p != nil {
						if p != http.ErrAbortHandler {
							panic(p)
						}
						aborted = true
					}
				}()
				h.deliverPersistent(httptest.NewRecorder(), &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}, lease, !strings.HasPrefix(tc.name, "json_"))
			}()
			d := h.Diagnostics()
			if tc.name == "shape" && d.CompletionRejection != "completion_status_invalid" || tc.name == "json_shape" && d.CompletionRejection != "completion_error_present" {
				t.Fatalf("missing structural reason: %+v", d)
			}
			if d.ResponseFailure != tc.code || d.CompletionSeen != tc.seen || d.CompletionCommitted != tc.committed || aborted == tc.committed {
				t.Fatalf("unexpected diagnostic: %+v", d)
			}
			b, _ := json.Marshal(d)
			if strings.Contains(string(b), "synthetic-secret") {
				t.Fatal("response leaked")
			}
		})
	}
}

func TestResponseReadFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{context.Canceled, "response_read_canceled"},
		{context.DeadlineExceeded, "response_read_timeout"},
		{errors.New("synthetic-secret"), "response_read_failed"},
	} {
		if responseReadFailure(tc.err) != tc.code {
			t.Fatal("incorrect read diagnostic")
		}
	}
	if completionStoreFailure(errors.New("synthetic-secret")) != "completion_store_failed" {
		t.Fatal("storage error exposed")
	}
}

func TestDispatchHookRejectsWithoutAttempt(t *testing.T) {
	db, err := affinity.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	o := persistentOrigin("synthetic")
	if _, err := db.Register(o, "a", time.Now()); err != nil {
		t.Fatal(err)
	}
	h := persistentHandler(t, db, "http://127.0.0.1:1", "synthetic", "a")
	defer h.Close()
	calls := 0
	h.BeforeAttempt = func() error { calls++; return errors.New("synthetic-secret") }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":"synthetic"}`)))
	d := h.Diagnostics()
	if calls != 1 || d.Attempts != 0 || w.Code != 409 || d.Rejection != "request_dispatch_rejected" || db.Ready(o) == nil {
		t.Fatalf("dispatch rejection bypassed: %+v", d)
	}
	if strings.Contains(w.Body.String(), "synthetic-secret") {
		t.Fatal("hook error leaked")
	}
}
