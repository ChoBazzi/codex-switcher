package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func deletionCommand(t *testing.T, h *multiHarness, id, home string) map[string]any {
	t.Helper()
	h.send(t, map[string]any{"action": "status"})
	e := h.next(t, "probe_state", func(e map[string]any) bool { return e["connection_id"] == id })
	return map[string]any{"action": "connection_delete", "connection_id": id, "expected_home": home, "revision": e["revision"]}
}

func TestConnectionDeleteIsolationAndReuse(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	fetcher := &probeUsageFetcher{release: make(chan struct{})}
	close(fetcher.release)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "fail") {
			http.Error(w, "synthetic failure", 503)
			return
		}
		if strings.Contains(string(body), "hold") {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}]}}\n\n")
	}))
	defer up.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	h := startMulti(t, dir, up.URL, syntheticProbeAccess{}, fetcher, nil)
	home1 := h.next(t, "probe_ready", nil)["codex_home"].(string)
	h.send(t, map[string]any{"action": "usage_refresh", "request_id": 1})
	h.next(t, "usage_refresh", func(e map[string]any) bool { return e["status"] == "finished" })
	url1, key1, _ := checkpointProfile(t, home1)
	root1, root2 := "12345678-1234-4234-8234-123456789011", "12345678-1234-4234-8234-123456789022"
	if code, _ := multiRequest(url1, key1, root1, "fail"); code != 503 {
		t.Fatal("synthetic failure missing")
	}
	h.next(t, "probe_state", func(e map[string]any) bool { return e["failed"] == true && e["busy"] == false })
	// The archive includes user-owned files and must not be recursively removed.
	archive := filepath.Join(home1, "synthetic-history.jsonl")
	if os.WriteFile(archive, []byte("synthetic history\n"), 0600) != nil {
		t.Fatal("archive setup failed")
	}
	h.send(t, map[string]any{"action": "connection_create"})
	home2 := h.next(t, "probe_ready", nil)["codex_home"].(string)
	h.next(t, "connection_result", nil)
	url2, key2, _ := checkpointProfile(t, home2)
	result := make(chan int, 1)
	go func() { code, _ := multiRequest(url2, key2, root2, "hold"); result <- code }()
	<-entered
	h.next(t, "probe_state", func(e map[string]any) bool { return e["busy"] == true })
	h.send(t, deletionCommand(t, h, "2", home2))
	if h.next(t, "connection_result", nil)["accepted"] != false {
		t.Fatal("active connection deleted")
	}
	h.selectConnection(t, "1")
	stale := deletionCommand(t, h, "1", home1)
	bad := deletionCommand(t, h, "1", home1)
	bad["revision"] = float64(999999)
	h.send(t, bad)
	if h.next(t, "connection_result", nil)["accepted"] != false {
		t.Fatal("stale revision accepted")
	}
	h.send(t, stale)
	if h.next(t, "connection_result", nil)["accepted"] != true {
		t.Fatal("failed primary could not be removed beside active sibling")
	}
	if code, _ := multiRequest(url1, key1, root1, "old connection"); code != 0 {
		t.Fatal("deleted listener still reachable")
	}
	if content, err := os.ReadFile(archive); err != nil || string(content) != "synthetic history\n" {
		t.Fatal("history was changed")
	}
	c, err := readProbeCheckpoint(dir)
	if err != nil || c == nil || !c.Deleted {
		t.Fatal("deletion was not durable before acknowledgment")
	}
	close(release)
	if <-result != 200 {
		t.Fatal("sibling request interrupted")
	}
	h.next(t, "probe_state", func(e map[string]any) bool { return e["connection_id"] == "2" && e["busy"] == false })
	// Usage control follows the surviving primary, not hard-coded connection 1.
	h.send(t, map[string]any{"action": "usage_refresh", "request_id": 2})
	h.next(t, "usage_refresh", func(e map[string]any) bool { return e["status"] == "finished" && e["request_id"] == float64(2) })
	if code, _ := multiRequest(url2, key2, root2, "continue"); code != 200 {
		t.Fatal("surviving conversation changed")
	}
	h.next(t, "probe_state", func(e map[string]any) bool { return e["busy"] == false })
	h.shutdown(t)
	before := calls.Load()
	h = startMulti(t, dir, up.URL, syntheticProbeAccess{}, fetcher, nil)
	if e := h.next(t, "probe_ready", nil); e["connection_id"] != "2" || e["codex_home"] != home2 {
		t.Fatal("restart resurrected deleted primary")
	}
	h.next(t, "probe_state", nil)
	h.send(t, map[string]any{"action": "connection_create"})
	fresh := h.next(t, "probe_ready", func(e map[string]any) bool { return e["connection_id"] == "1" })["codex_home"].(string)
	h.next(t, "connection_result", nil)
	if fresh == home1 || calls.Load() != before {
		t.Fatal("slot reuse restored old history or made a model request")
	}
	_, freshKey, _ := checkpointProfile(t, fresh)
	if freshKey == key1 {
		t.Fatal("deleted capability reused")
	}
	h.send(t, stale)
	if h.next(t, "connection_result", nil)["accepted"] != false {
		t.Fatal("old confirmation deleted reused slot")
	}
	h.shutdown(t)
}

func TestConnectionDeleteLastAndToolGuard(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	fetcher := &probeUsageFetcher{release: make(chan struct{})}
	close(fetcher.release)
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"name\":\"read\",\"call_id\":\"synthetic\",\"arguments\":\"{}\"}]}}\n\n")
	}))
	defer up.Close()
	h := startMulti(t, dir, up.URL, syntheticProbeAccess{}, fetcher, nil)
	home := h.next(t, "probe_ready", nil)["codex_home"].(string)
	h.send(t, map[string]any{"action": "usage_refresh", "request_id": 1})
	h.next(t, "usage_refresh", func(e map[string]any) bool { return e["status"] == "finished" })
	url, key, _ := checkpointProfile(t, home)
	if code, _ := multiRequest(url, key, "12345678-1234-4234-8234-123456789011", "tool"); code != 200 {
		t.Fatal("tool setup failed")
	}
	h.next(t, "probe_state", func(e map[string]any) bool { return e["can_abandon_turn"] == true })
	command := deletionCommand(t, h, "1", home)
	h.send(t, command)
	if h.next(t, "connection_result", nil)["accepted"] != false {
		t.Fatal("pending tool deleted")
	}
	h.send(t, map[string]any{"action": "abandon_turn", "connection_id": "1", "slot": "a", "revision": command["revision"]})
	if h.next(t, "probe_abandonment", nil)["accepted"] != true {
		t.Fatal("explicit synthetic tool abandonment failed")
	}
	h.send(t, deletionCommand(t, h, "1", home))
	if h.next(t, "connection_result", nil)["accepted"] != true {
		t.Fatal("last failed session deletion rejected")
	}
	fresh := h.next(t, "probe_ready", nil)["codex_home"].(string)
	h.next(t, "connection_result", nil)
	if fresh == home || calls.Load() != 1 {
		t.Fatal("last session deletion reused history or replayed request")
	}
	h.shutdown(t)
}
