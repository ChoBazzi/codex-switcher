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
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Child has only synthetic access. Kill tests never touch a user's daemon.
func TestProbeCheckpointChild(t *testing.T) {
	if os.Getenv("SWITCHER_CHECKPOINT_CHILD") != "1" {
		return
	}
	err := switchProbeWithCheckpoint([]string{"--allow-live", "--managed", "--tools"}, os.Stdin, os.Stdout,
		syntheticProbeAccess{}, os.Getenv("SWITCHER_CHECKPOINT_UPSTREAM"), nil, time.Hour, time.Second, os.Getenv("SWITCHER_CHECKPOINT_DIR"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

type checkpointProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	events chan map[string]any
}

func startCheckpointProcess(t *testing.T, dir, upstream string) *checkpointProcess {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestProbeCheckpointChild$")
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), "SWITCHER_CHECKPOINT_CHILD=1", "SWITCHER_CHECKPOINT_DIR="+dir, "SWITCHER_CHECKPOINT_UPSTREAM="+upstream)
	input, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &checkpointProcess{c, input, make(chan map[string]any, 128)}
	if c.Start() != nil {
		t.Fatal("child start failed")
	}
	go func() {
		defer close(p.events)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var e map[string]any
			if json.Unmarshal(scanner.Bytes(), &e) == nil {
				p.events <- e
			}
		}
	}()
	t.Cleanup(func() { p.stop() })
	return p
}
func (p *checkpointProcess) stop() {
	if p.cmd.ProcessState == nil {
		p.cmd.Process.Kill()
		p.cmd.Wait()
	}
	p.input.Close()
}
func (p *checkpointProcess) next(t *testing.T, kind string) map[string]any {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-p.events:
			if !ok {
				t.Fatal("checkpoint child exited")
			}
			if e["event"] == kind {
				return e
			}
		case <-timer.C:
			t.Fatal("checkpoint event timeout")
		}
	}
}
func checkpointProfile(t *testing.T, home string) (string, string, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	field := func(key string) string {
		m := regexp.MustCompile(`(?m)^` + key + ` = "([^"]+)"`).FindSubmatch(data)
		if len(m) != 2 {
			t.Fatal("profile field missing")
		}
		return string(m[1])
	}
	return field("base_url"), field("X-Switcher-Run"), data
}
func checkpointRequest(t *testing.T, url, secret, body string) int {
	return checkpointRequestRoute(t, url, secret, "/responses", body)
}

