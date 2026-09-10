package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/sessionstatus"
)

func statusCommand(args []string) error {
	return statusCommandWithIO(args, os.Stdin, os.Stdout)
}

func statusCommandWithIO(args []string, input io.Reader, output io.Writer) error {
	f := flag.NewFlagSet("status-server", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	port := f.Int("port", 8766, "loopback status API port")
	managed := f.Bool("managed", false, "stop when owning app closes stdin")
	if f.Parse(args) != nil || f.NArg() != 0 || *port < 0 || *port > 65535 {
		return errors.New("status_arguments_invalid")
	}
	secret := os.Getenv("SWITCHER_CONTROL_TOKEN")
	if len(secret) < 32 {
		return errors.New("status_control_token_required_min_32")
	}
	parent, err := os.UserConfigDir()
	if err != nil {
		return sessionstatus.ErrStatus
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)))
	if err != nil {
		return errors.New("status_listen_failed")
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *managed {
		go func() { _, _ = io.Copy(io.Discard, input); stop() }()
	}
	dir := filepath.Join(parent, "com.bazzi.codex-switcher", "session-status")
	cleanup := func() {
		if sessionstatus.Cleanup(dir, time.Now().UTC()) != nil {
			fmt.Fprintln(os.Stderr, "session_status_cleanup_failed")
		}
	}
	cleanup()
	server := &http.Server{Handler: sessionstatus.Handler(dir, secret), ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if *managed {
		if err := json.NewEncoder(output).Encode(struct {
			Event string `json:"event"`
			Port  int    `json:"port"`
		}{"status_server_ready", listener.Addr().(*net.TCPAddr).Port}); err != nil {
			return errors.New("status_ready_output_failed")
		}
	}
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			return errors.New("status_server_stopped")
		case <-tick.C:
			cleanup()
		}
	}
}
