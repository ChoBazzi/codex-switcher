package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

type recoveryQuotaFetcher struct{ low atomic.Bool }

func (f *recoveryQuotaFetcher) Fetch(_ context.Context, a accounts.Access) (usage.Data, error) {
	r := 0.0
	if a.Token == "synthetic-a" {
		r = 80
		if f.low.Load() {
			r = 4
		}
	} else if a.Token == "synthetic-b" {
		r = 60
	}
	allowed, reached := r > 0, r == 0
	return usage.Data{RemainingPercent: &r, Allowed: &allowed, LimitReached: &reached}, nil
}

// Exercises real HTTP admission, quota changes and checkpoint restoration, not
// just the selector. No real credentials, CLI, or model calls are used.
func TestProbeQuotaFailureRecovery(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("upstream_failure_", fail), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			fetcher := &recoveryQuotaFetcher{}
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				io.Copy(io.Discard, r.Body)
				if n == 1 && fail {
					w.WriteHeader(429)
					return
				}
				if n > 1 && r.Header.Get("Authorization") != "Bearer synthetic-b" {
					t.Error("did not switch to eligible account")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic_%d\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[]}]}}\n\n", n)
			}))
			defer up.Close()
			start := func() (io.WriteCloser, func(string) map[string]any, func()) {
				input, commands := io.Pipe()
				output, sink := io.Pipe()
				done := make(chan error, 1)
				go func() {
					done <- switchProbeWithCheckpoint([]string{"--allow-live", "--managed", "--auto", "--tools"}, input, sink, syntheticProbeAccess{}, up.URL, fetcher, time.Hour, 0, dir)
				}()
				events := make(chan map[string]any, 128)
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
					for {
						select {
						case e := <-events:
							if e["event"] == kind {
								return e
							}
						case <-time.After(5 * time.Second):
							t.Fatal("event timeout: " + kind)
							return nil
						}
					}
				}
				stopped := false
				stop := func() {
					if stopped {
						return
					}
					stopped = true
					commands.Close()
					select {
					case err := <-done:
						if err != nil {
							t.Error(err)
						}
					case <-time.After(5 * time.Second):
						t.Error("shutdown timeout")
					}
					input.Close()
					sink.Close()
					output.Close()
				}
				t.Cleanup(stop)
				return commands, next, stop
			}
			commands, next, stop := start()
			home := next("probe_ready")["codex_home"].(string)
			// Explicit refresh ACK also covers an initial poll published before ready.
			io.WriteString(commands, "{\"action\":\"usage_refresh\",\"request_id\":1}\n")
			for next("usage_refresh")["status"] != "finished" {
			}
			url, secret, _ := checkpointProfile(t, home)
			body := `{"model":"synthetic","input":[{"role":"user","content":"first"}]}`
			want := 200
			if fail {
				want = 429
			}
			if got := checkpointRequest(t, url, secret, body); got != want {
				t.Fatalf("first status %d", got)
			}
			next("probe_request_finished")
			fetcher.low.Store(true)
			if fail {
				stop()
				commands, next, _ = start()
				if next("probe_ready")["codex_home"] != home {
					t.Fatal("CLI home changed")
				}
			}
			io.WriteString(commands, "{\"action\":\"usage_refresh\",\"request_id\":2}\n")
			for next("usage_refresh")["status"] != "finished" {
			}
			if fail {
				if checkpointRequest(t, url, secret, body) != 409 {
					t.Fatal("failure not preserved")
				}
				io.WriteString(commands, "{\"action\":\"status\"}\n")
				state := next("probe_state")
				for state["failed"] != true {
					state = next("probe_state")
				}
				fmt.Fprintf(commands, "{\"action\":\"recover\",\"revision\":%v}\n", state["revision"].(float64)+1)
				if ack := next("probe_selection"); ack["accepted"] != false || ack["failed"] != true || calls.Load() != 1 {
					t.Fatal("stale recovery command changed failed state")
				}
				fmt.Fprintf(commands, "{\"action\":\"recover\",\"revision\":%v}\n", state["revision"])
				ack := next("probe_selection")
				if ack["accepted"] != true || ack["slot"] != "b" || calls.Load() != 1 {
					t.Fatal("recovery must prepare B without replay")
				}
				if checkpointRequest(t, url, secret, body) != 409 || calls.Load() != 1 {
					t.Fatal("old input replayed")
				}
				next("probe_request_finished")
			}
			fresh := `{"model":"synthetic","input":[{"role":"user","content":"first"},{"role":"user","content":"inspect current state and continue"}]}`
			if got := checkpointRequest(t, url, secret, fresh); got != 200 {
				t.Fatalf("new input status %d", got)
			}
			next("probe_request_finished")
			if calls.Load() != 2 {
				t.Fatal("unexpected retry")
			}
		})
	}
}
