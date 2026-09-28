package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func brokerEvent(t *testing.T, r *bufio.Reader, conn net.Conn) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if json.Unmarshal(line, &event) != nil {
		t.Fatal("invalid event")
	}
	return event
}
func TestServiceDetachPreservesStateAndAckIsolation(t *testing.T) {
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	b := &serviceBroker{cache: map[string][]byte{}, input: writer}
	b.Write([]byte("{\"event\":\"probe_ready\",\"codex_home\":\"/synthetic-home\"}\n"))
	b.Write([]byte("{\"event\":\"probe_selection\",\"slot\":\"c\",\"busy\":true,\"revision\":7,\"accepted\":true}\n"))
	a, client := net.Pipe()
	closed := make(chan struct{})
	go func() { b.attach(a); close(closed) }()
	r := bufio.NewReader(client)
	if brokerEvent(t, r, client)["event"] != "probe_ready" {
		t.Fatal("missing profile")
	}
	state := brokerEvent(t, r, client)
	if state["event"] != "probe_state" || state["slot"] != "c" || state["accepted"] != nil || state["busy"] != true {
		t.Fatal("state or ACK replay corrupted")
	}
	command := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(input)
		if s.Scan() {
			command <- s.Text()
		}
	}()
	io.WriteString(client, "{\"action\":\"usage_refresh\",\"request_id\":1}\n")
	<-command
	client.Close()
	<-closed
	// Upstream can finish with no app connected; only its latest state is kept.
	b.Write([]byte("{\"event\":\"probe_state\",\"slot\":\"c\",\"busy\":false,\"revision\":8}\n"))
	a, next := net.Pipe()
	defer next.Close()
	go b.attach(a)
	r = bufio.NewReader(next)
	brokerEvent(t, r, next)
	state = brokerEvent(t, r, next)
	if state["busy"] != false || state["slot"] != "c" {
		t.Fatal("detached completion lost")
	}
	b.Write([]byte("{\"event\":\"usage_refresh\",\"request_id\":1,\"status\":\"finished\"}\n"))
	b.Write([]byte("{\"event\":\"probe_state\",\"slot\":\"c\",\"revision\":9}\n"))
	if brokerEvent(t, r, next)["event"] != "probe_state" {
		t.Fatal("old UI request ACK delivered to new UI")
	}
}

func TestServiceLifecycleAndSingleInstance(t *testing.T) {
	// Keep the Unix socket path below macOS's sockaddr_un limit.
	dir, err := os.MkdirTemp("/private/tmp", "switcher-service-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	stop := make(chan struct{})
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveProxy(dir, func(input io.Reader, output io.Writer) error {
			io.WriteString(output, "{\"event\":\"probe_ready\",\"codex_home\":\"/synthetic\"}\n")
			close(ready)
			<-stop
			return nil
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("service start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("service start timeout")
	}
	if err := serveProxy(dir, func(io.Reader, io.Writer) error { t.Error("second service started"); return nil }); err == nil {
		t.Fatal("single instance lock bypassed")
	}
	path := filepath.Join(dir, "control.sock")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("socket not private")
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	brokerEvent(t, bufio.NewReader(c), c)
	c.Close()
	// Closing the only app connection must not end the service.
	select {
	case <-done:
		t.Fatal("app disconnect killed service")
	case <-time.After(30 * time.Millisecond):
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceRefusesForeignPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.sock")
	if err := os.WriteFile(path, []byte("user-owned"), 0600); err != nil {
		t.Fatal(err)
	}
	if serveProxy(dir, func(io.Reader, io.Writer) error { return nil }) == nil {
		t.Fatal("regular file replaced")
	}
	data, _ := os.ReadFile(path)
	if !strings.EqualFold(string(data), "user-owned") {
		t.Fatal("existing file changed")
	}
}

func TestServiceConnectionSwitchClearsCache(t *testing.T) {
	b := &serviceBroker{cache: map[string][]byte{}}
	emit := func(value string) { b.Write([]byte(value + "\n")) }
	emit(`{"event":"connection_list","selected":"1","connections":[]}`)
	emit(`{"event":"probe_ready","codex_home":"/synthetic-one"}`)
	emit(`{"event":"probe_state","slot":"a","busy":true,"authentication":[]}`)
	emit(`{"event":"probe_diagnostic","scope":"root","code":"request_busy","at":"2026-09-22T00:00:00Z"}`)
	emit(`{"event":"connection_list","selected":"2","connections":[]}`)
	for _, key := range []string{"probe_ready", "probe_state", "diagnostic_root", "diagnostic_auxiliary"} {
		if b.cache[key] != nil {
			t.Fatal("previous connection cache retained")
		}
	}
	if len(b.diagnostics) != 0 {
		t.Fatal("previous diagnostic suppression retained")
	}
	emit(`{"event":"probe_ready","codex_home":"/synthetic-two"}`)
	emit(`{"event":"probe_state","slot":"b","busy":false,"authentication":[]}`)
	emit(`{"event":"connection_list","selected":"2","connections":[]}`)
	if b.cache["probe_ready"] == nil || b.cache["probe_state"] == nil {
		t.Fatal("same connection status discarded")
	}
	if strings.Contains(string(b.cache["probe_state"]), "authentication") {
		t.Fatal("transient authentication cached")
	}
}
