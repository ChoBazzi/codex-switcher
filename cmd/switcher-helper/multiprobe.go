package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

const probeConnectionLimit = 5

type connectionEvent struct {
	id    string
	data  []byte
	ended bool
	err   error
}
type connectionWriter struct {
	id      string
	events  chan<- connectionEvent
	stopped <-chan struct{}
}

func (w connectionWriter) Write(data []byte) (int, error) {
	select {
	case w.events <- connectionEvent{id: w.id, data: append([]byte(nil), data...)}:
	case <-w.stopped:
		return 0, io.ErrClosedPipe
	}
	return len(data), nil
}

type probeConnection struct {
	id           string
	commands     chan []byte
	reader       *io.PipeReader
	writer       *io.PipeWriter
	ready, state map[string]any
	diagnostics  map[string]*probeDiagnostic
	activate     bool
}

// Fixed private directories are the durable catalog. Directory creation commits
// intent before starting a listener; an incomplete startup is restored, never
// silently replaced by a different connection. Existing connection 1 stays put.
func connectionDirectory(dir, id string) string {
	if id == "1" {
		return dir
	}
	return filepath.Join(dir, "connection-"+id)
}
func validConnectionID(id string) bool { return len(id) == 1 && id[0] >= '1' && id[0] <= '5' }

