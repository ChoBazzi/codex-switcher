package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

// Use the real account manager with a synthetic vault/exchange and advance only
// its credential checks. No wall-clock sleeps, real Keychain, or OAuth calls.
type refreshHistoryVault struct {
	mu   sync.Mutex
	data []byte
	fail bool
}

func (v *refreshHistoryVault) Read() ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]byte(nil), v.data...), nil
}
func (v *refreshHistoryVault) Write(data []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fail {
		return credentialstore.ErrUnavailable
	}
	v.data = append([]byte(nil), data...)
	return nil
}

type refreshHistoryExchange struct {
	next    accounts.Credentials
	calls   atomic.Int32
	failure string
	vault   *refreshHistoryVault
	entered chan struct{}
	release chan struct{}
}

func (f *refreshHistoryExchange) Refresh(context.Context, accounts.Credentials) (accounts.Credentials, error) {
	f.calls.Add(1)
	if f.entered != nil {
		close(f.entered)
		<-f.release
	}
	if f.failure == "network" {
		return accounts.Credentials{}, accounts.ErrRefresh
	}
	if f.failure == "commit" {
		f.vault.mu.Lock()
		f.vault.fail = true
		f.vault.mu.Unlock()
	}
	return f.next, nil
}

type refreshHistorySource struct {
	*accounts.Manager
	elapsed  atomic.Int64
	vault    *refreshHistoryVault
	exchange *refreshHistoryExchange
	dir      string
}

