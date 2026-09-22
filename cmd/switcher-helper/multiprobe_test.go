package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

type multiHarness struct {
	input  *io.PipeWriter
	events chan map[string]any
	done   chan error
}

func startMulti(t *testing.T, dir, upstream string, access probeAccess, fetcher usage.Fetcher, guard func() (io.Closer, error)) *multiHarness {
	t.Helper()
	in, commands := io.Pipe()
	out, sink := io.Pipe()
	h := &multiHarness{commands, make(chan map[string]any, 512), make(chan error, 1)}
	go func() {
		h.done <- multiProbe(in, sink, dir, access, upstream, fetcher, time.Hour, 0, guard)
		in.Close()
		sink.Close()
	}()
	go func() {
		defer out.Close()
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			var e map[string]any
			if json.Unmarshal(scanner.Bytes(), &e) == nil {
				h.events <- e
			}
		}
		close(h.events)
	}()
	t.Cleanup(func() { commands.Close() })
	return h
}
func (h *multiHarness) send(t *testing.T, command map[string]any) {
	t.Helper()
	if json.NewEncoder(h.input).Encode(command) != nil {
		t.Fatal("control disconnected")
	}
}
func (h *multiHarness) next(t *testing.T, kind string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-h.events:
			if !ok {
				t.Fatal("multi service ended")
			}
			if e["event"] == kind && (match == nil || match(e)) {
				return e
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for %s", kind)
		}
	}
}
func (h *multiHarness) selectConnection(t *testing.T, id string) string {
	t.Helper()
	h.send(t, map[string]any{"action": "connection_select", "connection_id": id})
	home := h.next(t, "probe_ready", func(e map[string]any) bool { return e["connection_id"] == id })["codex_home"].(string)
	h.next(t, "connection_result", nil)
	return home
}
func (h *multiHarness) shutdown(t *testing.T) {
	t.Helper()
	h.send(t, map[string]any{"action": "shutdown"})
	if h.next(t, "probe_shutdown", nil)["accepted"] != true {
		t.Fatal("idle shutdown rejected")
	}
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown stuck")
	}
}
func multiRequest(url, secret, root, text string) (int, error) {
	body, _ := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "user", "content": text}}})
	r, _ := http.NewRequest("POST", url+"/responses", strings.NewReader(string(body)))
	r.Header.Set("X-Switcher-Run", secret)
	r.Header.Set("Thread-Id", root)
	r.Header.Set("Session-Id", root)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	return response.StatusCode, nil
}

type multiSyntheticAccess struct {
	syntheticProbeAccess
	turns sharedTurns
}

func (a *multiSyntheticAccess) BeginTurn() (io.Closer, error) { return a.turns.begin() }

