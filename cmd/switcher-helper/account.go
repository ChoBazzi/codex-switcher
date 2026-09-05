package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/applock"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
)

func accountCommand(args []string, output io.Writer) error {
	if len(args) == 0 || (args[0] != "status" && args[0] != "login" && args[0] != "reauth") {
		return errors.New("usage: switcher-helper account status | login a|b | reauth a|b")
	}
	if args[0] == "status" && len(args) != 1 || args[0] != "status" && (len(args) != 2 || args[1] != "a" && args[1] != "b") {
		return errors.New("invalid_account_command")
	}
	parent, err := os.UserConfigDir()
	if err != nil {
		return applock.ErrStorage
	}
	stateDir := filepath.Join(parent, "com.bazzi.codex-switcher")
	lock, err := applock.Acquire(stateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	m := accounts.New(credentialstore.New(), stateDir)
	encode := json.NewEncoder(output)
	if args[0] == "status" {
		status, err := m.Status()
		if err != nil {
			return err
		}
		return encode.Encode(map[string]any{"accounts": status, "remote_verified": false, "automatic_refresh": false})
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		return errors.New("codex_executable_not_found")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	report := func(state accounts.State) {
		_ = encode.Encode(map[string]string{"event": "account_login", "slot": args[1], "state": string(state)})
	}
	if args[0] == "reauth" {
		return m.Reauthenticate(ctx, args[1], accounts.CodexRunner{Binary: binary}, report)
	}
	return m.Login(ctx, args[1], accounts.CodexRunner{Binary: binary}, report)
}
