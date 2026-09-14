package directcli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

const sid = "12345678-1234-1234-1234-123456789abc"
const modelSecret = "synthetic-model-secret-1234567890123456"
const hookSecret = "synthetic-hook-secret-12345678901234567"

type access struct{}

func (access) Access(slot string, now time.Time) (accounts.Access, error) {
	return accounts.Access{Token: "synthetic-token", AccountID: "synthetic-account", ExpiresAt: now.Add(time.Hour)}, nil
}

func setup(t *testing.T) (*Bridge, *routing.Router, string) {
	t.Helper()
	dir := t.TempDir()
	origin, err := projectidentity.Resolve(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := routing.New(access{}, 90)
	d, _ := usage.Parse([]byte(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`))
	now := time.Now()
	r.Update([]usage.Snapshot{{Slot: "a", State: "ok", LastAttempt: now, LastSuccess: &now, Usage: &d}})
	b, err := New(r, origin, modelSecret, hookSecret)
	if err != nil {
		t.Fatal(err)
	}
	return b, r, dir
}
func request() *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1/responses", nil)
	r.Header.Set("X-Switcher-Run", modelSecret)
	r.Header.Set("Thread-Id", sid)
	r.Header.Set("Session-Id", sid)
	return r
}

func TestHookRegistrationWaitsForInputAndPinsAccount(t *testing.T) {
	b, r, dir := setup(t)
	ctx := context.Background()
	if _, err := b.Resolve(request()); err == nil {
		t.Fatal("headers alone registered session")
	}
	if err := b.event(ctx, Event{Name: "SessionStart", Session: sid, Directory: dir, Source: "startup"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Session(sid); ok {
		t.Fatal("startup allocated account before input")
	}
	if _, err := b.Resolve(request()); err == nil {
		t.Fatal("startup admitted model request")
	}
	if err := b.event(ctx, Event{Name: "UserPromptSubmit", Session: sid, Directory: dir}); err != nil {
		t.Fatal(err)
	}
	identity, err := b.Resolve(request())
	if err != nil || identity.Slot != "a" {
		t.Fatal(identity.Slot, err)
	}
	if err := b.event(ctx, Event{Name: "UserPromptSubmit", Session: sid, Directory: dir}); err != nil {
		t.Fatal(err)
	}
	if err := b.event(ctx, Event{Name: "SessionEnd", Session: sid, Directory: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Resolve(request()); err == nil {
		t.Fatal("ended session admitted")
	}
	if err := b.event(ctx, Event{Name: "SessionStart", Session: sid, Directory: dir, Source: "resume"}); err != nil {
		t.Fatal(err)
	}
	if err := b.event(ctx, Event{Name: "UserPromptSubmit", Session: sid, Directory: dir}); err != nil {
		t.Fatal(err)
	}
	identity, err = b.Resolve(request())
	if err != nil || identity.Slot != "a" {
		t.Fatal(err)
	}
}

func TestHookRejectsUnknownResumeForeignProjectAndWrongSecret(t *testing.T) {
	b, _, dir := setup(t)
	for _, e := range []Event{{"SessionStart", sid, dir, "resume"}, {"SessionStart", sid, t.TempDir(), "startup"}, {"UserPromptSubmit", sid, dir, ""}} {
		if b.event(context.Background(), e) == nil {
			t.Fatal("unsafe event accepted")
		}
	}
	data, _ := json.Marshal(Event{"SessionStart", sid, dir, "startup"})
	r := httptest.NewRequest("POST", "/control/cli-hook", strings.NewReader(string(data)))
	r.Header.Set("X-Switcher-Hook", modelSecret)
	w := httptest.NewRecorder()
	b.Hook(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "/control/cli-hook", strings.NewReader(string(data)))
	r.Header.Set("X-Switcher-Hook", hookSecret)
	w = httptest.NewRecorder()
	b.Hook(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestDecodeDropsPromptAndRejectsMalformedEvents(t *testing.T) {
	e, err := DecodeEvent(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"` + sid + `","cwd":"/synthetic","prompt":"synthetic-secret","transcript_path":"synthetic-private"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(e)
	if strings.Contains(string(data), "synthetic-secret") || strings.Contains(string(data), "transcript") {
		t.Fatal("private hook data retained")
	}
	for _, s := range []string{`{}`, `{"hook_event_name":"Stop","session_id":"` + sid + `","cwd":"/synthetic"}`, strings.Repeat("x", (1<<20)+1)} {
		if _, err := DecodeEvent(strings.NewReader(s)); err == nil {
			t.Fatal("invalid event accepted")
		}
	}
}

func TestProfileExclusiveAndCleanupPreservesChanges(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.toml")
	os.WriteFile(config, []byte("synthetic original"), 0600)
	cleanup, err := Install(dir, "/synthetic/helper", "http://127.0.0.1:8765", modelSecret, hookSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, "/synthetic/helper", "http://127.0.0.1:8765", modelSecret, hookSecret); err == nil {
		t.Fatal("overwrote existing profile")
	}
	profile, err := os.ReadFile(filepath.Join(dir, "switcher.config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(profile), "approval_policy") || strings.Contains(string(profile), "sandbox_mode") || strings.Contains(string(profile), hookSecret) {
		t.Fatal("profile changed permissions or leaked hook capability")
	}
	os.WriteFile(filepath.Join(dir, "switcher.config.toml"), []byte("user changed"), 0600)
	cleanup()
	if data, _ := os.ReadFile(config); string(data) != "synthetic original" {
		t.Fatal("base config altered")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "switcher.config.toml")); string(data) != "user changed" {
		t.Fatal("user change removed")
	}
}

func TestSendHookDoesNotForwardPromptOrFollowRedirect(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		data, _ := io.ReadAll(r.Body)
		if strings.Contains(string(data), "prompt") {
			t.Error("prompt transmitted")
		}
		w.Header().Set("Location", "http://127.0.0.1:1")
		w.WriteHeader(302)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "connection.json")
	data, _ := json.Marshal(Connection{server.URL, hookSecret})
	os.WriteFile(path, data, 0600)
	if SendHook(path, Event{"SessionStart", sid, "/synthetic", "startup"}) == nil || calls != 1 {
		t.Fatal("redirect or retry")
	}
	os.Chmod(path, 0644)
	if SendHook(path, Event{}) == nil || calls != 1 {
		t.Fatal("public connection accepted")
	}
}
