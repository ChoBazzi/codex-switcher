//go:build darwin && cgo

package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
)

type completionWriter struct {
	*httptest.ResponseRecorder
	check func()
}

func (w completionWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "response.completed") {
		w.check()
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}

func TestCompletionCommittedBeforeClientDisconnect(t *testing.T) {
	db, err := affinity.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	origin := persistentOrigin("one")
	if _, err := db.Register(origin, "a", time.Now()); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-final\",\"status\":\"completed\"}}\n\n")
		w.(http.Flusher).Flush()
		// Completion and EOF deliberately arrive separately.
		time.Sleep(25 * time.Millisecond)
	}))
	defer up.Close()
	h := persistentHandler(t, db, up.URL, "one", "a")
	observed := false
	w := completionWriter{httptest.NewRecorder(), func() {
		observed = true
		if db.Ready(origin) != nil {
			t.Error("completion reached CLI before commit")
		}
	}}
	h.ServeHTTP(w, httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":"synthetic"}`)))
	if !observed || db.Ready(origin) != nil {
		t.Fatal("completed response was blocked by client close")
	}
}

func TestPersistentMissingContentType(t *testing.T) {
	for _, tc := range []struct {
		name, body, format string
		success            bool
	}{
		{"data", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-final\",\"status\":\"completed\"}}\n\n", "missing_sse", true},
		{"event", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-final\",\"status\":\"completed\"}}\n\n", "missing_sse", true},
		{"comment", ": keepalive\n\ndata: {\"type\":\"error\"}\n\n", "missing_sse", false},
		{"partial", "data: {\"type\":\"response.created\"}\n\n", "missing_sse", false},
		{"html", "<html>synthetic</html>", "missing", false},
		{"json", `{"id":"synthetic-final","status":"completed"}`, "missing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := affinity.Open(filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			o := persistentOrigin("one")
			if _, err := db.Register(o, "a", time.Now()); err != nil {
				t.Fatal(err)
			}
			var attempts atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header()["Content-Type"] = nil // Suppress net/http auto-sniffing.
				fmt.Fprint(w, tc.body)
			}))
			defer up.Close()
			h := persistentHandler(t, db, up.URL, "one", "a")
			w := httptest.NewRecorder()
			func() {
				defer func() {
					if p := recover(); p != nil && p != http.ErrAbortHandler {
						panic(p)
					}
				}()
				h.ServeHTTP(w, httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":"synthetic"}`)))
			}()
			d := h.Diagnostics()
			if d.ResponseFormat != tc.format || d.CompletionCommitted != tc.success || (db.Ready(o) == nil) != tc.success || attempts.Load() != 1 {
				t.Fatalf("unexpected result: %+v attempts=%d", d, attempts.Load())
			}
			if tc.success && w.Body.String() != tc.body {
				t.Fatal("body changed")
			}
		})
	}
}
