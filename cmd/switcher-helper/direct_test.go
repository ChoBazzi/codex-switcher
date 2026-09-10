package main

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/directcli"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
)

type directNoAccess struct{}

func (directNoAccess) Access(string, time.Time) (accounts.Access, error) {
	return accounts.Access{}, accounts.ErrNotRegistered
}

func TestGeneratedDirectSecretsPassStartupValidation(t *testing.T) {
	modelSecret, hookSecret := newDirectSecret(), newDirectSecret()
	for _, secret := range []string{modelSecret, hookSecret} {
		decoded, err := base64.RawURLEncoding.DecodeString(secret)
		if err != nil || len(decoded) != 32 || len(secret) < 32 {
			t.Fatal("invalid generated secret length or encoding")
		}
	}
	if modelSecret == hookSecret {
		t.Fatal("capabilities must be independent")
	}
	router, err := routing.New(directNoAccess{}, 90)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := directcli.New(router, checkpoint.Origin{Project: "synthetic", Worktree: "synthetic", Branch: "synthetic"}, modelSecret, hookSecret); err != nil {
		t.Fatal("generated credentials rejected at startup")
	}
	cleanup, err := directcli.Install(t.TempDir(), "/synthetic/helper", "http://127.0.0.1:8765", modelSecret, hookSecret)
	if err != nil {
		t.Fatal("generated credentials rejected during profile installation")
	}
	defer cleanup()
}