func TestMultiProbeConcurrentIsolationAndRestart(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	activityDir := t.TempDir()
	os.Chmod(activityDir, 0700)
	acquire := func() (io.Closer, error) { return accounts.AcquireActivity(activityDir) }
	access := &multiSyntheticAccess{turns: sharedTurns{acquire: acquire}}
	fetcher := &probeUsageFetcher{release: make(chan struct{})}
	close(fetcher.release)
	entered := make(chan string, 4)
	release := make(chan struct{})
	releaseSecond := make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "hold") {
			gate := release
			if strings.Contains(string(body), "again") {
				gate = releaseSecond
			}
			entered <- r.Header.Get("Authorization")
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if strings.Contains(string(body), "fail") {
			http.Error(w, "synthetic failure", 429)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[]}]}}\n\n")
	}))
	defer upstream.Close()
	h := startMulti(t, dir, upstream.URL, access, fetcher, acquire)
	home1 := h.next(t, "probe_ready", nil)["codex_home"].(string)
	h.send(t, map[string]any{"action": "usage"})
	h.next(t, "usage_snapshot", nil)
	h.send(t, map[string]any{"action": "connection_create"})
	home2 := h.next(t, "probe_ready", func(e map[string]any) bool { return e["connection_id"] == "2" })["codex_home"].(string)
	state2 := h.next(t, "probe_state", func(e map[string]any) bool { return e["connection_id"] == "2" })
	h.next(t, "connection_result", nil)
	if home1 == home2 {
		t.Fatal("shared CLI home")
	}
	h.send(t, map[string]any{"action": "select", "connection_id": "2", "slot": "b", "revision": state2["revision"]})
	if h.next(t, "probe_selection", nil)["accepted"] != true {
		t.Fatal("second account selection failed")
	}
	url1, key1, _ := checkpointProfile(t, home1)
	url2, key2, _ := checkpointProfile(t, home2)
	if url1 == url2 || key1 == key2 {
		t.Fatal("shared capability")
	}
	root1 := "12345678-1234-4234-8234-123456789011"
	root2 := "12345678-1234-4234-8234-123456789022"
	results := make(chan int, 2)
	go func() { code, _ := multiRequest(url1, key1, root1, "hold one"); results <- code }()
	go func() { code, _ := multiRequest(url2, key2, root2, "hold two"); results <- code }()
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case auth := <-entered:
			seen[auth] = true
		case <-time.After(3 * time.Second):
			t.Fatal("connections did not run concurrently")
		}
	}
	if !seen["Bearer synthetic-a"] || !seen["Bearer synthetic-b"] {
		t.Fatal("per-connection account lost")
	}
	if lock, err := acquire(); err == nil {
		lock.Close()
		t.Fatal("account mutation allowed during turns")
	}
	h.send(t, map[string]any{"action": "shutdown"})
	if h.next(t, "probe_shutdown", nil)["accepted"] != false {
		t.Fatal("busy connection shutdown accepted")
	}
	close(release)
	for i := 0; i < 2; i++ {
		if <-results != 200 {
			t.Fatal("concurrent model request failed")
		}
	}
	h.next(t, "probe_state", func(e map[string]any) bool { return e["connection_id"] == "2" && e["busy"] == false })
	// Only connection 2 is active. An idle selected connection must survive
	// rejected global shutdown and continue admitting its own work.
	h.selectConnection(t, "1")
	go func() { code, _ := multiRequest(url2, key2, root2, "hold again"); results <- code }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("second request did not start")
	}
	h.send(t, map[string]any{"action": "shutdown"})
	if h.next(t, "probe_shutdown", nil)["accepted"] != false {
		t.Fatal("unselected activity lost")
	}
	h.send(t, map[string]any{"action": "status"})
	h.next(t, "probe_state", func(e map[string]any) bool { return e["connection_id"] == "1" })
	// Prepared connections were unfrozen, and neither connection owns the other root.
	if code, _ := multiRequest(url1, key1, root2, "wrong root"); code != 409 {
		t.Fatal("cross-connection root accepted")
	}
	if code, _ := multiRequest(url1, key2, root1, "wrong capability"); code != 401 {
		t.Fatal("cross-connection capability accepted")
	}
	if code, _ := multiRequest(url1, key1, root1, "fail one"); code != 429 {
		t.Fatal("failure missing")
	}
	count := calls.Load()
	if code, _ := multiRequest(url1, key1, root1, "fail one"); code != 409 || calls.Load() != count {
		t.Fatal("failed request replayed")
	}
	close(releaseSecond)
	if <-results != 200 {
		t.Fatal("rejected shutdown interrupted the active connection")
	}
	h.selectConnection(t, "2")
	if code, _ := multiRequest(url2, key2, root2, "two remains healthy"); code != 200 {
		t.Fatal("failure leaked to second root")
	}
	h.next(t, "probe_state", func(e map[string]any) bool { return e["connection_id"] == "2" && e["busy"] == false })
	if fetcher.calls.Load() != 5 {
		t.Fatal("multiple quota pollers")
	}
	if lock, err := acquire(); err != nil {
		t.Fatal("activity lease leaked")
	} else {
		lock.Close()
	}
	// Invalid explicit targets never fall back to the original connection.
	for _, target := range []any{"../1", "", 2, nil} {
		h.send(t, map[string]any{"action": "connection_select", "connection_id": target})
		if h.next(t, "connection_result", nil)["accepted"] != false {
			t.Fatal("invalid target accepted")
		}
	}
	h.shutdown(t)
	before := calls.Load()
	restored := startMulti(t, dir, upstream.URL, access, fetcher, acquire)
	restoredHome1 := restored.next(t, "probe_ready", nil)["codex_home"].(string)
	restored.next(t, "connection_list", func(e map[string]any) bool {
		rows := e["connections"].([]any)
		return len(rows) == 2 && rows[0].(map[string]any)["ready"] == true && rows[1].(map[string]any)["ready"] == true
	})
	restoredHome2 := restored.selectConnection(t, "2")
	if restoredHome1 != home1 || restoredHome2 != home2 || calls.Load() != before {
		t.Fatal("restart changed home or replayed model request")
	}
	restoredURL, key, _ := checkpointProfile(t, restoredHome2)
	if restoredURL != url2 || key != key2 {
		t.Fatal("restart lost address or capability")
	}
	restored.send(t, map[string]any{"action": "shutdown", "new_session": true, "connection_id": "2"})
	if restored.next(t, "probe_shutdown", nil)["accepted"] != true {
		t.Fatal("targeted retirement rejected")
	}
	if err := <-restored.done; err != nil {
		t.Fatal(err)
	}
	first, err := readProbeCheckpoint(dir)
	if err != nil || first == nil || first.Home != home1 {
		t.Fatal("unrelated connection retired")
	}
	second, err := readProbeCheckpoint(connectionDirectory(dir, "2"))
	if err != nil || second != nil {
		t.Fatal("target connection not retired")
	}
	if _, err := os.Stat(home2); err != nil {
		t.Fatal("retirement deleted CLI history")
	}
}

