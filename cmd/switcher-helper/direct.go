package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
	"github.com/ChoBazzi/codex-switcher/internal/directcli"
	"github.com/ChoBazzi/codex-switcher/internal/livetest"
	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

// Use 256 random bits explicitly: rand.Text currently yields only 26 characters,
// below the direct bridge's minimum encoded length of 32.
func newDirectSecret() string {
	var entropy [32]byte
	rand.Read(entropy[:])
	return base64.RawURLEncoding.EncodeToString(entropy[:])
}

func directHook(args []string, input io.Reader, output io.Writer) error {
	f := flag.NewFlagSet("direct-hook", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	connection := f.String("connection", "", "private local connection file")
	if f.Parse(args) != nil || f.NArg() != 0 || !filepath.IsAbs(*connection) {
		return directcli.ErrSession
	}
	e, err := directcli.DecodeEvent(input)
	if err == nil {
		err = directcli.SendHook(*connection, e)
	}
	if err != nil {
		// Return a fixed hook decision, never raw paths, prompts or credentials.
		_ = json.NewEncoder(output).Encode(map[string]any{"continue": false, "stopReason": "Switcher local session registration failed. Check proxy and /hooks."})
		return directcli.ErrSession
	}
	return json.NewEncoder(output).Encode(map[string]any{})
}

// This command starts only a server. It never launches Codex or sends a model request.
func directProxy(args []string, output io.Writer) error {
	f := flag.NewFlagSet("direct-proxy", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	dir := f.String("C", ".", "allowed project directory")
	profileDir := f.String("profile-dir", "", "existing CODEX_HOME, default ~/.codex")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return errors.New("direct_proxy_arguments_invalid")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	origin, err := projectidentity.Resolve(ctx, *dir)
	if err != nil {
		return err
	}
	if *profileDir == "" {
		*profileDir = os.Getenv("CODEX_HOME")
		if *profileDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return directcli.ErrSession
			}
			*profileDir = filepath.Join(home, ".codex")
		}
	}
	*profileDir, err = filepath.Abs(*profileDir)
	if err != nil {
		return directcli.ErrSession
	}
	helper, err := os.Executable()
	if err != nil {
		return directcli.ErrSession
	}
	parent, err := os.UserConfigDir()
	if err != nil {
		return affinity.ErrStorage
	}
	store, err := affinity.Open(filepath.Join(parent, "com.bazzi.codex-switcher", "affinity"))
	if err != nil {
		return err
	}
	defer store.Close()
	access := accounts.New(credentialstore.New(), "")
	router, err := routing.NewPersistent(access, 90, store)
	if err != nil {
		return err
	}
	client := usage.NewClient()
	defer client.Close()
	monitor := usage.NewMonitor(access, client)
	router.Update(monitor.Refresh(ctx, accountslot.All()))
	pollCtx, stopPoll := context.WithCancel(ctx)
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		ticker := time.NewTicker(usage.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				router.Update(monitor.Refresh(pollCtx, accountslot.All()))
			}
		}
	}()
	defer func() { stopPoll(); <-pollDone }()
	modelSecret, hookSecret := newDirectSecret(), newDirectSecret()
	bridge, err := directcli.New(router, origin, modelSecret, hookSecret)
	if err != nil {
		return err
	}
	h, err := proxy.NewPersistent(livetest.Upstream, bridge.Resolve, store)
	if err != nil {
		return err
	}
	defer h.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("direct_proxy_listen_failed")
	}
	defer listener.Close()
	cleanup, err := directcli.Install(*profileDir, helper, "http://"+listener.Addr().String(), modelSecret, hookSecret)
	if err != nil {
		return errors.New("direct_profile_install_failed_existing_or_inaccessible")
	}
	defer cleanup()
	mux := http.NewServeMux()
	mux.HandleFunc("/control/cli-hook", bridge.Hook)
	var diagnosticMu sync.Mutex
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			diagnosticMu.Lock()
			defer diagnosticMu.Unlock()
			_ = json.NewEncoder(output).Encode(struct {
				Event string `json:"event"`
				proxy.Diagnostics
			}{"direct_proxy_request_finished", h.Diagnostics()})
		}()
		h.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if err := json.NewEncoder(output).Encode(map[string]any{"event": "direct_proxy_ready", "command": "codex -p switcher", "hook_review_required": true, "model_requests": 0}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	case <-done:
		return errors.New("direct_proxy_stopped")
	}
}
