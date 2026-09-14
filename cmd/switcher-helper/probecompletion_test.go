package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeCompletionOverlap(t *testing.T) {
	for _, mode := range []string{"success", "cancel_first", "trailing_failure", "duplicate", "cancel_waiter"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			release := make(chan struct{})
			var attempts atomic.Int32
			terminal := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[]}]}}\n\n"
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := attempts.Add(1)
				defer r.Body.Close()
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
				http.NewResponseController(w).Flush()
				io.WriteString(w, terminal)
				http.NewResponseController(w).Flush()
				if n == 1 {
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				if n == 1 && mode == "trailing_failure" {
					io.WriteString(w, "data: {\"type\":\"response.failed\"}\n\n")
				}
			}))
			defer up.Close()
			defer close(release)
			input, commands := io.Pipe()
			output, sink := io.Pipe()
			defer input.Close()
			defer commands.Close()
			defer output.Close()
			defer sink.Close()
			done := make(chan error, 1)
			go func() {
				done <- switchProbeWithAccess([]string{"--allow-live", "--managed", "--tools"}, input, sink, syntheticProbeAccess{}, up.URL)
			}()
			events := make(chan map[string]any, 64)
			go func() {
				s := bufio.NewScanner(output)
				for s.Scan() {
					var e map[string]any
					if json.Unmarshal(s.Bytes(), &e) == nil {
						events <- e
					}
				}
			}()
			next := func(kind string) map[string]any {
				t.Helper()
				timer := time.NewTimer(3 * time.Second)
				defer timer.Stop()
				for {
					select {
					case e := <-events:
						if e["event"] == kind {
							return e
						}
					case <-timer.C:
						t.Fatal("event timeout: " + kind)
						return nil
					}
				}
			}
			home := next("probe_ready")["codex_home"].(string)
			next("probe_state")
			profile, err := os.ReadFile(filepath.Join(home, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			extract := func(key string) string {
				m := regexp.MustCompile(`(?m)^` + key + ` = "([^"]+)"`).FindSubmatch(profile)
				if len(m) != 2 {
					t.Fatal("profile field missing")
				}
				return string(m[1])
			}
			endpoint, secret := extract("base_url"), extract("X-Switcher-Run")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := func(ctx context.Context, text string) *http.Request {
				req, _ := http.NewRequestWithContext(ctx, "POST", endpoint+"/responses", strings.NewReader(`{"input":[{"role":"user","content":"`+text+`"}]}`))
				req.Header.Set("X-Switcher-Run", secret)
				req.Header.Set("Thread-Id", "12345678-1234-4234-8234-123456789012")
				req.Header.Set("Session-Id", "12345678-1234-4234-8234-123456789012")
				return req
			}
			client := &http.Client{Timeout: 5 * time.Second}
			first, err := client.Do(request(ctx, "first"))
			if err != nil {
				t.Fatal(err)
			}
			defer first.Body.Close()
			for {
				if next("probe_state")["completion_pending"] == true {
					break
				}
			}
			// Terminal is withheld: only the created frame has been forwarded so far.
			scanner := bufio.NewScanner(first.Body)
			if !scanner.Scan() || !strings.Contains(scanner.Text(), "response.created") {
				t.Fatal("prefix lost")
			}
			followCtx, stopWaiter := context.WithCancel(context.Background())
			defer stopWaiter()
			type result struct {
				resp *http.Response
				err  error
			}
			response := make(chan result, 1)
			text := "next"
			if mode == "duplicate" {
				text = "first"
			}
			go func() { r, e := client.Do(request(followCtx, text)); response <- result{r, e} }()
			next("probe_request_waiting")
			if attempts.Load() != 1 {
				t.Fatal("followup dispatched before first EOF")
			}
			if mode == "cancel_first" {
				cancel()
				first.Body.Close()
			} else {
				if mode == "cancel_waiter" {
					stopWaiter()
				}
				release <- struct{}{}
			}
			var got result
			select {
			case got = <-response:
			case <-time.After(3 * time.Second):
				t.Fatal("followup timeout")
			}
			if mode == "cancel_waiter" {
				if got.err == nil {
					got.resp.Body.Close()
					t.Fatal("canceled waiter sent")
				}
			} else {
				if got.err != nil {
					t.Fatal(got.err)
				}
				b, _ := io.ReadAll(got.resp.Body)
				got.resp.Body.Close()
				want := 200
				code := ""
				if mode == "cancel_first" || mode == "trailing_failure" {
					want = 409
					code = "probe_previous_request_failed"
				}
				if mode == "duplicate" {
					want = 409
					code = "probe_duplicate_followup"
				}
				if got.resp.StatusCode != want || !strings.Contains(string(b), code) {
					t.Fatalf("unexpected result %d", got.resp.StatusCode)
				}
			}
			io.Copy(io.Discard, first.Body)
			first.Body.Close()
			diagnostic := next("probe_request_finished")
			if mode == "cancel_first" && diagnostic["response_failure_code"] != "probe_client_canceled" {
				t.Fatal("cancel diagnostic missing")
			}
			if mode == "trailing_failure" && diagnostic["response_failure_code"] != "probe_tool_response_unsupported" {
				t.Fatal("trailing failure diagnostic missing")
			}
			expected := int32(1)
			if mode == "success" {
				expected = 2
			}
			if attempts.Load() != expected {
				t.Fatal("unexpected upstream attempts")
			}
			if mode == "cancel_first" || mode == "trailing_failure" {
				state := next("probe_state")
				if state["failed"] != true {
					t.Fatal("expected failed state")
				}
				command, _ := json.Marshal(map[string]any{"action": "select", "slot": "b", "revision": state["revision"]})
				commands.Write(append(command, '\n'))
				ack := next("probe_selection")
				if ack["accepted"] != true || ack["slot"] != "b" || ack["failed"] != false {
					t.Fatal("explicit recovery rejected")
				}
				if attempts.Load() != 1 {
					t.Fatal("selection replayed model request")
				}
				replay, e := client.Do(request(context.Background(), "first"))
				if e != nil {
					t.Fatal(e)
				}
				body, _ := io.ReadAll(replay.Body)
				replay.Body.Close()
				if replay.StatusCode != 409 || !strings.Contains(string(body), "probe_recovery_requires_new_input") || attempts.Load() != 1 {
					t.Fatal("old input replayed")
				}
				fresh, e := client.Do(request(context.Background(), "new explicit instruction"))
				if e != nil {
					t.Fatal(e)
				}
				io.Copy(io.Discard, fresh.Body)
				fresh.Body.Close()
				if fresh.StatusCode != 200 || attempts.Load() != 2 {
					t.Fatal("new input recovery failed")
				}
			}
			commands.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown timeout")
			}
		})
	}
}

func TestProbeTerminalGate(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &probeTurnWriter{ResponseWriter: rec, holdTerminal: true}
	prefix := "data: {\"type\":\"response.created\"}\r\n\r\n"
	terminal := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\r\n\r\n"
	for _, b := range []byte(prefix + terminal) {
		if _, err := w.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if rec.Body.String() != prefix || !w.valid() {
		t.Fatal("terminal released early")
	}
	if err := w.release(); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != prefix+terminal {
		t.Fatal("terminal bytes changed")
	}
}
