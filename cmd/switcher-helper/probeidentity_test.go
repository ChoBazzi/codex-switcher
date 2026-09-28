package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
)

// Mutable synthetic source models replacement both between requests and during
// RequestAccess without a verified refresh. No real vault, daemon, or OAuth
// endpoint is used.
type historyAccess struct {
	mu       sync.Mutex
	current  accounts.Access
	dispatch *accounts.Access
}

func newHistoryAccess() *historyAccess {
	return &historyAccess{current: accounts.Access{Token: "synthetic-token", AccountID: "synthetic-account", UserID: "synthetic-user", Registration: "synthetic-registration", ExpiresAt: time.Now().Add(time.Hour)}}
}
func (a *historyAccess) Access(string, time.Time) (accounts.Access, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current, nil
}
func (a *historyAccess) RequestAccess(string, time.Time) (accounts.Access, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dispatch != nil {
		a.current = *a.dispatch
		a.dispatch = nil
	}
	return a.current, nil
}
func (a *historyAccess) change(field string, atDispatch bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.current
	switch field {
	case "token":
		next.Token += "-new"
	case "account":
		next.AccountID += "-new"
	case "user":
		next.UserID += "-new"
	case "registration":
		next.Registration += "-new"
	case "missing-user":
		next.UserID = ""
	}
	if atDispatch {
		a.dispatch = &next
	} else {
		a.current = next
	}
}

const historyReasoning = `{"type":"reasoning","id":"rs_synthetic","summary":[],"encrypted_content":"synthetic-opaque"}`
const historyFirst = `{"input":[{"role":"user","content":"synthetic request"}]}`
const historyFollowup = `{"input":[{"role":"user","content":"synthetic request"},` + historyReasoning + `]}`
const historyCompact = `{"input":[{"role":"user","content":"synthetic request"},{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-compact"}]}`

func historyUpstream(calls *atomic.Int32, checks ...func(*http.Request, []byte)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		for _, check := range checks {
			check(r, b)
		}
		if r.URL.Path == "/compact" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"object":"response.compaction","output":[{"role":"user","content":"synthetic request"},{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-compact"}]}`)
			return
		}
		reasoning := historyReasoning + ","
		if strings.Contains(string(b), "fresh text") {
			reasoning = ""
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_synthetic","status":"completed","output":[`+reasoning+`{"type":"message","role":"assistant","content":[]}]}}`+"\n\n")
	}))
}

type historyProbe struct {
	commands        *io.PipeWriter
	events          chan map[string]any
	address, secret string
	stop            func()
}

func startHistoryProbe(t *testing.T, source probeAccess, upstream, dir string) *historyProbe {
	t.Helper()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	input, commands := io.Pipe()
	output, sink := io.Pipe()
	p := &historyProbe{commands: commands, events: make(chan map[string]any, 128)}
	done := make(chan error, 1)
	go func() {
		defer sink.Close()
		done <- switchProbeWithCheckpoint([]string{"--allow-live", "--managed", "--tools"}, input, sink, source, upstream, nil, time.Hour, time.Second, dir)
	}()
	go func() {
		defer close(p.events)
		scan := bufio.NewScanner(output)
		for scan.Scan() {
			var event map[string]any
			if json.Unmarshal(scan.Bytes(), &event) == nil {
				p.events <- event
			}
		}
	}()
	var once sync.Once
	p.stop = func() {
		once.Do(func() {
			commands.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("synthetic proxy shutdown timeout")
			}
			input.Close()
			output.Close()
		})
	}
	t.Cleanup(p.stop)
	home := p.next(t, "probe_ready")["codex_home"].(string)
	p.address, p.secret, _ = checkpointProfile(t, home)
	p.next(t, "probe_state")
	return p
}
func (p *historyProbe) next(t *testing.T, kind string) map[string]any {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-p.events:
			if !ok {
				t.Fatal("synthetic proxy exited")
			}
			if e["event"] == kind {
				return e
			}
		case <-timer.C:
			t.Fatal("synthetic proxy event timeout: " + kind)
		}
	}
}
func (p *historyProbe) send(t *testing.T, path, body string) int {
	t.Helper()
	return p.sendThread(t, path, body, "12345678-1234-4234-8234-123456789012")
}
func (p *historyProbe) sendThread(t *testing.T, path, body, thread string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", p.address+path, strings.NewReader(body))
	req.Header.Set("Thread-Id", thread)
	req.Header.Set("Session-Id", "12345678-1234-4234-8234-123456789012")
	req.Header.Set("X-Switcher-Run", p.secret)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if _, err := io.WriteString(p.commands, "{\"action\":\"status\"}\n"); err != nil {
		t.Fatal(err)
	}
	for {
		if p.next(t, "probe_state")["busy"] == false {
			break
		}
	}
	return resp.StatusCode
}

func TestProbeHistoryIdentityIsolation(t *testing.T) {
	for _, mode := range []string{"reasoning", "reasoning_with_declaration", "compact"} {
		for _, field := range []string{"unchanged", "token", "account", "user", "registration", "missing-user"} {
			for _, dispatch := range []bool{false, true} {
				name := mode + "/" + field
				if dispatch {
					name += "/dispatch"
				}
				t.Run(name, func(t *testing.T) {
					var calls atomic.Int32
					up := historyUpstream(&calls)
					defer up.Close()
					source := newHistoryAccess()
					p := startHistoryProbe(t, source, up.URL, t.TempDir())
					path, next := "/responses", historyFollowup
					if mode == "reasoning_with_declaration" {
						next = strings.TrimSuffix(historyFollowup, "]}") + `,{"type":"additional_tools","role":"user","tools":[]}]}`
					}
					if mode == "compact" {
						path, next = "/responses/compact", historyCompact
					}
					if p.send(t, path, historyFirst) != 200 {
						t.Fatal("initial response failed")
					}
					source.change(field, dispatch)
					status := p.send(t, "/responses", next)
					if field == "unchanged" {
						if status != 200 || calls.Load() != 2 {
							t.Fatal("same identity rejected")
						}
					} else {
						if status == 200 || calls.Load() != 1 {
							t.Fatal("old opaque state dispatched to changed identity")
						}
						if p.send(t, "/responses", next) != 409 || calls.Load() != 1 {
							t.Fatal("failed request replayed")
						}
					}
				})
			}
		}
	}
}

