//go:build darwin && cgo

package clirun_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/cliprobe"
	"github.com/ChoBazzi/codex-switcher/internal/clirun"
	"github.com/ChoBazzi/codex-switcher/internal/clisession"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
	"github.com/ChoBazzi/codex-switcher/internal/wikidraft"
)

func TestInstalledCLIHandoffWikiOnly(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("synthetic CLI opt-in")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	db, err := affinity.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	router, err := routing.NewPersistent(syntheticAccess{}, 90, db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	data, _ := usage.Parse([]byte(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`))
	for _, slot := range []string{"a", "b"} {
		router.Update([]usage.Snapshot{{Slot: slot, State: "ok", LastAttempt: now, LastSuccess: &now, Usage: &data}})
	}
	origin := checkpoint.Origin{Project: "p", Worktree: "w", Branch: "b", Session: "11111111-1111-4111-8111-111111111111"}
	if _, err := router.Register(origin, true, now); err != nil {
		t.Fatal(err)
	}
	meta := checkpoint.Metadata{ID: "synthetic-checkpoint", Origin: origin}
	snap, err := checkpoint.CheckBytes(checkpoint.Format(meta, "synthetic-wiki-goal", "none", "none", "next"), meta)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := router.PrepareHandoff(origin, "b", snap, true, now)
	if err != nil {
		t.Fatal(err)
	}
	newOrigin := origin
	newOrigin.Session = ""
	binding, err := clisession.NewHandoff(router, newOrigin, "synthetic-handoff-secret", reservation.ID, snap, true)
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Close()
	fixture := &cliprobe.Upstream{Scenario: "success"}
	var checked atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		text := string(body)
		if strings.Contains(text, origin.Session) || strings.Contains(text, "checkpoint-complete:") || strings.Contains(text, "codex-switcher:") || !strings.Contains(text, "synthetic-wiki-goal") || !strings.Contains(text, "synthetic-new-user-input") {
			t.Error("handoff prompt boundary violated")
		}
		checked.Store(true)
		r.Body = io.NopCloser(bytes.NewReader(body))
		fixture.ServeHTTP(w, r)
	}))
	defer up.Close()
	h, err := proxy.NewPersistent(up.URL, binding.Resolve, db)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	local := httptest.NewServer(h)
	defer local.Close()
	prompt, err := wikidraft.HandoffPrompt(snap, "synthetic-new-user-input; do not use tools")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var targetID string
	err = clirun.Run(ctx, clirun.Options{Binary: binary, Parent: t.TempDir(), Directory: t.TempDir(), Endpoint: local.URL, Secret: "synthetic-handoff-secret", Model: "switcher-synthetic", Prompt: prompt}, func(id string) error { targetID = id; return binding.Started(id) }, io.Discard)
	if err != nil {
		t.Fatalf("handoff failed: %v %+v", err, h.Diagnostics())
	}
	s, err := db.Lookup(targetID)
	if err != nil || s.Account != "b" || targetID == origin.Session || !checked.Load() || fixture.Calls.Load() != 1 {
		t.Fatal("target binding failed")
	}
	old, _ := db.Lookup(origin.Session)
	if old.Account != "a" {
		t.Fatal("source moved")
	}
}
