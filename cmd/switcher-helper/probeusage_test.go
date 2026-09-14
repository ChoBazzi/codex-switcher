package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

type probeUsageFetcher struct {
	calls   atomic.Int32
	release chan struct{}
}

func (f *probeUsageFetcher) Fetch(ctx context.Context, _ accounts.Access) (usage.Data, error) {
	f.calls.Add(1)
	select {
	case <-ctx.Done():
		return usage.Data{}, ctx.Err()
	case <-f.release:
	}
	allowed, reached, remaining := true, false, 80.0
	return usage.Data{Allowed: &allowed, LimitReached: &reached, RemainingPercent: &remaining}, nil
}

func TestProbeUsageSingleSource(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	fetcher := &probeUsageFetcher{release: make(chan struct{})}
	input, commands := io.Pipe()
	output, sink := io.Pipe()
	defer commands.Close()
	defer input.Close()
	defer output.Close()
	defer sink.Close()
	done := make(chan error, 1)
	go func() {
		done <- switchProbeWithUsage([]string{"--allow-live", "--managed", "--auto"}, input, sink, syntheticProbeAccess{}, "http://127.0.0.1:9", fetcher)
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
				t.Fatal("event timeout")
				return nil
			}
		}
	}
	next("probe_ready")
	next("probe_state")
	deadline := time.Now().Add(3 * time.Second)
	for fetcher.calls.Load() != 5 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fetcher.calls.Load() != 5 {
		t.Fatal("initial fetch missing")
	}
	io.WriteString(commands, "{\"action\":\"account_changing\",\"slot\":\"a\"}\n")
	next("usage_snapshot")
	close(fetcher.release)
	snapshot := next("usage_snapshot")
	items := snapshot["accounts"].([]any)
	if items[0].(map[string]any)["state"] != "unknown" || items[1].(map[string]any)["state"] != "ok" {
		t.Fatal("inflight result restored invalid account")
	}
	io.WriteString(commands, "{\"action\":\"account_changed\",\"slot\":\"a\"}\n")
	next("usage_snapshot")
	for i := 0; i < 5; i++ {
		io.WriteString(commands, "{\"action\":\"usage\"}\n")
		cached := next("usage_snapshot")["accounts"].([]any)
		if cached[0].(map[string]any)["usage"] != nil {
			t.Fatal("old account data restored")
		}
	}
	if fetcher.calls.Load() != 5 {
		t.Fatal("cache reads triggered upstream polls")
	}
	commands.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timeout")
	}
}