func (s *refreshHistorySource) Access(slot string, now time.Time) (accounts.Access, error) {
	return s.Manager.Access(slot, now.Add(time.Duration(s.elapsed.Load())))
}
func (s *refreshHistorySource) RequestAccess(slot string, now time.Time) (accounts.Access, error) {
	return s.Manager.RequestAccess(slot, now.Add(time.Duration(s.elapsed.Load())))
}
func refreshHistoryCredentials(t *testing.T, account, user string, expires time.Time) accounts.Credentials {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{"exp": expires.Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account, "chatgpt_user_id": user}})
	token := "synthetic." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
	data, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"access_token": token, "refresh_token": "synthetic-refresh", "id_token": "synthetic-id", "account_id": account}})
	c, err := accounts.ParseAuth(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func newRefreshHistorySource(t *testing.T) *refreshHistorySource {
	t.Helper()
	old := refreshHistoryCredentials(t, "synthetic-account", "synthetic-user", time.Now().Add(time.Hour))
	other := refreshHistoryCredentials(t, "synthetic-other-account", "synthetic-other-user", time.Now().Add(4*time.Hour))
	v := &refreshHistoryVault{}
	v.data, _ = json.Marshal(map[string]any{"version": 1, "accounts": []any{
		map[string]any{"slot": "a", "credentials": old, "registration": "synthetic-registration"},
		map[string]any{"slot": "b", "credentials": other, "registration": "synthetic-other-registration"},
	}})
	f := &refreshHistoryExchange{vault: v, next: refreshHistoryCredentials(t, "synthetic-account", "synthetic-user", time.Now().Add(4*time.Hour))}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return &refreshHistorySource{Manager: accounts.NewRefreshing(v, dir, f), vault: v, exchange: f, dir: dir}
}

func refreshHistoryRequest(p *historyProbe, t *testing.T, mode string, first bool) int {
	t.Helper()
	body, path := historyFollowup, "/responses"
	if mode == "compact" {
		body = historyCompact
		if first {
			path = "/responses/compact"
		}
	}
	if first {
		body = historyFirst
	}
	if mode == "auxiliary" {
		return p.sendThread(t, path, body, "23456789-2345-4345-8345-234567890123")
	}
	return p.send(t, path, body)
}

func TestProbeRefreshPrevalidation(t *testing.T) {
	for _, mode := range []string{"reasoning", "compact"} {
		t.Run(mode, func(t *testing.T) {
			source := newRefreshHistorySource(t)
			owner, err := probeHistoryCredential(source, "a")
			if err != nil {
				t.Fatal(err)
			}
			var reasoning probeReasoningOwners
			var item map[string]json.RawMessage
			_ = json.Unmarshal([]byte(historyReasoning), &item)
			if !reasoning.accept(map[[32]byte]bool{probeReasoningKey(item): true}, owner, [32]byte{}) {
				t.Fatal("initial reasoning owner unavailable")
			}
			compact := probeCompactRegistry{}
			if err := compact.accept([]byte(`{"object":"response.compaction","output":[{"role":"user","content":"synthetic request"},{"type":"compaction","id":"cmp_synthetic","encrypted_content":"synthetic-compact"}]}`), "a", owner); err != nil {
				t.Fatal(err)
			}
			source.elapsed.Store(int64(2 * time.Hour))
			if _, err := source.Access("a", time.Now()); err == nil {
				t.Fatal("fixture authentication did not expire")
			}
			credential, err := probeHistoryCredential(source, "a")
			if err != nil {
				t.Fatal(err)
			}
			if credential != owner || credential == ([32]byte{}) || source.exchange.calls.Load() != 0 {
				t.Fatal("local prevalidation lost ownership or exchanged credentials")
			}
			body := historyFollowup
			if mode == "compact" {
				body = historyCompact
			}
			allowReasoning := func(item map[string]json.RawMessage) bool { return reasoning.permits(item, credential) }
			allowCompact := func(item map[string]json.RawMessage) bool { return compact.permits(item, "a", credential) }
			if _, err := probeToolBodyWithOwnership([]byte(body), "a", "a", "synthetic-salt", allowCompact, allowReasoning); err != nil {
				t.Fatal("expired credential blocked history before dispatch refresh")
			}
			access, err := usage.RequestAccess(source, "a", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if access.HistoryCredential() != owner || source.exchange.calls.Load() != 1 {
				t.Fatal("dispatch refresh changed verified ownership")
			}
			credential = access.HistoryCredential()
			if _, err := probeToolBodyWithOwnership([]byte(body), "a", "a", "synthetic-salt", allowCompact, allowReasoning); err != nil {
				t.Fatal("refreshed credential lost observed history")
			}
			unknown := strings.ReplaceAll(body, "synthetic-opaque", "synthetic-unobserved")
			unknown = strings.ReplaceAll(unknown, "synthetic-compact", "synthetic-unobserved")
			if _, err := probeToolBodyWithOwnership([]byte(unknown), "a", "a", "synthetic-salt", allowCompact, allowReasoning); err == nil {
				t.Fatal("refresh authorized unobserved history")
			}
		})
	}
}

func TestProbeVerifiedRefreshContinuity(t *testing.T) {
	for _, mode := range []string{"reasoning", "compact", "auxiliary"} {
		for _, trigger := range []string{"dispatch", "usage"} {
			for _, age := range []time.Duration{59 * time.Minute, 2 * time.Hour} {
				t.Run(mode+"/"+trigger+"/"+age.String(), func(t *testing.T) {
					source := newRefreshHistorySource(t)
					var calls atomic.Int32
					up := historyUpstream(&calls, func(r *http.Request, body []byte) {
						if source.elapsed.Load() == 0 {
							return
						}
						if r.Header.Get("Authorization") != "Bearer "+source.exchange.next.AccessToken {
							t.Error("followup did not use renewed authentication")
						}
						opaque := "synthetic-opaque"
						if mode == "compact" {
							opaque = "synthetic-compact"
						}
						if !strings.Contains(string(body), opaque) {
							t.Error("verified history was dropped")
						}
					})
					defer up.Close()
					p := startHistoryProbe(t, source, up.URL, t.TempDir())
					if refreshHistoryRequest(p, t, mode, true) != 200 {
						t.Fatal("initial request failed")
					}
					source.elapsed.Store(int64(age))
					if trigger == "usage" {
						if _, err := usage.RequestAccess(source, "a", time.Now()); err != nil {
							t.Fatal(err)
						}
					}
					if refreshHistoryRequest(p, t, mode, false) != 200 || calls.Load() != 2 || source.exchange.calls.Load() != 1 {
						t.Fatal("verified refresh did not preserve history with one exchange/dispatch")
					}
				})
			}
		}
	}
}

func TestProbeRefreshFailureNeverDispatchesOrReplays(t *testing.T) {
	for _, mode := range []string{"reasoning", "compact", "auxiliary"} {
		for _, failure := range []string{"network", "account", "user", "commit"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				source := newRefreshHistorySource(t)
				source.exchange.failure = failure
				if failure == "account" || failure == "user" {
					account, user := "synthetic-account", "synthetic-user"
					if failure == "account" {
						account += "-other"
					} else {
						user += "-other"
					}
					source.exchange.next = refreshHistoryCredentials(t, account, user, time.Now().Add(4*time.Hour))
				}
				var calls atomic.Int32
				up := historyUpstream(&calls)
				defer up.Close()
				p := startHistoryProbe(t, source, up.URL, t.TempDir())
				if refreshHistoryRequest(p, t, mode, true) != 200 {
					t.Fatal("initial request failed")
				}
				source.elapsed.Store(int64(2 * time.Hour))
				if refreshHistoryRequest(p, t, mode, false) == 200 {
					t.Fatal("failed refresh dispatched")
				}
				if refreshHistoryRequest(p, t, mode, false) != 409 || calls.Load() != 1 || source.exchange.calls.Load() != 1 {
					t.Fatal("failed model request or refresh replayed")
				}
				restarted := accounts.NewRefreshing(source.vault, source.dir, source.exchange)
				if _, err := restarted.RequestAccess("a", time.Now()); err == nil || source.exchange.calls.Load() != 1 {
					t.Fatal("failed refresh replayed after manager restart")
				}
			})
		}
	}
}

