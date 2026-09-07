package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/clirecord"
	"github.com/ChoBazzi/codex-switcher/internal/clirun"
	"github.com/ChoBazzi/codex-switcher/internal/clisession"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
	"github.com/ChoBazzi/codex-switcher/internal/livetest"
	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
	"github.com/ChoBazzi/codex-switcher/internal/wikidraft"
)

func execCommand(args []string, output, diagnostics io.Writer) error {
	f := flag.NewFlagSet("exec", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	directory := f.String("C", ".", "project directory")
	model := f.String("model", "", "model override")
	resume := f.String("resume", "", "explicit local conversation handle")
	handoffID := f.String("handoff", "", "explicit prepared handoff; requires new user input")
	wiki := f.Bool("checkpoint", false, "request a Wiki from the resumed conversation")
	if f.Parse(args) != nil {
		return errors.New("usage: switcher-helper exec [-C DIRECTORY] [--model MODEL] [--resume HANDLE] [--checkpoint | PROMPT]")
	}
	handoffSpecified := false
	f.Visit(func(value *flag.Flag) {
		if value.Name == "handoff" {
			handoffSpecified = true
		}
	})
	if handoffSpecified && *handoffID == "" {
		return errors.New("handoff_id_required")
	}
	if (*wiki && (f.NArg() != 0 || *resume == "")) || (!*wiki && (f.NArg() != 1 || strings.TrimSpace(f.Arg(0)) == "" || len(f.Arg(0)) > 1<<20)) {
		return errors.New("usage: switcher-helper exec [-C DIRECTORY] [--model MODEL] PROMPT")
	}
	if *resume != "" && (!clirecord.ValidHandle(*resume) || *model != "") {
		return clirecord.ErrRecord
	}
	if *handoffID != "" && (*resume != "" || *wiki) {
		return errors.New("handoff_requires_new_conversation")
	}
	if _, err := livetest.Profile("http://127.0.0.1:1", "validation-only-secret", *model); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dir, err := filepath.Abs(*directory)
	if err != nil {
		return projectidentity.ErrIdentity
	}
	origin, err := projectidentity.Resolve(ctx, dir)
	if err != nil {
		return err
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		return errors.New("codex_executable_not_found")
	}
	parent, err := os.UserConfigDir()
	if err != nil {
		return affinity.ErrStorage
	}
	// One owner until the shared helper daemon is introduced.
	store, err := affinity.Open(filepath.Join(parent, "com.bazzi.codex-switcher", "affinity"))
	if err != nil {
		return err
	}
	defer store.Close()
	var handoffSnapshot checkpoint.Snapshot
	if *handoffID != "" {
		reservation, err := store.Handoff(*handoffID)
		if err != nil {
			return err
		}
		if reservation.ConsumedBy != "" || reservation.Source.Project != origin.Project || reservation.Source.Worktree != origin.Worktree || reservation.Source.Branch != origin.Branch {
			return affinity.ErrConflict
		}
		handoffSnapshot, err = wikidraft.ReadPin(filepath.Join(parent, "com.bazzi.codex-switcher"), reservation)
		if err != nil {
			return err
		}
	}
	recordParent := filepath.Join(parent, "com.bazzi.codex-switcher", "conversations")
	var record *clirecord.Record
	if *resume == "" {
		record, err = clirecord.Create(recordParent, origin, *model)
	} else {
		record, err = clirecord.Load(recordParent, *resume, origin)
	}
	if err != nil {
		return err
	}
	defer record.Close()
	resumeThread := record.Origin().Session
	if resumeThread != "" {
		s, err := store.Lookup(resumeThread)
		if err != nil || s.Origin != record.Origin() {
			return clirecord.ErrRecord
		}
		if err := store.Ready(s.Origin); err != nil {
			return err
		}
	}
	access := accounts.New(credentialstore.New(), "")
	router, err := routing.NewPersistent(access, 90, store)
	if err != nil {
		return err
	}
	client := usage.NewClient()
	defer client.Close()
	monitor := usage.NewMonitor(access, client)
	ready, pollDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(pollDone)
		first := true
		monitor.Run(ctx, []string{"a", "b"}, func(samples []usage.Snapshot) error {
			router.Update(samples)
			if first {
				close(ready)
				first = false
			}
			return nil
		})
	}()
	defer func() { cancel(); <-pollDone }()
	select {
	case <-ready:
	case <-ctx.Done():
		return clirun.ErrRun
	}
	secret := rand.Text()
	if resumeThread != "" {
		if _, err := router.Resolve(record.Origin(), time.Now()); err != nil {
			return err
		}
	}
	var binding *clisession.Binding
	if *handoffID != "" {
		binding, err = clisession.NewHandoff(router, origin, secret, *handoffID, handoffSnapshot, true)
	} else {
		binding, err = clisession.New(router, origin, secret, true, resumeThread)
	}
	if err != nil {
		return err
	}
	defer binding.Close()
	h, err := proxy.NewPersistent(livetest.Upstream, func(r *http.Request) (proxy.Identity, error) {
		current, err := projectidentity.Resolve(r.Context(), dir)
		if err != nil || current != origin {
			return proxy.Identity{}, projectidentity.ErrIdentity
		}
		return binding.Resolve(r)
	}, store)
	if err != nil {
		return err
	}
	defer h.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("cli_proxy_listen_failed")
	}
	defer listener.Close()
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	defer server.Close()
	prompt := f.Arg(0)
	if *handoffID != "" {
		prompt, err = wikidraft.HandoffPrompt(handoffSnapshot, prompt)
		if err != nil {
			return err
		}
		if len(prompt) > 1<<20 {
			return errors.New("handoff_prompt_too_large")
		}
	}
	var draft *wikidraft.Draft
	if *wiki {
		draft, err = wikidraft.New(record.Home, record.Origin())
		if err != nil {
			return err
		}
		defer draft.Close()
		prompt, output = draft.Prompt(), draft
	}
	started := func(id string) error {
		if err := binding.Started(id); err != nil {
			return err
		}
		if err := record.Started(id); err != nil {
			return err
		}
		session, ok := router.Session(id)
		if !ok {
			return clisession.ErrBinding
		}
		return json.NewEncoder(diagnostics).Encode(map[string]string{"event": "cli_session_started", "slot": session.Account, "conversation": record.Handle})
	}
	if err := record.Begin(); err != nil {
		return err
	}
	if draft != nil {
		var stopWiki context.CancelFunc
		ctx, stopWiki = context.WithCancel(ctx)
		defer stopWiki()
		wikiCtx := ctx
		h.BeforeAttempt = func() error {
			if err := draft.MarkSent(); err != nil {
				return err
			}
			go func() {
				deadline := time.NewTimer(checkpoint.Timeout)
				defer deadline.Stop()
				select {
				case <-deadline.C:
					stopWiki()
				case <-wikiCtx.Done():
				}
			}()
			return nil
		}
	}
	go func() {
		if server.Serve(listener) != http.ErrServerClosed {
			cancel()
		}
	}()
	runErr := clirun.Run(ctx, clirun.Options{Binary: binary, Home: record.Home, Resume: resumeThread, Directory: dir, Endpoint: "http://" + listener.Addr().String(), Secret: secret, Model: record.Model(), Prompt: prompt}, started, output)
	// Drain request handlers before sampling counters. No request is replayed.
	shutdown, stopShutdown := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopShutdown()
	if server.Shutdown(shutdown) != nil {
		server.Close()
	}
	if draft != nil {
		// An expired deadline may be rechecked; user cancellation may not.
		cancelled := ctx.Err() != nil && time.Now().Before(draft.Deadline())
		if err := draft.Retain(cancelled); err != nil {
			return err
		}
		if err := json.NewEncoder(diagnostics).Encode(map[string]any{"event": "checkpoint_candidate", "checkpoint_id": draft.ID(), "conversation": record.Handle, "switch_ready": false}); err != nil {
			return err
		}
	}
	if runErr == nil {
		if err := store.Ready(record.Origin()); err != nil {
			runErr = err
		} else {
			runErr = record.Complete()
		}
	}
	if err := writeExecDiagnostics(diagnostics, h.Diagnostics(), runErr); err != nil && runErr == nil {
		return errors.New("cli_diagnostics_output_failed")
	}
	if draft != nil && runErr == nil {
		current, err := projectidentity.Resolve(ctx, dir)
		if err != nil || current != origin {
			return projectidentity.ErrIdentity
		}
		snap, err := draft.Publish(dir)
		if err != nil {
			return err
		}
		if err := backupCheckpoint(snap); err != nil {
			return err
		}
		return json.NewEncoder(diagnostics).Encode(map[string]any{"event": "checkpoint_saved", "checkpoint_id": snap.Metadata.ID, "sha256": snap.SHA256, "backup_saved": true, "switch_ready": false})
	}
	return runErr
}

func writeExecDiagnostics(out io.Writer, d proxy.Diagnostics, runErr error) error {
	return json.NewEncoder(out).Encode(struct {
		Event string `json:"event"`
		proxy.Diagnostics
		Stage     string `json:"cli_stage"`
		Succeeded bool   `json:"succeeded"`
	}{"cli_run_finished", d, clirun.FailureStage(runErr), runErr == nil})
}