func multiProbe(input io.Reader, output io.Writer, dir string, access probeAccess, upstream string, fetcher usage.Fetcher, interval, cooldown time.Duration, shutdownLease func() (io.Closer, error)) error {
	if privateServiceDir(dir) != nil {
		return errCheckpoint
	}
	events := make(chan connectionEvent, 128)
	commands := make(chan map[string]any, 32)
	stopped := make(chan struct{})
	var workers sync.WaitGroup
	connections := map[string]*probeConnection{}
	selected := "1"
	sharedUsage := &probeUsageCache{}
	var lease io.Closer
	defer func() {
		close(stopped)
		for _, c := range connections {
			c.reader.Close()
			c.writer.Close()
		}
		workers.Wait()
		if lease != nil {
			lease.Close()
		}
	}()
	emit := func(e map[string]any) { _ = json.NewEncoder(output).Encode(e) }
	tagged := func(c *probeConnection, e map[string]any) {
		copy := make(map[string]any, len(e)+1)
		for k, v := range e {
			copy[k] = v
		}
		copy["connection_id"] = c.id
		emit(copy)
	}
	list := func() {
		rows := []map[string]any{}
		for i := 1; i <= probeConnectionLimit; i++ {
			id := strconv.Itoa(i)
			c := connections[id]
			if c == nil {
				continue
			}
			rows = append(rows, map[string]any{"id": id, "ready": c.ready != nil && c.state != nil, "busy": c.state == nil || c.state["busy"] == true, "failed": c.state != nil && c.state["failed"] == true})
		}
		emit(map[string]any{"event": "connection_list", "selected": selected, "connections": rows, "limit": probeConnectionLimit})
	}
	show := func(c *probeConnection) {
		selected = c.id
		list()
		if c.ready != nil {
			tagged(c, c.ready)
		}
		if c.state != nil {
			tagged(c, c.state)
		}
		for _, scope := range []string{"root", "auxiliary"} {
			if d := c.diagnostics[scope]; d != nil {
				data, _ := json.Marshal(d)
				var e map[string]any
				_ = json.Unmarshal(data, &e)
				tagged(c, e)
			}
		}
	}
	launch := func(id string, activate bool) error {
		path := connectionDirectory(dir, id)
		if privateServiceDir(path) != nil {
			return errCheckpoint
		}
		parent, err := os.Open(dir)
		if err != nil {
			return errCheckpoint
		}
		err = parent.Sync()
		parent.Close()
		if err != nil {
			return errCheckpoint
		}
		r, w := io.Pipe()
		c := &probeConnection{id: id, commands: make(chan []byte, 32), reader: r, writer: w, diagnostics: map[string]*probeDiagnostic{}, activate: activate}
		connections[id] = c
		workers.Add(2)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-stopped:
					return
				case data := <-c.commands:
					if _, err := w.Write(data); err != nil {
						return
					}
				}
			}
		}()
		go func() {
			defer workers.Done()
			err := switchProbeWithSharedUsage([]string{"--allow-live", "--managed", "--auto", "--tools"}, r, connectionWriter{id, events, stopped}, access, upstream, fetcher, interval, cooldown, path, sharedUsage, id == "1")
			select {
			case events <- connectionEvent{id: id, ended: true, err: err}:
			case <-stopped:
			}
		}()
		return nil
	}
	for i := 1; i <= probeConnectionLimit; i++ {
		id := strconv.Itoa(i)
		if i > 1 {
			if _, err := os.Lstat(connectionDirectory(dir, id)); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return errCheckpoint
			}
		}
		if err := launch(id, false); err != nil {
			return err
		}
	}
	list()
	go func() {
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 1024), 8192)
		for scanner.Scan() {
			var command map[string]any
			if json.Unmarshal(scanner.Bytes(), &command) != nil {
				continue
			}
			select {
			case commands <- command:
			case <-stopped:
				return
			}
		}
		select {
		case commands <- nil:
		case <-stopped:
		}
	}()
	send := func(c *probeConnection, command map[string]any) bool {
		data, _ := json.Marshal(command)
		select {
		case c.commands <- append(data, '\n'):
			return true
		default:
			return false
		}
	}
	phase := ""
	prepared := map[string]bool{}
	aborted := map[string]bool{}
	acknowledged := map[string]bool{}
	ended := map[string]bool{}
	retire := ""
	for {
		select {
		case command := <-commands:
			if command == nil {
				return nil
			}
			action, _ := command["action"].(string)
			id := "1" // Legacy commands always target the original connection.
			if raw, present := command["connection_id"]; present {
				value, ok := raw.(string)
				if !ok || !validConnectionID(value) {
					emit(map[string]any{"event": "connection_result", "accepted": false})
					continue
				}
				id = value
			}
			c := connections[id]
			if phase != "" {
				continue
			}
			switch action {
			case "connection_create":
				next := ""
				for i := 1; i <= probeConnectionLimit; i++ {
					candidate := strconv.Itoa(i)
					if connections[candidate] == nil {
						next = candidate
						break
					}
				}
				if next == "" {
					emit(map[string]any{"event": "connection_result", "accepted": false})
					continue
				}
				if err := launch(next, true); err != nil {
					return err
				}
				list()
			case "connection_select":
				if c == nil || c.ready == nil || c.state == nil {
					emit(map[string]any{"event": "connection_result", "accepted": false})
					continue
				}
				show(c)
				emit(map[string]any{"event": "connection_result", "accepted": true})
			case "shutdown":
				if c == nil {
					emit(map[string]any{"event": "probe_shutdown", "accepted": false})
					continue
				}
				phase = "prepare"
				prepared = map[string]bool{}
				aborted = map[string]bool{}
				acknowledged = map[string]bool{}
				ended = map[string]bool{}
				retire = ""
				if command["new_session"] == true {
					retire = id
				}
				for _, p := range connections {
					if !send(p, map[string]any{"action": "prepare_shutdown"}) {
						return errDaemon
					}
				}
			case "status":
				for _, p := range connections {
					if !send(p, map[string]any{"action": "status"}) {
						return errDaemon
					}
				}
			case "usage", "usage_refresh", "account_changing", "account_changed":
				if !send(connections["1"], command) {
					return errDaemon
				}
			case "select", "recover", "abandon_turn":
				if c == nil || id != selected {
					emit(map[string]any{"event": "connection_result", "accepted": false})
					continue
				}
				if !send(c, command) {
					return errDaemon
				}
			}
		case event := <-events:
			c := connections[event.id]
			if event.ended {
				if event.err != nil || phase != "commit" {
					return errDaemon
				}
				ended[c.id] = true
				if len(ended) == len(connections) {
					emit(map[string]any{"event": "probe_shutdown", "accepted": len(acknowledged) == len(connections)})
					return nil
				}
				continue
			}
			var e map[string]any
			if json.Unmarshal(event.data, &e) != nil {
				continue
			}
			kind, _ := e["event"].(string)
			switch kind {
			case "probe_shutdown_prepared":
				if phase != "prepare" {
					continue
				}
				prepared[c.id] = e["accepted"] == true
				if len(prepared) < len(connections) {
					continue
				}
				ok := true
				for _, accepted := range prepared {
					ok = ok && accepted
				}
				if ok && shutdownLease != nil {
					var err error
					lease, err = shutdownLease()
					ok = err == nil
					if err != nil {
						lease = nil
					}
				}
				if !ok {
					// Ordered per-connection commands unfreeze every listener before later controls.
					for _, p := range connections {
						if !send(p, map[string]any{"action": "abort_shutdown"}) {
							return errDaemon
						}
					}
					phase = "abort"
					continue
				}
				phase = "commit"
				for _, p := range connections {
					if !send(p, map[string]any{"action": "shutdown", "new_session": p.id == retire}) {
						return errDaemon
					}
				}
			case "probe_shutdown_aborted":
				if phase != "abort" {
					continue
				}
				aborted[c.id] = true
				if len(aborted) == len(connections) {
					phase = ""
					emit(map[string]any{"event": "probe_shutdown", "accepted": false})
				}
			case "probe_shutdown":
				if phase != "commit" || e["accepted"] != true {
					return errDaemon
				}
				acknowledged[c.id] = true
			case "probe_ready":
				c.ready = e
				if c.id == selected {
					tagged(c, e)
				}
			case "probe_state", "probe_selection", "probe_abandonment":
				state := make(map[string]any, len(e))
				for k, v := range e {
					state[k] = v
				}
				state["event"] = "probe_state"
				delete(state, "accepted")
				delete(state, "authentication")
				c.state = state
				if c.activate {
					c.activate = false
					show(c)
					emit(map[string]any{"event": "connection_result", "accepted": true})
				} else {
					list()
					if c.id == selected {
						tagged(c, e)
					}
				}
			case "usage_snapshot", "usage_refresh":
				if c.id == "1" {
					emit(e)
				}
			default:
				if d := sanitizedProbeDiagnostic(event.data); d != nil {
					if c.diagnostics[d.Scope] != nil && (d.Code == "previous_request_failed" || d.Code == "new_input_required") {
						continue
					}
					c.diagnostics[d.Scope] = d
					if c.id == selected {
						data, _ := json.Marshal(d)
						var clean map[string]any
						_ = json.Unmarshal(data, &clean)
						tagged(c, clean)
					}
				}
			}
		}
	}
}
