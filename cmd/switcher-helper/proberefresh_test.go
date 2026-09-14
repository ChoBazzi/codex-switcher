package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

type refreshGateFetcher struct {
	calls   atomic.Int32
	fail    atomic.Bool
	release chan struct{}
}

func (f *refreshGateFetcher) Fetch(ctx context.Context, _ accounts.Access) (usage.Data, error) {
	f.calls.Add(1)
	select {
	case <-ctx.Done():
		return usage.Data{}, ctx.Err()
	case <-f.release:
	}
	if f.fail.Load() {
		return usage.Data{}, errors.New("synthetic_usage_failure")
	}
	remaining := 70.0
	return usage.Data{RemainingPercent: &remaining}, nil
}

func TestProbeManualUsageRefresh(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := &refreshGateFetcher{release: make(chan struct{}, 5)}
	input, commands := io.Pipe()
	output, sink := io.Pipe()
	t.Cleanup(func() { commands.Close(); input.Close(); output.Close(); sink.Close() })
	done := make(chan error, 1)
	go func() {
		done <- switchProbeWithUsageTiming([]string{"--allow-live", "--managed", "--auto"}, input, sink, syntheticProbeAccess{}, "http://127.0.0.1:9", f, 200*time.Millisecond, 30*time.Millisecond)
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
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case e := <-events:
				if e["event"] == kind {
					return e
				}
			case <-timer.C:
				t.Fatal("refresh event timeout")
				return nil
			}
		}
	}
	waitCalls := func(n int32) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for f.calls.Load() < n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if f.calls.Load() != n {
			t.Fatalf("want %d synthetic fetch calls, got %d", n, f.calls.Load())
		}
	}
	request := func(id int, want string) {
		t.Helper()
		fmt.Fprintf(commands, "{\"action\":\"usage_refresh\",\"request_id\":%d}\n", id)
		e := next("usage_refresh")
		if e["request_id"] != float64(id) || e["status"] != want {
			t.Fatal("unexpected refresh acknowledgment")
		}
	}
	release := func() {
		for i := 0; i < 5; i++ {
			f.release <- struct{}{}
		}
	}
	next("probe_ready")
	waitCalls(5)
	request(1, "started") // Join the in-flight automatic round.
	request(2, "busy")
	fmt.Fprintln(commands, `{"action":"usage"}`)
	next("usage_snapshot") // A cache read must not complete or duplicate the round.
	waitCalls(5)
	release()
	next("usage_snapshot")
	e := next("usage_refresh")
	if e["status"] != "finished" || e["request_id"] != float64(1) || e["succeeded"] != true {
		t.Fatal("joined round did not complete")
	}
	request(3, "cooldown")
	time.Sleep(50 * time.Millisecond)
	f.fail.Store(true)
	request(4, "started")
	waitCalls(10)
	time.Sleep(250 * time.Millisecond) // Cross the old automatic deadline while HTTP is pending.
	waitCalls(10)
	release()
	snapshot := next("usage_snapshot")
	for _, raw := range snapshot["accounts"].([]any) {
		a := raw.(map[string]any)
		if a["state"] != "fetch_error" || a["stale"] != true || a["usage"] == nil || a["last_success"] == nil {
			t.Fatal("failure did not retain stale usage")
		}
	}
	e = next("usage_refresh")
	if e["request_id"] != float64(4) || e["status"] != "finished" || e["succeeded"] != false {
		t.Fatal("failed fetch reported success")
	}
	time.Sleep(100 * time.Millisecond)
	waitCalls(10) // No catch-up tick or failure retry immediately after completion.
	waitCalls(15) // Next automatic round starts one full interval after completion.
	commands.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("refresh shutdown timeout")
	}
}
