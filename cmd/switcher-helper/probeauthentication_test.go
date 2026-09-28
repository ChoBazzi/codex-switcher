package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
)

type cancellationObservedAccess struct {
	*refreshHistorySource
	canceled chan struct{}
}

func (s *cancellationObservedAccess) RequestAccessContext(ctx context.Context, slot string, now time.Time) (accounts.Access, error) {
	stop := context.AfterFunc(ctx, func() { close(s.canceled) })
	defer stop()
	return s.refreshHistorySource.RequestAccessContext(ctx, slot, now)
}

func TestProbeCanceledRefreshProgress(t *testing.T) {
	source := &cancellationObservedAccess{newRefreshHistorySource(t), make(chan struct{})}
	source.exchange.entered, source.exchange.release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(source.exchange.release) }) }
	defer unblock()
	var calls atomic.Int32
	up := historyUpstream(&calls)
	defer up.Close()
	p := startHistoryProbe(t, source, up.URL, t.TempDir())
	source.elapsed.Store(int64(2 * time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", p.address+"/responses", strings.NewReader(historyFirst))
	r.Header.Set("Thread-Id", "12345678-1234-4234-8234-123456789012")
	r.Header.Set("Session-Id", "12345678-1234-4234-8234-123456789012")
	r.Header.Set("X-Switcher-Run", p.secret)
	done := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-source.exchange.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("exchange did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("client did not cancel")
	}
	select {
	case <-source.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not observe cancellation")
	}
	if _, err := io.WriteString(p.commands, "{\"action\":\"shutdown\"}\n"); err != nil {
		t.Fatal(err)
	}
	state := p.next(t, "probe_shutdown")
	encoded, _ := json.Marshal(state["authentication"])
	var activity []accounts.AuthenticationStatus
	if json.Unmarshal(encoded, &activity) != nil || len(activity) != 1 || activity[0] != (accounts.AuthenticationStatus{Slot: "a", Refreshing: true, Canceled: true}) || state["accepted"] != false || state["busy"] != true {
		t.Fatal("canceled refresh not observable during safe completion")
	}
	unblock()
	p.next(t, "probe_request_finished")
	for {
		state = p.next(t, "probe_state")
		if state["busy"] == false {
			break
		}
	}
	encoded, _ = json.Marshal(state["authentication"])
	if string(encoded) != "[]" || calls.Load() != 0 || source.exchange.calls.Load() != 1 {
		t.Fatal("completion left stale progress or dispatched a canceled model request")
	}
}