func TestMultiProbeCapacityAndQuotaInvalidation(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	fetcher := &probeUsageFetcher{release: make(chan struct{})}
	close(fetcher.release)
	h := startMulti(t, dir, "http://127.0.0.1:9", syntheticProbeAccess{}, fetcher, nil)
	h.next(t, "probe_ready", nil)
	h.send(t, map[string]any{"action": "usage"})
	h.next(t, "usage_snapshot", nil)
	var home string
	for i := 2; i <= 5; i++ {
		h.send(t, map[string]any{"action": "connection_create"})
		home = h.next(t, "probe_ready", nil)["codex_home"].(string)
		h.next(t, "connection_result", nil)
	}
	h.send(t, map[string]any{"action": "connection_create"})
	if h.next(t, "connection_result", nil)["accepted"] != false {
		t.Fatal("connection limit exceeded")
	}
	// Invalidate all accounts in the primary. A secondary sees the same state
	// synchronously and blocks before touching the unreachable model endpoint.
	for _, slot := range []string{"a", "b", "c", "d", "e"} {
		h.send(t, map[string]any{"action": "account_changing", "slot": slot})
		h.next(t, "usage_snapshot", nil)
	}
	url, key, _ := checkpointProfile(t, home)
	code, err := multiRequest(url, key, "12345678-1234-4234-8234-123456789055", "new request")
	if err != nil || code != 409 {
		t.Fatalf("invalidated quota used: %d %v", code, err)
	}
	if fetcher.calls.Load() != 5 {
		t.Fatal("creation triggered quota fetch")
	}
	h.shutdown(t)
}

func TestSharedTurnsReferenceCount(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	acquire := func() (io.Closer, error) { return accounts.AcquireActivity(dir) }
	turns := sharedTurns{acquire: acquire}
	first, err := turns.begin()
	if err != nil {
		t.Fatal(err)
	}
	second, err := turns.begin()
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	first.Close()
	if lock, err := acquire(); err == nil {
		lock.Close()
		t.Fatal("released while second active")
	}
	second.Close()
	lock, err := acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = turns.begin(); err == nil {
		t.Fatal("mutation lease bypassed")
	}
	lock.Close()
	if third, err := turns.begin(); err != nil {
		t.Fatal(err)
	} else {
		third.Close()
	}
}

func TestMultiProbeUnsafeDirectory(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	other := t.TempDir()
	if os.Symlink(other, connectionDirectory(dir, "2")) != nil {
		t.Fatal("symlink failed")
	}
	fetcher := &probeUsageFetcher{release: make(chan struct{})}
	close(fetcher.release)
	h := startMulti(t, dir, "http://127.0.0.1:9", syntheticProbeAccess{}, fetcher, nil)
	select {
	case err := <-h.done:
		if err == nil {
			t.Fatal("unsafe directory accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unsafe startup hung")
	}
}

func TestMultiProbeAccountMutationBlocksShutdown(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	activity := t.TempDir()
	os.Chmod(activity, 0700)
	acquire := func() (io.Closer, error) { return accounts.AcquireActivity(activity) }
	fetcher := &probeUsageFetcher{release: make(chan struct{})}
	close(fetcher.release)
	h := startMulti(t, dir, "http://127.0.0.1:9", syntheticProbeAccess{}, fetcher, acquire)
	h.next(t, "probe_ready", nil)
	mutation, err := acquire()
	if err != nil {
		t.Fatal(err)
	}
	h.send(t, map[string]any{"action": "shutdown"})
	if h.next(t, "probe_shutdown", nil)["accepted"] != false {
		t.Fatal("shutdown overlapped account mutation")
	}
	mutation.Close()
	h.shutdown(t)
}