func checkpointRequestRoute(t *testing.T, url, secret, route, body string) int {
	t.Helper()
	r, _ := http.NewRequest("POST", url+route, strings.NewReader(body))
	r.Header.Set("X-Switcher-Run", secret)
	r.Header.Set("Thread-Id", "12345678-1234-4234-8234-123456789012")
	r.Header.Set("Session-Id", "12345678-1234-4234-8234-123456789012")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil {
		t.Fatal("synthetic request failed")
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func TestProbeCheckpointKillAndResume(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "completed"
		if interrupted {
			name = "inflight"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			entered := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				io.Copy(io.Discard, r.Body)
				if interrupted && n == 1 {
					entered <- struct{}{}
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[]}]}}\n\n")
			}))
			defer upstream.Close()
			dir := filepath.Join(t.TempDir(), "runtime")
			if err := privateServiceDir(dir); err != nil {
				t.Fatal("private test directory failed")
			}
			p := startCheckpointProcess(t, dir, upstream.URL)
			defer func() { p.stop() }()
			home := p.next(t, "probe_ready")["codex_home"].(string)
			p.next(t, "probe_state")
			io.WriteString(p.input, "{\"action\":\"select\",\"slot\":\"b\",\"revision\":0}\n")
			if p.next(t, "probe_selection")["accepted"] != true {
				t.Fatal("initial selection failed")
			}
			// Preserve user/CLI additions to config, too.
			f, err := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			io.WriteString(f, "\n# synthetic user customization\n")
			f.Close()
			url, secret, originalProfile := checkpointProfile(t, home)
			first := `{"input":[{"role":"user","content":"synthetic first"}]}`
			if interrupted {
				requestDone := make(chan struct{})
				go func() {
					defer close(requestDone)
					r, _ := http.NewRequest("POST", url+"/responses", strings.NewReader(first))
					r.Header.Set("X-Switcher-Run", secret)
					r.Header.Set("Thread-Id", "12345678-1234-4234-8234-123456789012")
					r.Header.Set("Session-Id", "12345678-1234-4234-8234-123456789012")
					response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
					if err == nil {
						io.Copy(io.Discard, response.Body)
						response.Body.Close()
					}
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("upstream not entered")
				}
				p.stop()
				<-requestDone
			} else {
				if checkpointRequest(t, url, secret, first) != 200 {
					t.Fatal("first request rejected")
				}
				for p.next(t, "probe_state")["busy"] == true {
				}
				p.stop()
			}
			p = startCheckpointProcess(t, dir, upstream.URL)
			if p.next(t, "probe_ready")["codex_home"] != home {
				t.Fatal("CLI home changed")
			}
			state := p.next(t, "probe_state")
			_, _, profile := checkpointProfile(t, home)
			if string(profile) != string(originalProfile) || state["connected"] != true || state["failed"] != interrupted || state["slot"] != "b" {
				t.Fatal("checkpoint state not restored")
			}
			if calls.Load() != 1 {
				t.Fatal("restart replayed request")
			}
			if interrupted {
				if checkpointRequest(t, url, secret, first) != 409 {
					t.Fatal("interrupted request admitted")
				}
				command, _ := json.Marshal(map[string]any{"action": "select", "slot": "b", "revision": state["revision"]})
				p.input.Write(append(command, '\n'))
				if p.next(t, "probe_selection")["accepted"] != true {
					t.Fatal("same-account recovery rejected")
				}
			}
			if checkpointRequest(t, url, secret, first) != 409 || calls.Load() != 1 {
				t.Fatal("old input replayed after restart/recovery")
			}
			next := `{"input":[{"role":"user","content":"synthetic first"},{"role":"assistant","content":"synthetic history"},{"role":"user","content":"synthetic new instruction"}]}`
			if checkpointRequest(t, url, secret, next) != 200 || calls.Load() != 2 {
				t.Fatal("new input did not resume")
			}
			p.stop()
			contents, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"synthetic-a", "synthetic first", "synthetic history", "synthetic-response"} {
				if strings.Contains(string(contents), forbidden) {
					t.Fatal("checkpoint contains credential or content")
				}
			}
		})
	}
}

func TestProbeCheckpointRejectsUnsafeStorage(t *testing.T) {
	for _, kind := range []string{"corrupt", "permissions", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "runtime")
			if err := privateServiceDir(dir); err != nil {
				t.Fatal("private test directory failed")
			}
			path := filepath.Join(dir, "checkpoint.json")
			switch kind {
			case "corrupt":
				os.WriteFile(path, []byte(`{"Version":999}`), 0600)
			case "permissions":
				os.WriteFile(path, []byte(`{}`), 0644)
			case "symlink":
				os.Symlink(filepath.Join(t.TempDir(), "missing"), path)
			case "directory":
				os.Mkdir(path, 0700)
			}
			if _, err := readProbeCheckpoint(dir); err == nil {
				t.Fatal("unsafe checkpoint accepted")
			}
		})
	}
}

func TestProbeCheckpointOwnerRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if privateServiceDir(dir) != nil {
		t.Fatal("private directory")
	}
	c := &probeCheckpoint{Version: 1, Address: "127.0.0.1:12345", Home: filepath.Join(dir, "switcher-probe-synthetic"), Secret: strings.Repeat("x", 43), Slot: "c", OpaqueSlot: "c",
		Owners: []probeCheckpointOwner{{Key: [32]byte{1}, Credential: [32]byte{2}, Slot: "c"}}}
	if writeProbeCheckpoint(dir, c) != nil {
		t.Fatal("save failed")
	}
	restored, err := readProbeCheckpoint(dir)
	if err != nil || restored.Owners[0] != c.Owners[0] || restored.OpaqueSlot != "c" {
		t.Fatal("ownership not preserved")
	}
	info, _ := os.Stat(filepath.Join(dir, "checkpoint.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("checkpoint is not private")
	}
	c.Retired = true
	if writeProbeCheckpoint(dir, c) != nil {
		t.Fatal("retirement failed")
	}
	if restored, err = readProbeCheckpoint(dir); err != nil || restored != nil {
		t.Fatal("explicit new session reused checkpoint")
	}
}

