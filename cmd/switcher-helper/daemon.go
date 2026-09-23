package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/applock"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
	"github.com/ChoBazzi/codex-switcher/internal/livetest"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

var errDaemon = errors.New("proxy_service_unavailable")

// Only a local control relay belongs to the app. The service owns the actual
// probe, its profile, quota polling, failure gates and live HTTP connections.
func serviceDir() (string, error) {
	parent, err := os.UserConfigDir()
	if err != nil {
		return "", errDaemon
	}
	return filepath.Join(parent, "com.bazzi.codex-switcher", "runtime"), nil
}

func proxyConnect(input io.Reader, output io.Writer) error {
	dir, err := serviceDir()
	if err != nil {
		return err
	}
	if err = privateServiceDir(dir); err != nil {
		return err
	}
	// Serialize launchers separately from the service lifetime lock.
	lock, err := applock.Acquire(filepath.Join(dir, "connect"))
	if err != nil {
		return errDaemon
	}
	socket := filepath.Join(dir, "control.sock")
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		exe, e := os.Executable()
		if e != nil {
			lock.Close()
			return errDaemon
		}
		child := exec.Command(exe, "proxy-daemon")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		// nil standard streams attach to /dev/null, never the owning app's pipes.
		if child.Start() != nil {
			lock.Close()
			return errDaemon
		}
		_ = child.Process.Release()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			conn, err = net.DialTimeout("unix", socket, 200*time.Millisecond)
			if err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	lock.Close()
	if err != nil {
		return errDaemon
	}
	return relayProxy(conn, input, output)
}

func relayProxy(conn net.Conn, input io.Reader, output io.Writer) error {
	defer conn.Close()
	if err := json.NewEncoder(output).Encode(map[string]any{"event": "helper_build", "build_id": processBuildID, "protocol_version": controlProtocolVersion}); err != nil {
		return errDaemon
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(conn, input); done <- struct{}{} }()
	go func() { _, _ = io.Copy(output, conn); done <- struct{}{} }()
	<-done
	return nil
}

type servicePeer struct {
	conn   net.Conn
	events chan []byte
}
type serviceBroker struct {
	mu           sync.Mutex
	peer         *servicePeer
	cache        map[string][]byte
	input        *io.PipeWriter
	sequence     uint64
	refreshOwner *servicePeer
	refreshID    uint64
	diagnostics  map[string]probeDiagnostic
}

func (b *serviceBroker) enqueue(p *servicePeer, data []byte) {
	select {
	case p.events <- append([]byte(nil), data...):
	default:
		_ = p.conn.Close()
	}
}

// Reports never wait for UI I/O. A slow or dead UI loses only its control
// connection; upstream streaming and daemon state keep running.
func (b *serviceBroker) Write(data []byte) (int, error) {
	var event map[string]json.RawMessage
	if json.Unmarshal(data, &event) != nil {
		return len(data), nil
	}
	var kind string
	_ = json.Unmarshal(event["event"], &kind)
	b.mu.Lock()
	defer b.mu.Unlock()
	if d := sanitizedProbeDiagnostic(data); d != nil {
		if b.diagnostics == nil {
			b.diagnostics = map[string]probeDiagnostic{}
		}
		// Follow-on failure gates must not hide the original cause.
		if _, exists := b.diagnostics[d.Scope]; exists && (d.Code == "previous_request_failed" || d.Code == "new_input_required") {
			return len(data), nil
		}
		b.diagnostics[d.Scope] = *d
		encoded, _ := json.Marshal(d)
		b.cache["diagnostic_"+d.Scope] = append(encoded, '\n')
		if b.peer != nil {
			b.enqueue(b.peer, append(encoded, '\n'))
		}
		return len(data), nil
	}
	if kind == "probe_shutdown" {
		// Idle-only shutdown ACK must reach its requester before the service exits.
		if b.peer != nil {
			b.peer.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_, _ = b.peer.conn.Write(data)
		}
		return len(data), nil
	}
	if kind == "connection_list" {
		var before, after struct {
			Selected string `json:"selected"`
		}
		_ = json.Unmarshal(b.cache[kind], &before)
		_ = json.Unmarshal(data, &after)
		if before.Selected != after.Selected {
			for _, key := range []string{"probe_ready", "probe_state", "diagnostic_root", "diagnostic_auxiliary"} {
				delete(b.cache, key)
			}
			b.diagnostics = nil
		}
		b.cache[kind] = append([]byte(nil), data...)
	}
	if kind == "probe_ready" || kind == "usage_snapshot" {
		b.cache[kind] = append([]byte(nil), data...)
	}
	if kind == "probe_state" || kind == "probe_selection" || kind == "probe_abandonment" {
		state := make(map[string]json.RawMessage, len(event))
		for k, v := range event {
			state[k] = v
		}
		state["event"] = json.RawMessage(`"probe_state"`)
		delete(state, "accepted")
		// Authentication activity is transient. Reconnect must await a fresh
		// status observation rather than resurrecting a completed exchange.
		delete(state, "authentication")
		normalized, _ := json.Marshal(state)
		b.cache["probe_state"] = append(normalized, '\n')
	}
	if kind == "usage_refresh" {
		var id uint64
		_ = json.Unmarshal(event["request_id"], &id)
		if id != b.sequence || b.peer != b.refreshOwner || b.peer == nil {
			return len(data), nil
		}
		event["request_id"], _ = json.Marshal(b.refreshID)
		encoded, _ := json.Marshal(event)
		b.enqueue(b.peer, append(encoded, '\n'))
		return len(data), nil
	}
	// Raw diagnostics are never retained; only the explicit summary above is.
	if b.peer != nil && (kind == "connection_list" || kind == "connection_result" || kind == "probe_ready" || kind == "probe_state" || kind == "probe_selection" || kind == "probe_abandonment" || kind == "usage_snapshot") {
		b.enqueue(b.peer, data)
	}
	return len(data), nil
}

