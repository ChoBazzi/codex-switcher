//go:build darwin && cgo

package clirun_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/cliprobe"
	"github.com/ChoBazzi/codex-switcher/internal/clirecord"
	"github.com/ChoBazzi/codex-switcher/internal/clirun"
	"github.com/ChoBazzi/codex-switcher/internal/clisession"
	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

type syntheticAccess struct{}

func (syntheticAccess) Access(slot string, now time.Time) (accounts.Access, error) {
	return accounts.Access{Token: "synthetic-token", AccountID: "synthetic-account", ExpiresAt: now.Add(time.Hour)}, nil
}

func TestInstalledCLI(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_INTEGRATION") != "1" {
		t.Skip("opt in to synthetic CLI test")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"success", "rate-limit", "partial", "resume", "resume_compact", "resume_logprobs"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := &cliprobe.Upstream{Scenario: scenario}
			resume := scenario == "resume" || scenario == "resume_compact" || scenario == "resume_logprobs"
			fixture.CompactCompletion = scenario == "resume_compact" || scenario == "resume_logprobs"
			fixture.EmptyLogprobs = scenario == "resume_logprobs"
			if resume {
				fixture.Scenario = "success"
			}
			up := httptest.NewServer(fixture)
			defer up.Close()
			storePath := filepath.Join(t.TempDir(), "db")
			store, err := affinity.Open(storePath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			router, err := routing.NewPersistent(syntheticAccess{}, 90, store)
			if err != nil {
				t.Fatal(err)
			}
			data, err := usage.Parse([]byte(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			router.Update([]usage.Snapshot{{Slot: "a", State: "ok", LastAttempt: now, LastSuccess: &now, Usage: &data}})
			work := t.TempDir()
			origin, err := projectidentity.Resolve(context.Background(), work)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := clisession.New(router, origin, "synthetic-run-secret", true, "")
			if err != nil {
				t.Fatal(err)
			}
			defer binding.Close()
			h, err := proxy.NewPersistent(up.URL, binding.Resolve, store)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			p := httptest.NewServer(h)
			defer p.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var out bytes.Buffer
			parent := t.TempDir()
			options := clirun.Options{Binary: binary, Parent: parent, Directory: work, Endpoint: p.URL, Secret: "synthetic-run-secret", Model: "switcher-synthetic", Prompt: "Synthetic protocol probe only. Do not use tools."}
			started := binding.Started
			var record *clirecord.Record
			if resume {
				record, err = clirecord.Create(filepath.Join(parent, "private"), origin, options.Model)
				if err != nil {
					t.Fatal(err)
				}
				options.Home = record.Home
				started = func(id string) error {
					if err := binding.Started(id); err != nil {
						return err
					}
					return record.Started(id)
				}
			}
			err = clirun.Run(ctx, options, started, &out)
			if (err == nil) != (scenario == "success" || resume) {
				t.Fatalf("unexpected run status: %v", err)
			}
			if fixture.Calls.Load() != 1 {
				t.Fatalf("upstream calls: %d", fixture.Calls.Load())
			}
			if scenario == "success" && out.Len() == 0 {
				t.Fatal("missing answer")
			}
			if resume {
				if err := record.Complete(); err != nil {
					t.Fatal(err)
				}
				handle := record.Handle
				record.Close()
				p.Close()
				h.Close()
				binding.Close()
				store.Close()
				store, err = affinity.Open(storePath)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				router, err = routing.NewPersistent(syntheticAccess{}, 90, store)
				if err != nil {
					t.Fatal(err)
				}
				now = time.Now()
				router.Update([]usage.Snapshot{{Slot: "a", State: "ok", LastAttempt: now, LastSuccess: &now, Usage: &data}})
				record, err = clirecord.Load(filepath.Join(parent, "private"), handle, origin)
				if err != nil {
					t.Fatal(err)
				}
				defer record.Close()
				binding2, err := clisession.New(router, origin, "synthetic-resume-secret", true, record.Origin().Session)
				if err != nil {
					t.Fatal(err)
				}
				defer binding2.Close()
				h2, err := proxy.NewPersistent(up.URL, binding2.Resolve, store)
				if err != nil {
					t.Fatal(err)
				}
				defer h2.Close()
				p2 := httptest.NewServer(h2)
				defer p2.Close()
				options.Endpoint, options.Secret, options.Resume = p2.URL, "synthetic-resume-secret", record.Origin().Session
				if err := record.Begin(); err != nil {
					t.Fatal(err)
				}
				err = clirun.Run(ctx, options, func(id string) error {
					t.Logf("synthetic resume id_matches=%t store_ready=%t", id == record.Origin().Session, store.Ready(record.Origin()) == nil)
					return binding2.Started(id)
				}, &out)
				if err != nil {
					t.Fatalf("resume failed stage=%s diagnostic=%+v", clirun.FailureStage(err), h2.Diagnostics())
				}
				if fixture.Calls.Load() != 2 {
					t.Fatal("resume not forwarded exactly once")
				}
				if _, err := os.Stat(filepath.Join(options.Home, "switcher-live-test.config.toml")); !os.IsNotExist(err) {
					t.Fatal("profile secret retained")
				}
				if err := filepath.Walk(options.Home, func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					if info.IsDir() {
						return nil
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if bytes.Contains(data, []byte("synthetic-resume-secret")) || bytes.Contains(data, []byte("synthetic-run-secret")) {
						t.Error("run secret retained in CLI files")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				return
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatal("private run data not cleaned")
			}
		})
	}
}