func TestProbeCheckpointWriteFailureBlocksDispatch(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	dir := filepath.Join(t.TempDir(), "runtime")
	p := startCheckpointProcess(t, dir, upstream.URL)
	defer p.stop()
	home := p.next(t, "probe_ready")["codex_home"].(string)
	p.next(t, "probe_state")
	url, secret, _ := checkpointProfile(t, home)
	// Force atomic rename to fail after successful startup.
	path := filepath.Join(dir, "checkpoint.json")
	if os.Remove(path) != nil || os.Mkdir(path, 0700) != nil {
		t.Fatal("failure setup")
	}
	r, _ := http.NewRequest("POST", url+"/responses", strings.NewReader(`{"input":[{"role":"user","content":"synthetic"}]}`))
	r.Header.Set("X-Switcher-Run", secret)
	r.Header.Set("Thread-Id", "12345678-1234-4234-8234-123456789012")
	r.Header.Set("Session-Id", "12345678-1234-4234-8234-123456789012")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err == nil {
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode == 200 {
			t.Fatal("storage failure reported success")
		}
	}
	p.stop()
	if calls.Load() != 0 {
		t.Fatal("dispatched without durable checkpoint")
	}
	if _, err := readProbeCheckpoint(dir); err == nil {
		t.Fatal("invalid storage silently reset")
	}
}

func TestInstalledProbeCheckpoint(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("installed CLI opt-in")
	}
	var calls atomic.Int32
	var historyPreserved atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		r.Body.Close()
		if calls.Add(1) == 2 {
			historyPreserved.Store(strings.Contains(string(body), "synthetic-remember-word") && strings.Contains(string(body), "synthetic-remember-answer"))
		}
		item := map[string]any{"type": "message", "id": "msg_synthetic", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "synthetic-remember-answer", "annotations": []any{}}}}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(kind string, fields map[string]any) {
			fields["type"] = kind
			data, _ := json.Marshal(fields)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		emit("response.created", map[string]any{"response": map[string]any{"id": "resp_synthetic", "status": "in_progress"}})
		emit("response.output_item.added", map[string]any{"output_index": 0, "item": item})
		emit("response.output_item.done", map[string]any{"output_index": 0, "item": item})
		emit("response.completed", map[string]any{"response": map[string]any{"id": "resp_synthetic", "status": "completed", "output": []any{item}}})
	}))
	defer upstream.Close()
	dir := filepath.Join(t.TempDir(), "runtime")
	p := startCheckpointProcess(t, dir, upstream.URL)
	defer func() { p.stop() }()
	home := p.next(t, "probe_ready")["codex_home"].(string)
	p.next(t, "probe_state")
	run := func(tail ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, "codex", append([]string{"exec", "--skip-git-repo-check", "--json"}, tail...)...)
		c.Dir = home
		c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + home, "TMPDIR=" + os.TempDir(), "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatal("synthetic CLI resume failed; raw output omitted")
		}
		for _, line := range strings.Split(string(out), "\n") {
			var event struct {
				Type   string `json:"type"`
				Thread string `json:"thread_id"`
			}
			if json.Unmarshal([]byte(line), &event) == nil && event.Type == "thread.started" {
				return event.Thread
			}
		}
		t.Fatal("CLI thread missing")
		return ""
	}
	id := run("Remember synthetic-remember-word.")
	for p.next(t, "probe_state")["busy"] == true {
	}
	p.stop()
	p = startCheckpointProcess(t, dir, upstream.URL)
	if p.next(t, "probe_ready")["codex_home"] != home {
		t.Fatal("CLI home changed")
	}
	p.next(t, "probe_state")
	if calls.Load() != 1 {
		t.Fatal("restart invoked model")
	}
	if run("resume", id, "Recall the previous synthetic word.") != id || calls.Load() != 2 || !historyPreserved.Load() {
		t.Fatal("CLI conversation not preserved")
	}
}