func (b *serviceBroker) attach(conn net.Conn) {
	p := &servicePeer{conn: conn, events: make(chan []byte, 64)}
	b.mu.Lock()
	if b.peer != nil {
		b.mu.Unlock()
		conn.Close()
		return
	}
	b.peer = p
	for _, key := range []string{"connection_list", "probe_ready", "usage_snapshot", "probe_state", "diagnostic_root", "diagnostic_auxiliary"} {
		if data := b.cache[key]; data != nil {
			b.enqueue(p, data)
		}
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.peer == p {
			b.peer = nil
		}
		b.mu.Unlock()
		conn.Close()
	}()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case data := <-p.events:
				_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				if _, err := conn.Write(data); err != nil {
					conn.Close()
					return
				}
			}
		}
	}()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024), 8192)
	for scanner.Scan() {
		var command map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &command) != nil {
			return
		}
		var action string
		_ = json.Unmarshal(command["action"], &action)
		switch action {
		case "connection_create", "connection_select", "connection_delete", "status", "usage", "select", "recover", "abandon_turn", "account_changing", "account_changed", "shutdown":
		case "usage_refresh":
			var id uint64
			if json.Unmarshal(command["request_id"], &id) != nil || id == 0 {
				return
			}
			b.mu.Lock()
			b.sequence++
			b.refreshOwner = p
			b.refreshID = id
			command["request_id"], _ = json.Marshal(b.sequence)
			b.mu.Unlock()
		default:
			return
		}
		data, _ := json.Marshal(command)
		if _, err := b.input.Write(append(data, '\n')); err != nil {
			return
		}
	}
}

func serveProxy(dir string, run func(io.Reader, io.Writer) error) error {
	lock, err := applock.Acquire(dir)
	if err != nil {
		return errDaemon
	}
	defer lock.Close()
	socket := filepath.Join(dir, "control.sock")
	// The lifetime lock proves no other service owns this path. Never unlink a
	// symlink or a regular file masquerading as stale service metadata.
	if info, e := os.Lstat(socket); e == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if info.Mode()&os.ModeSocket == 0 || !ok || st.Uid != uint32(os.Getuid()) {
			return errDaemon
		}
		if os.Remove(socket) != nil {
			return errDaemon
		}
	} else if !os.IsNotExist(e) {
		return errDaemon
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return errDaemon
	}
	defer listener.Close()
	if os.Chmod(socket, 0600) != nil {
		return errDaemon
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	broker := &serviceBroker{cache: map[string][]byte{}, input: writer}
	defer func() {
		broker.mu.Lock()
		defer broker.mu.Unlock()
		if broker.peer != nil {
			broker.peer.conn.Close()
		}
	}()
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go broker.attach(conn)
		}
	}()
	return run(reader, broker)
}

func proxyDaemon() error {
	dir, err := serviceDir()
	if err != nil {
		return err
	}
	return serveProxy(dir, func(input io.Reader, output io.Writer) error {
		access := accounts.NewRefreshing(credentialstore.New(), filepath.Dir(dir), accounts.NewOAuthRefresher())
		shared := &multiAccountAccess{Manager: access, turns: sharedTurns{acquire: access.BeginTurn}}
		return multiProbe(input, output, dir, shared, livetest.Upstream, nil, usage.Interval, 5*time.Second, access.BeginTurn)
	})
}

// Explicit maintenance only: never starts a service or retries a stop request.
func proxyStop() error { return proxyStopSession(false) }

func proxyStopSession(newSession bool) error { return proxyStopConnection(newSession, "1") }

func proxyStopConnection(newSession bool, connection string) error {
	if !validConnectionID(connection) {
		return errDaemon
	}
	dir, err := serviceDir()
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", filepath.Join(dir, "control.sock"), time.Second)
	if err != nil {
		return errDaemon
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	scanner := bufio.NewScanner(conn)
	if connection != "1" {
		// An older daemon ignores unknown command fields. Confirm multi-session
		// capability before asking it to retire anything other than connection 1.
		if !scanner.Scan() {
			return errDaemon
		}
		var catalog struct {
			Event       string `json:"event"`
			Connections []struct {
				ID string `json:"id"`
			} `json:"connections"`
		}
		if json.Unmarshal(scanner.Bytes(), &catalog) != nil || catalog.Event != "connection_list" {
			return errors.New("proxy_multi_connection_unavailable")
		}
		found := false
		for _, row := range catalog.Connections {
			found = found || row.ID == connection
		}
		if !found {
			return errors.New("proxy_connection_unavailable")
		}
	}
	command := map[string]any{"action": "shutdown", "new_session": newSession}
	// A plain stop ends the service even if the original connection was deleted.
	// Retirement always keeps its explicit target and never falls back.
	if newSession || connection != "1" {
		command["connection_id"] = connection
	}
	if err = json.NewEncoder(conn).Encode(command); err != nil {
		return errDaemon
	}
	for scanner.Scan() {
		var event struct {
			Event    string `json:"event"`
			Accepted bool   `json:"accepted"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return errDaemon
		}
		if event.Event == "probe_shutdown" {
			if event.Accepted {
				return nil
			}
			return errors.New("proxy_service_busy")
		}
	}
	return errDaemon
}

func privateServiceDir(dir string) error {
	if os.MkdirAll(dir, 0700) != nil {
		return errDaemon
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errDaemon
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) {
		return errDaemon
	}
	return nil
}