func TestProbeRefreshedCompactCheckpoint(t *testing.T) {
	for _, timing := range []string{"before_restart", "after_restart"} {
		t.Run(timing, func(t *testing.T) {
			source := newRefreshHistorySource(t)
			var calls atomic.Int32
			up := historyUpstream(&calls)
			defer up.Close()
			dir := t.TempDir()
			first := startHistoryProbe(t, source, up.URL, dir)
			if refreshHistoryRequest(first, t, "compact", true) != 200 {
				t.Fatal("initial compact failed")
			}
			first.stop()
			if timing == "before_restart" {
				if _, err := source.Manager.RequestAccess("a", time.Now().Add(2*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			source.Manager = accounts.NewRefreshing(source.vault, source.dir, source.exchange)
			restored := startHistoryProbe(t, source, up.URL, dir)
			source.elapsed.Store(int64(2 * time.Hour))
			// Restart requires explicit new user input; the compact owner persists.
			body := strings.TrimSuffix(historyCompact, "]}") + `,{"role":"user","content":"explicit new request"}]}`
			if restored.send(t, "/responses", body) != 200 || calls.Load() != 2 || source.exchange.calls.Load() != 1 {
				t.Fatal("verified compact ownership lost across manager/proxy restart")
			}
		})
	}
}

func TestProbeStatusAvailableDuringRefresh(t *testing.T) {
	source := newRefreshHistorySource(t)
	source.exchange.entered, source.exchange.release = make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	up := historyUpstream(&calls)
	defer up.Close()
	p := startHistoryProbe(t, source, up.URL, t.TempDir())
	var once sync.Once
	unblock := func() { once.Do(func() { close(source.exchange.release) }) }
	defer unblock()
	if p.send(t, "/responses", historyFirst) != 200 {
		t.Fatal("initial request failed")
	}
	source.elapsed.Store(int64(2 * time.Hour))
	done := make(chan int, 1)
	go func() { done <- p.send(t, "/responses", historyFollowup) }()
	select {
	case <-source.exchange.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	if _, err := io.WriteString(p.commands, "{\"action\":\"status\"}\n"); err != nil {
		t.Fatal(err)
	}
	// A distinct shutdown rejection proves the control loop ran while OAuth
	// was blocked, instead of merely consuming the queued request-start event.
	if _, err := io.WriteString(p.commands, "{\"action\":\"shutdown\"}\n"); err != nil {
		t.Fatal(err)
	}
	if p.next(t, "probe_shutdown")["accepted"] != false {
		t.Fatal("shutdown interrupted active refresh")
	}
	unblock()
	select {
	case status := <-done:
		if status != 200 || calls.Load() != 2 {
			t.Fatal("refresh continuation failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
	}
}
