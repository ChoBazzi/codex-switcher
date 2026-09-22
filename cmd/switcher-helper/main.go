// switcher-helper currently exposes a synthetic loopback demo only.
package main

import (
	"context"
	"crypto/subtle"
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
	"strconv"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
	"github.com/ChoBazzi/codex-switcher/internal/cliprobe"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "proxy-stop" {
		f := flag.NewFlagSet("proxy-stop", flag.ContinueOnError)
		newSession := f.Bool("new-session", false, "retire the selected connection while stopping all connections")
		connection := f.String("connection", "1", "connection number (1-5)")
		if f.Parse(os.Args[2:]) != nil || f.NArg() != 0 || !validConnectionID(*connection) {
			fmt.Fprintln(os.Stderr, "usage: proxy-stop [--new-session] [--connection 1-5]")
			os.Exit(1)
		}
		if err := proxyStopConnection(*newSession, *connection); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) == 2 && (os.Args[1] == "proxy-connect" || os.Args[1] == "proxy-daemon") {
		var err error
		if os.Args[1] == "proxy-connect" {
			err = proxyConnect(os.Stdin, os.Stdout)
		} else {
			err = proxyDaemon()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "proxy_service_unavailable")
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "switch-probe" {
		if err := switchProbe(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "direct-hook" {
		if err := directHook(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "direct-proxy" {
		if err := directProxy(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "status-server" {
		if err := statusCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "handoff" {
		if err := handoffCommand(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "checkpoint" {
		if err := checkpointCommand(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "exec" {
		if err := execCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "project" {
		if err := projectCommand(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "usage" {
		if err := usageCommand(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "live-test" {
		if err := liveCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "account" {
		if err := accountCommand(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	demo := flag.Bool("demo", false, "run synthetic upstream; no real accounts")
	port := flag.Int("port", 8765, "loopback port (0 selects an available port)")
	scenario := flag.String("demo-scenario", "success", "synthetic response: success, rate-limit, server-error, partial")
	flag.Parse()
	if !*demo {
		fmt.Fprintln(os.Stderr, "Only --demo is available; live Codex integration is not implemented.")
		os.Exit(2)
	}
	secret := os.Getenv("SWITCHER_CONTROL_TOKEN")
	if len(secret) < 16 || *port < 0 || *port > 65535 || !cliprobe.ValidScenario(*scenario) {
		fmt.Fprintln(os.Stderr, "Set SWITCHER_CONTROL_TOKEN (at least 16 characters) and a valid port.")
		os.Exit(2)
	}
	if err := run(*port, secret, *scenario); err != nil {
		fmt.Fprintln(os.Stderr, "helper_start_or_shutdown_failed")
		os.Exit(1)
	}
}

func run(port int, secret, scenario string) error {
	upListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	fixture := &cliprobe.Upstream{Scenario: scenario}
	up := &http.Server{Handler: fixture, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	go up.Serve(upListener)
	defer up.Close()
	h, err := proxy.New("http://"+upListener.Addr().String()+"/responses", func(r *http.Request) (proxy.Identity, error) {
		// Synthetic mode marker; never an authentication or production session ID.
		if r.Header.Get("X-Switcher-Demo-Session") != "demo" {
			return proxy.Identity{}, errors.New("unknown_demo_session")
		}
		id := "demo"
		if len(r.Header.Values("Thread-Id")) > 0 || len(r.Header.Values("Session-Id")) > 0 {
			var err error
			id, err = cliidentity.ThreadID(r.Header)
			if err != nil {
				return proxy.Identity{}, err
			}
		}
		return proxy.Identity{Session: id, Token: "synthetic-demo-token"}, nil
	})
	if err != nil {
		return err
	}
	defer h.Close()
	mux := http.NewServeMux()
	mux.Handle("/responses", h)
	mux.HandleFunc("GET /control/status", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"mode": "demo", "live_accounts": false, "persistence": false, "scenario": scenario, "upstream_calls": fixture.Calls.Load()})
	})
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	json.NewEncoder(os.Stdout).Encode(map[string]string{"event": "helper_started", "mode": "demo", "listen": listener.Addr().String()})
	select {
	case err := <-done:
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return err
		}
		return nil
	}
}
