package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/applock"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
)

func accountCommand(args []string, output io.Writer) error {
	if len(args) == 0 || (args[0] != "status" && args[0] != "login" && args[0] != "reauth" && args[0] != "logout") {
		return errors.New("usage: switcher-helper account status | login a|b|c|d|e | reauth a|b|c|d|e | logout a|b|c|d|e")
	}
	if args[0] == "status" && len(args) != 1 || args[0] != "status" && (len(args) != 2 || !accountslot.Valid(args[1])) {
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
	if args[0] == "logout" {
		// Open fails while an exec owns the affinity store; do not delete live credentials.
		db, err := affinity.Open(filepath.Join(stateDir, "affinity"))
		if err != nil {
			return err
		}
		defer db.Close()
		if _, err := m.Status(); err != nil {
			return err
		}
		if err := db.InvalidateSlot(args[1]); err != nil {
			return err
		}
		if err := m.Logout(args[1]); err != nil {
			return err
		}
		return encode.Encode(map[string]string{"event": "account_logout", "slot": args[1], "state": "succeeded"})
	}
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