func TestProbeOldReasoningCannotBeRelabeled(t *testing.T) {
	var calls atomic.Int32
	up := historyUpstream(&calls)
	defer up.Close()
	source := newHistoryAccess()
	p := startHistoryProbe(t, source, up.URL, t.TempDir())
	if p.send(t, "/responses", historyFirst) != 200 {
		t.Fatal("initial response failed")
	}
	source.change("registration", false)
	if p.send(t, "/responses", `{"input":[{"role":"user","content":"fresh text"}]}`) != 200 {
		t.Fatal("fresh text blocked after replacement")
	}
	if p.send(t, "/responses", historyFollowup) != 409 || calls.Load() != 2 {
		t.Fatal("old blob acquired new ownership")
	}
}

func TestAuxiliaryHistoryIdentityIsolation(t *testing.T) {
	for _, field := range []string{"unchanged", "token", "account", "user", "registration", "missing-user"} {
		t.Run(field, func(t *testing.T) {
			var calls atomic.Int32
			up := historyUpstream(&calls)
			defer up.Close()
			source := newHistoryAccess()
			a := &probeAuxiliary{binding: probeAuxiliaryBinding{Thread: "synthetic-child", Slot: "a"}}
			defer func() {
				if a.handler != nil {
					a.handler.Close()
				}
			}()
			var mu sync.Mutex
			send := func(body string) int {
				r := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
				w := httptest.NewRecorder()
				a.serve(w, r, &mu, source, up.URL, "synthetic-salt", func() bool { return true }, nil)
				return w.Code
			}
			if send(historyFirst) != 200 {
				t.Fatal("auxiliary initial response failed")
			}
			source.change(field, true)
			status := send(historyFollowup)
			if field == "unchanged" {
				if status != 200 || calls.Load() != 2 {
					t.Fatal("same auxiliary identity rejected")
				}
			} else if status == 200 || calls.Load() != 1 || !a.failed {
				t.Fatal("auxiliary history crossed identity")
			}
		})
	}
}

func TestProbeHistoryCheckpointIdentity(t *testing.T) {
	for _, mode := range []string{"reasoning", "compact"} {
		for _, field := range []string{"unchanged", "registration", "user", "legacy"} {
			t.Run(mode+"/"+field, func(t *testing.T) {
				var calls atomic.Int32
				up := historyUpstream(&calls)
				defer up.Close()
				source := newHistoryAccess()
				dir := t.TempDir()
				first := startHistoryProbe(t, source, up.URL, dir)
				path, body := "/responses", historyFirst
				if mode == "compact" {
					path = "/responses/compact"
				}
				if first.send(t, path, body) != 200 {
					t.Fatal("initial response failed")
				}
				first.stop()
				checkpoint, err := readProbeCheckpoint(dir)
				if err != nil {
					t.Fatal(err)
				}
				access, _ := source.Access("a", time.Now())
				if checkpoint.PreviousCredential != access.HistoryCredential() {
					t.Fatal("checkpoint lost owner binding")
				}
				encoded, _ := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
				for _, secret := range []string{access.Token, access.AccountID, access.UserID, access.Registration, "synthetic-opaque", "synthetic-compact"} {
					if strings.Contains(string(encoded), secret) {
						t.Fatal("checkpoint exposes identity or content")
					}
				}
				if field == "legacy" {
					// Older checkpoints have no root binding and token-only compact owners.
					checkpoint.PreviousCredential = [32]byte{}
					for i := range checkpoint.Owners {
						checkpoint.Owners[i].Credential = sha256.Sum256([]byte(access.Token))
					}
					if writeProbeCheckpoint(dir, checkpoint) != nil {
						t.Fatal("legacy fixture write failed")
					}
				} else {
					source.change(field, false)
				}
				restored := startHistoryProbe(t, source, up.URL, dir)
				if restored.address != first.address || restored.secret != first.secret {
					t.Fatal("connection not restored")
				}
				if mode == "compact" {
					body = `{"input":[{"role":"user","content":"synthetic request"},{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-compact"},{"role":"user","content":"explicit new request"}]}`
				} else {
					body = `{"input":[{"role":"user","content":"explicit new request"},` + historyReasoning + `]}`
				}
				status := restored.send(t, "/responses", body)
				if mode == "compact" && field == "unchanged" {
					if status != 200 || calls.Load() != 2 {
						t.Fatal("verified compact ownership lost on restart")
					}
				} else if status == 200 || calls.Load() != 1 {
					t.Fatal("unverified checkpoint history dispatched")
				}
			})
		}
	}
}

func TestProbeReasoningRejectsUnknownItemID(t *testing.T) {
	var calls atomic.Int32
	up := historyUpstream(&calls)
	defer up.Close()
	p := startHistoryProbe(t, newHistoryAccess(), up.URL, t.TempDir())
	if p.send(t, "/responses", historyFirst) != 200 {
		t.Fatal("initial response failed")
	}
	body := strings.ReplaceAll(historyFollowup, "rs_synthetic", "rs_unobserved")
	if p.send(t, "/responses", body) != 409 || calls.Load() != 1 {
		t.Fatal("known ciphertext authorized an unobserved server item ID")
	}
}
