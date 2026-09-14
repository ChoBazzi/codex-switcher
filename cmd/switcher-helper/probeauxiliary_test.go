package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAuxiliaryFailureIsolationAndAccountPinning(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-a" {
			t.Error("auxiliary account changed")
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "synthetic-fail") {
			http.Error(w, "synthetic failure", 429)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[]}]}}\n\n")
	}))
	defer up.Close()
	var mu sync.Mutex
	makeAux := func(id string) *probeAuxiliary {
		return &probeAuxiliary{binding: probeAuxiliaryBinding{Thread: id, Root: "synthetic-root", Slot: "a"}}
	}
	first, second := makeAux("synthetic-child-1"), makeAux("synthetic-child-2")
	defer func() {
		for _, a := range []*probeAuxiliary{first, second} {
			if a.handler != nil {
				a.handler.Close()
			}
		}
	}()
	send := func(a *probeAuxiliary, text string) int {
		body := `{"input":[{"role":"user","content":"` + text + `"}]}`
		r := httptest.NewRequest("POST", "/responses", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		a.serve(w, r, &mu, syntheticProbeAccess{}, up.URL, "synthetic-salt", func() bool { return true }, nil)
		return w.Code
	}
	if send(first, "synthetic-fail") != 429 || calls.Load() != 1 {
		t.Fatal("initial failure not propagated")
	}
	if send(first, "synthetic-fail") != 409 || calls.Load() != 1 {
		t.Fatal("failed child was retried")
	}
	if send(first, "new input") != 409 || calls.Load() != 1 {
		t.Fatal("failed child silently recovered")
	}
	if send(second, "synthetic-success") != 200 || calls.Load() != 2 {
		t.Fatal("failure leaked to sibling")
	}
	if !first.failed || second.failed || second.busy || second.pending {
		t.Fatal("child state isolation failed")
	}
}

func TestAuxiliaryCheckpointFailurePreventsDispatch(t *testing.T) {
	var mu sync.Mutex
	a := &probeAuxiliary{binding: probeAuxiliaryBinding{Thread: "synthetic-child", Slot: "a"}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":[]}`))
	a.serve(w, r, &mu, syntheticProbeAccess{}, "http://127.0.0.1:1", "synthetic", func() bool { return false }, nil)
	if w.Code != 503 || a.handler != nil || a.busy {
		t.Fatal("checkpoint failure permitted dispatch")
	}
}

func TestAuxiliaryBindingSurvivesServiceRestart(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[]}]}}\n\n")
	}))
	defer up.Close()
	dir := t.TempDir()
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("private directory failed")
	}
	first := startCheckpointProcess(t, dir, up.URL)
	home := first.next(t, "probe_ready")["codex_home"].(string)
	address, secret, _ := checkpointProfile(t, home)
	root := "12345678-1234-4234-8234-123456789012"
	child := "11111111-1111-4111-8111-111111111111"
	send := func(thread, session, key string) int {
		r := httptest.NewRequest("POST", address+"/responses", strings.NewReader(`{"input":[{"role":"user","content":"synthetic request"}]}`))
		r.RequestURI = ""
		r.Header.Set("Thread-Id", thread)
		r.Header.Set("Session-Id", session)
		r.Header.Set("X-Switcher-Run", key)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal("synthetic auxiliary request failed")
		}
		defer response.Body.Close()
		io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if send(child, root, secret) != 200 || calls.Load() != 1 {
		t.Fatal("initial auxiliary failed")
	}
	if send("22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", secret) != 409 || calls.Load() != 1 {
		t.Fatal("foreign parent admitted")
	}
	if send(child, root, "synthetic-wrong-secret") != 401 || calls.Load() != 1 {
		t.Fatal("unauthenticated auxiliary admitted")
	}
	first.stop()
	second := startCheckpointProcess(t, dir, up.URL)
	second.next(t, "probe_ready")
	if send(child, root, secret) != 409 || calls.Load() != 1 {
		t.Fatal("restored auxiliary replayed")
	}
	if send("22222222-2222-4222-8222-222222222222", root, secret) != 200 || calls.Load() != 2 {
		t.Fatal("new explicit auxiliary task was blocked")
	}
	if send(root, root, secret) != 200 || calls.Load() != 3 {
		t.Fatal("root cannot continue after auxiliary restart")
	}
}
