package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
)

type leasedProbeAccess struct {
	syntheticProbeAccess
	dir string
}

func (a leasedProbeAccess) BeginTurn() (io.Closer, error) { return accounts.AcquireActivity(a.dir) }

// Actual HTTP streaming through the probe while the app's relay disappears.
// Everything, including credentials and upstream content, is synthetic.
func TestServiceStreamSurvivesReconnect(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var attempts atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic\"}\n\n")
		w.(http.Flusher).Flush()
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[]}]}}\n\n")
	}))
	defer up.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	b := &serviceBroker{cache: map[string][]byte{}, input: writer}
	access := leasedProbeAccess{dir: t.TempDir()}
	done := make(chan error, 1)
	go func() {
		done <- switchProbeWithAccess([]string{"--allow-live", "--managed", "--tools"}, input, b, access, up.URL)
	}()
	a, client := net.Pipe()
	defer client.Close()
	detached := make(chan struct{})
	go func() { b.attach(a); close(detached) }()
	reader := bufio.NewReader(client)
	next := func(kind string) map[string]any {
		t.Helper()
		for {
			e := brokerEvent(t, reader, client)
			if e["event"] == kind {
				return e
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
			t.Fatal("profile missing")
		}
		return string(m[1])
	}
	request, _ := http.NewRequest("POST", extract("base_url")+"/responses", strings.NewReader(`{"input":[{"type":"message","role":"user","content":"synthetic"}]}`))
	request.Header.Set("X-Switcher-Run", extract("X-Switcher-Run"))
	request.Header.Set("Thread-Id", "12345678-1234-4234-8234-123456789012")
	responseDone := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 200 {
				err = errDaemon
			}
		}
		responseDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	if lease, err := accounts.AcquireActivity(access.dir); err == nil {
		lease.Close()
		t.Fatal("credential mutation permitted during stream")
	}
	io.WriteString(client, "{\"action\":\"shutdown\"}\n")
	if next("probe_shutdown")["accepted"] != false {
		t.Fatal("active stream shutdown accepted")
	}
	client.Close()
	<-detached
	releaseOnce.Do(func() { close(release) })
	if err := <-responseDone; err != nil {
		t.Fatal("UI detach interrupted HTTP stream")
	}
	a, client = net.Pipe()
	defer client.Close()
	go b.attach(a)
	reader = bufio.NewReader(client)
	if next("probe_ready")["codex_home"] != home {
		t.Fatal("profile changed on reconnect")
	}
	state := next("probe_state")
	for state["busy"] == true {
		io.WriteString(client, "{\"action\":\"status\"}\n")
		state = next("probe_state")
	}
	if state["failed"] != false || state["connected"] != true || attempts.Load() != 1 {
		t.Fatal("session state lost or upstream replayed")
	}
	lease, err := accounts.AcquireActivity(access.dir)
	if err != nil {
		t.Fatal("completed turn lock retained")
	}
	lease.Close()
	io.WriteString(client, "{\"action\":\"shutdown\"}\n")
	if next("probe_shutdown")["accepted"] != true {
		t.Fatal("idle shutdown rejected")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop")
	}
}
