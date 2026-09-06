package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

func usageCommand(args []string, output io.Writer) error {
	f := flag.NewFlagSet("usage", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	watch := f.Bool("watch", false, "poll every 60 seconds")
	if f.Parse(args) != nil || f.NArg() > 1 || f.NArg() == 1 && f.Arg(0) != "a" && f.Arg(0) != "b" {
		return errors.New("usage: switcher-helper usage [--watch] [a|b]")
	}
	slots := []string{"a", "b"}
	if f.NArg() == 1 {
		slots = []string{f.Arg(0)}
	}
	// Read-only Access does not use a login workspace or modify Keychain.
	client := usage.NewClient()
	defer client.Close()
	m := usage.NewMonitor(accounts.New(credentialstore.New(), ""), client)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	publish := func(samples []usage.Snapshot) error {
		if err := json.NewEncoder(output).Encode(struct {
			Event           string           `json:"event"`
			IntervalSeconds int              `json:"interval_seconds"`
			Accounts        []usage.Snapshot `json:"accounts"`
		}{"usage_snapshot", 60, samples}); err != nil {
			return errors.New("usage_output_failed")
		}
		return nil
	}
	if *watch {
		return m.Run(ctx, slots, publish)
	}
	samples := m.Refresh(ctx, slots)
	if err := publish(samples); err != nil {
		return err
	}
	for _, s := range samples {
		if s.ErrorCode != "" {
			return errors.New("usage_refresh_failed")
		}
	}
	return nil
}
