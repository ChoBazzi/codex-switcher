package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func userWriter(workspace, user string) Runner {
	return runnerFunc(func(ctx context.Context, dir string, waiting func()) error {
		waiting()
		return os.WriteFile(filepath.Join(dir, "auth.json"), syntheticUserAuth(workspace, user, time.Now().Add(time.Hour)), 0600)
	})
}

func TestCompositeIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, workspace, user string
		duplicate             bool
	}{
		{"same workspace different user", "workspace-one", "user-two", false},
		{"same user different workspace", "workspace-two", "user-one", false},
		{"same pair", "workspace-one", "user-one", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &memoryVault{}
			parent := t.TempDir()
			m := New(v, parent)
			ctx := context.Background()
			if err := m.Login(ctx, "a", userWriter("workspace-one", "user-one"), nil); err != nil {
				t.Fatal(err)
			}
			before := string(v.data)
			err := m.Login(ctx, "b", userWriter(tc.workspace, tc.user), nil)
			if tc.duplicate {
				if !errors.Is(err, ErrDuplicate) || string(v.data) != before {
					t.Fatal("duplicate mutated registry")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			status, err := New(v, parent).Status()
			if err != nil || !status[0].Registered || !status[1].Registered {
				t.Fatal("pair lost after restart")
			}
			encoded, _ := json.Marshal(status)
			if strings.Contains(string(encoded), "user-one") || strings.Contains(string(encoded), "workspace-one") {
				t.Fatal("identity exposed")
			}
		})
	}
}

func TestCompositeReauthentication(t *testing.T) {
	v := &memoryVault{}
	m := New(v, t.TempDir())
	ctx := context.Background()
	if err := m.Login(ctx, "a", userWriter("workspace-one", "user-one"), nil); err != nil {
		t.Fatal(err)
	}
	before := string(v.data)
	for _, identity := range [][2]string{{"workspace-one", "user-two"}, {"workspace-two", "user-one"}} {
		if err := m.Reauthenticate(ctx, "a", userWriter(identity[0], identity[1]), nil); !errors.Is(err, ErrMismatch) || string(v.data) != before {
			t.Fatal("reauth switched identity")
		}
	}
	if err := m.Reauthenticate(ctx, "a", userWriter("workspace-one", "user-one"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyIdentityReadIsNonMutating(t *testing.T) {
	for _, user := range []string{"user-one", ""} {
		v := &memoryVault{}
		// Version 1 has no separate user ID; derive it from the stored token.
		var auth struct {
			Tokens Credentials `json:"tokens"`
		}
		json.Unmarshal(syntheticUserAuth("workspace-one", user, time.Now().Add(time.Hour)), &auth)
		auth.Tokens.ExpiresAt = time.Now().Add(time.Hour)
		v.data, _ = json.Marshal(registry{Version: 1, Accounts: []record{{Slot: "a", Credentials: auth.Tokens}}})
		before := string(v.data)
		m := New(v, t.TempDir())
		if _, err := m.Status(); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Access("a", time.Now()); err != nil {
			t.Fatal(err)
		}
		if v.writes != 0 || string(v.data) != before {
			t.Fatal("legacy read rewrote vault")
		}
		err := m.Login(context.Background(), "b", userWriter("workspace-one", "user-two"), nil)
		if user == "" {
			if !errors.Is(err, ErrIdentity) || v.writes != 0 {
				t.Fatal("missing user guessed")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestUserIdentityMissingOrInvalid(t *testing.T) {
	for _, user := range []string{"", "bad\nuser", strings.Repeat("x", 257)} {
		if _, err := ParseAuth(syntheticUserAuth("workspace-one", user, time.Now().Add(time.Hour))); !errors.Is(err, ErrIdentity) {
			t.Fatal("unusable user ID accepted")
		}
	}
}

func TestDuplicateCompositeRegistryRejected(t *testing.T) {
	c, err := ParseAuth(syntheticUserAuth("workspace-one", "user-one", time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	v := &memoryVault{}
	v.data, _ = json.Marshal(registry{Version: 1, Accounts: []record{{Slot: "a", Credentials: c}, {Slot: "b", Credentials: c}}})
	if _, err := New(v, t.TempDir()).Status(); !errors.Is(err, ErrStore) || v.writes != 0 {
		t.Fatal("duplicate pair accepted")
	}
}
