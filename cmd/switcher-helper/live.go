package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/applock"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
	"github.com/ChoBazzi/codex-switcher/internal/livetest"
)

func liveCommand(args []string, out, diagnostics io.Writer) error {
	if len(args) != 1 && len(args) != 3 {
		return errors.New("usage: switcher-helper live-test a|b|c|d|e [--model MODEL]")
	}
	if !accountslot.Valid(args[0]) || len(args) == 3 && args[1] != "--model" {
		return errors.New("invalid_live_test_arguments")
	}
	model := ""
	if len(args) == 3 {
		model = args[2]
	}
	if _, err := livetest.Profile("http://127.0.0.1:1", "validation-only-secret", model); err != nil {
		return err
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		return errors.New("codex_executable_not_found")
	}
	parent, err := os.UserConfigDir()
	if err != nil {
		return applock.ErrStorage
	}
	dir := filepath.Join(parent, "com.bazzi.codex-switcher")
	lock, err := applock.Acquire(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	access, err := accounts.New(credentialstore.New(), dir).Access(args[0], time.Now())
	if err != nil {
		return err
	}
	secret := rand.Text()
	b, err := livetest.New(livetest.Upstream, secret, access)
	if err != nil {
		return errors.New("live_proxy_setup_failed")
	}
	defer b.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("live_proxy_listen_failed")
	}
	server := &http.Server{Handler: b, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	go server.Serve(listener)
	defer server.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	fmt.Fprintln(diagnostics, "실제 계정으로 인사 요청 1회를 보냅니다. 계정 사용량이 소모됩니다.")
	answer, runErr := livetest.Run(ctx, binary, dir, "http://"+listener.Addr().String(), secret, model)
	if runErr == nil && (b.Requests.Load() != 1 || b.Forwarded.Load() != 1 || b.LastStatus.Load() < 200 || b.LastStatus.Load() >= 300) {
		runErr = errors.New("live_proxy_path_not_verified")
	}
	json.NewEncoder(diagnostics).Encode(map[string]any{"event": "live_test_finished", "slot": args[0], "cli_requests": b.Requests.Load(), "proxy_admissions": b.Forwarded.Load(), "last_http_status": b.LastStatus.Load(), "local_rejection_code": b.RejectionCode(), "succeeded": runErr == nil})
	if runErr != nil {
		return runErr
	}
	_, err = fmt.Fprintln(out, answer)
	return err
}
