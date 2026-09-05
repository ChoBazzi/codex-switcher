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

	"github.com/ChoBazzi/codex-switcher/internal/proxy"
)

func main() {
	demo := flag.Bool("demo", false, "run synthetic upstream; no real accounts")
	port := flag.Int("port", 8765, "loopback port (0 selects an available port)")
	flag.Parse()
	if !*demo {
		fmt.Fprintln(os.Stderr, "Only --demo is available; live Codex integration is not implemented.")
		os.Exit(2)
	}
	secret := os.Getenv("SWITCHER_CONTROL_TOKEN")
	if len(secret) < 16 || *port < 0 || *port > 65535 {
		fmt.Fprintln(os.Stderr, "Set SWITCHER_CONTROL_TOKEN (at least 16 characters) and a valid port.")
		os.Exit(2)
	}
	if err := run(*port, secret); err != nil {
		fmt.Fprintln(os.Stderr, "helper_start_or_shutdown_failed")
		os.Exit(1)
	}
}

func run(port int, secret string) error {
	upListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	up := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 4<<20)); err != nil {
			w.WriteHeader(413)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Synthetic proxy demo; no model called.\"}\n\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
	}), ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	go up.Serve(upListener)
	defer up.Close()
	h, err := proxy.New("http://"+upListener.Addr().String()+"/responses", func(r *http.Request) (proxy.Identity, error) {
		// Demo sessions are deliberately fixed; this is NOT a Codex header adapter.
		if r.Header.Get("X-Switcher-Demo-Session") != "demo" {
			return proxy.Identity{}, errors.New("unknown_demo_session")
		}
		return proxy.Identity{Session: "demo", Token: "synthetic-demo-token"}, nil
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
		json.NewEncoder(w).Encode(map[string]any{"mode": "demo", "live_accounts": false, "persistence": false})
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