func TestProbeCheckpointNativeCompaction(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-a" {
			t.Error("cross-account opaque dispatch")
		}
		if r.URL.Path == "/compact" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"object":"response.compaction","output":[{"role":"user","content":"synthetic first"},{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-opaque"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"type":"response.completed","response":{"id":"synthetic-response","status":"completed","output":[{"type":"message","role":"assistant","phase":"final_answer","content":[]}]}}`+"\n\n")
	}))
	defer upstream.Close()
	dir := filepath.Join(t.TempDir(), "runtime")
	p := startCheckpointProcess(t, dir, upstream.URL)
	defer func() { p.stop() }()
	home := p.next(t, "probe_ready")["codex_home"].(string)
	p.next(t, "probe_state")
	url, secret, _ := checkpointProfile(t, home)
	if checkpointRequestRoute(t, url, secret, "/responses/compact", `{"input":[{"role":"user","content":"synthetic first"}]}`) != 200 {
		t.Fatal("native compact failed")
	}
	for p.next(t, "probe_state")["busy"] == true {
	}
	p.stop()
	p = startCheckpointProcess(t, dir, upstream.URL)
	p.next(t, "probe_ready")
	state := p.next(t, "probe_state")
	command, _ := json.Marshal(map[string]any{"action": "select", "slot": "b", "revision": state["revision"]})
	p.input.Write(append(command, '\n'))
	if p.next(t, "probe_selection")["accepted"] != false || calls.Load() != 1 {
		t.Fatal("opaque pin lost after restart")
	}
	body := `{"input":[{"role":"user","content":"synthetic first"},{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-opaque"},{"role":"user","content":"synthetic next"}]}`
	if checkpointRequest(t, url, secret, body) != 200 || calls.Load() != 2 {
		t.Fatal("native ownership not restored")
	}
	for p.next(t, "probe_state")["busy"] == true {
	}
	io.WriteString(p.input, "{\"action\":\"shutdown\",\"new_session\":true}\n")
	if p.next(t, "probe_shutdown")["accepted"] != true {
		t.Fatal("idle new session refused")
	}
	// Wait for graceful termination before starting another daemon.
	if p.cmd.Wait() != nil {
		t.Fatal("shutdown failed")
	}
	p = startCheckpointProcess(t, dir, upstream.URL)
	if p.next(t, "probe_ready")["codex_home"] == home {
		t.Fatal("explicit new session reused old connection")
	}
	if p.next(t, "probe_state")["connected"] != false || calls.Load() != 2 {
		t.Fatal("new session replayed history")
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); err != nil {
		t.Fatal("old CLI home removed")
	}
}

func TestProbeCheckpointStatusDoesNotWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	p := startCheckpointProcess(t, dir, "http://127.0.0.1:9")
	defer p.stop()
	p.next(t, "probe_ready")
	state := p.next(t, "probe_state")
	path := filepath.Join(dir, "checkpoint.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Atomic checkpoint writes replace the inode. Keep it open to prevent reuse.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := 0; i < 10; i++ {
		io.WriteString(p.input, "{\"action\":\"status\"}\n")
		if p.next(t, "probe_state")["revision"] != state["revision"] {
			t.Fatal("status mutated revision")
		}
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("status rewrote checkpoint")
	}
	command, _ := json.Marshal(map[string]any{"action": "select", "slot": "b", "revision": state["revision"]})
	p.input.Write(append(command, '\n'))
	if p.next(t, "probe_selection")["accepted"] != true {
		t.Fatal("selection failed")
	}
	after, err = os.Stat(path)
	if err != nil || os.SameFile(before, after) {
		t.Fatal("state change was not saved")
	}
	saved, err := readProbeCheckpoint(dir)
	if err != nil || saved.Slot != "b" {
		t.Fatal("selection not durable before acknowledgment")
	}
}

func TestProbeCheckpointComparison(t *testing.T) {
	a := &probeCheckpoint{Slot: "a", Owners: []probeCheckpointOwner{{Key: [32]byte{1}, Slot: "a"}, {Key: [32]byte{2}, Slot: "b"}}}
	b := *a
	b.Owners = []probeCheckpointOwner{a.Owners[1], a.Owners[0]}
	if !sameProbeCheckpoint(a, &b) {
		t.Fatal("registry order counted as change")
	}
	b.Busy = true
	if sameProbeCheckpoint(a, &b) {
		t.Fatal("inflight change ignored")
	}
	b.Busy = false
	b.Owners[0].Credential = [32]byte{3}
	if sameProbeCheckpoint(a, &b) {
		t.Fatal("ownership change ignored")
	}
	if sameProbeCheckpoint(nil, a) {
		t.Fatal("initial save skipped")
	}
}
