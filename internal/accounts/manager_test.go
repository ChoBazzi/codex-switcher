package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
)

type memoryVault struct {
	data   []byte
	fail   bool
	writes int
}

func (v *memoryVault) Read() ([]byte, error) {
	if v.fail {
		return nil, credentialstore.ErrUnavailable
	}
	if v.data == nil {
		return nil, credentialstore.ErrNotFound
	}
	return append([]byte(nil), v.data...), nil
}
func (v *memoryVault) Write(b []byte) error {
	if v.fail {
		return credentialstore.ErrUnavailable
	}
	v.writes++
	v.data = append([]byte(nil), b...)
	return nil
}

type runnerFunc func(context.Context, string, func()) error

func (f runnerFunc) Run(c context.Context, d string, w func()) error { return f(c, d, w) }

func syntheticAuth(id string, expires time.Time) []byte {
	return syntheticUserAuth(id, "user-"+id, expires)
}

func syntheticUserAuth(id, user string, expires time.Time) []byte {
	payload, _ := json.Marshal(map[string]any{"exp": expires.Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": id, "chatgpt_user_id": user}})
	access := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic"
	data, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"access_token": access, "refresh_token": "synthetic-refresh-secret", "id_token": "synthetic-id-token", "account_id": id}})
	return data
}

func writer(id string) Runner {
	return runnerFunc(func(ctx context.Context, dir string, waiting func()) error {
		waiting()
		return os.WriteFile(filepath.Join(dir, "auth.json"), syntheticAuth(id, time.Now().Add(time.Hour)), 0600)
	})
}

func TestLoginStoresOnlyInVaultAndRedactsStatus(t *testing.T) {
	v := &memoryVault{}
	parent := t.TempDir()
	m := New(v, parent)
	var states []State
	err := m.Login(context.Background(), "a", runnerFunc(func(ctx context.Context, dir string, waiting func()) error {
		info, _ := os.Stat(dir)
		if info.Mode().Perm() != 0700 {
			t.Error("login directory not private")
		}
		config, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
		if !strings.Contains(string(config), `cli_auth_credentials_store = "file"`) {
			t.Error("wrong credential backend")
		}
		return writer("synthetic-account-a").Run(ctx, dir, waiting)
	}), func(s State) { states = append(states, s) })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(states, []State{Launching, BrowserWaiting, Importing, Succeeded}) {
		t.Fatal("unexpected lifecycle")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 || v.writes != 1 {
		t.Fatal("plaintext not cleaned or wrong writes")
	}
	// Simulate application restart using a new manager and the same vault.
	status, err := New(v, parent).Status()
	if err != nil || !status[0].Registered || status[1].Registered {
		t.Fatal("registration not persisted")
	}
	encoded, _ := json.Marshal(status)
	for _, secret := range []string{"synthetic-account-a", "synthetic-refresh-secret", "access_token", "id_token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("status leaked credentials")
		}
	}
	c, _ := ParseAuth(syntheticAuth("synthetic-account-a", time.Now().Add(time.Hour)))
	if strings.Contains(fmt.Sprintf("%v %+v %#v", c, c, c), "synthetic") {
		t.Fatal("formatting leaked credentials")
	}
}

func TestSlotsDuplicateAndVaultErrors(t *testing.T) {
	v := &memoryVault{}
	m := New(v, t.TempDir())
	ctx := context.Background()
	if err := m.Login(ctx, "a", writer("synthetic-a"), nil); err != nil {
		t.Fatal(err)
	}
	never := runnerFunc(func(context.Context, string, func()) error { t.Fatal("unexpected login launch"); return nil })
	if !errors.Is(m.Login(ctx, "a", never, nil), ErrOccupied) {
		t.Fatal("occupied slot replaced")
	}
	if !errors.Is(m.Login(ctx, "c", never, nil), ErrSlot) {
		t.Fatal("third slot accepted")
	}
	if !errors.Is(m.Login(ctx, "b", writer("synthetic-a"), nil), ErrDuplicate) || v.writes != 1 {
		t.Fatal("duplicate account accepted")
	}
	if err := m.Login(ctx, "b", writer("synthetic-b"), nil); err != nil {
		t.Fatal(err)
	}
	v.fail = true
	if _, err := m.Status(); !errors.Is(err, ErrStore) {
		t.Fatal("vault failure hidden")
	}
}

func TestFailuresCleanPrivateDirectory(t *testing.T) {
	for _, kind := range []string{"cancel", "runner", "malformed", "expired", "symlink", "hardlink", "directory", "oversize", "store"} {
		t.Run(kind, func(t *testing.T) {
			v := &memoryVault{}
			parent := t.TempDir()
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("synthetic-outside"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := New(v, parent)
			runner := runnerFunc(func(ctx context.Context, dir string, waiting func()) error {
				waiting()
				path := filepath.Join(dir, "auth.json")
				switch kind {
				case "cancel":
					cancel()
					return errors.New("synthetic secret")
				case "runner":
					return errors.New("synthetic secret")
				case "malformed":
					return os.WriteFile(path, []byte("synthetic secret"), 0600)
				case "expired":
					return os.WriteFile(path, syntheticAuth("synthetic-a", time.Now().Add(-time.Hour)), 0600)
				case "symlink":
					return os.Symlink(outside, path)
				case "hardlink":
					return os.Link(outside, path)
				case "directory":
					return os.Mkdir(path, 0700)
				case "oversize":
					return os.WriteFile(path, make([]byte, MaxAuthBytes+1), 0600)
				case "store":
					v.fail = true
					return writer("synthetic-a").Run(ctx, dir, func() {})
				}
				return nil
			})
			var last State
			err := m.Login(ctx, "a", runner, func(s State) { last = s })
			if err == nil || strings.Contains(err.Error(), "synthetic") || v.writes != 0 {
				t.Fatal("failure leaked or stored credentials")
			}
			if kind == "cancel" && last != Cancelled {
				t.Fatal("wrong cancellation state")
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatal("temporary credentials left behind")
			}
			data, _ := os.ReadFile(outside)
			if string(data) != "synthetic-outside" {
				t.Fatal("outside file modified")
			}
		})
	}
}

func TestLoginEnvironmentDoesNotInheritSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "synthetic-key")
	t.Setenv("CODEX_ACCESS_TOKEN", "synthetic-token")
	t.Setenv("CODEX_HOME", "synthetic-old-home")
	t.Setenv("OPENAI_BASE_URL", "https://example.invalid")
	t.Setenv("HTTP_PROXY", "http://example.invalid")
	env := strings.Join(loginEnvironment("/synthetic/private"), "\n")
	for _, secret := range []string{"synthetic-key", "synthetic-token", "synthetic-old-home", "example.invalid"} {
		if strings.Contains(env, secret) {
			t.Fatal("login inherited unsafe environment")
		}
	}
}

func TestCorruptRegistryIsNotOverwritten(t *testing.T) {
	v := &memoryVault{data: []byte(`{"version":99}`)}
	m := New(v, t.TempDir())
	if err := m.Login(context.Background(), "a", writer("synthetic-a"), nil); !errors.Is(err, ErrStore) || v.writes != 0 {
		t.Fatal("corrupt registry overwritten")
	}
}

func TestReauthenticationRequiresSameAccount(t *testing.T) {
	v := &memoryVault{}
	m := New(v, t.TempDir())
	ctx := context.Background()
	if err := m.Reauthenticate(ctx, "a", writer("synthetic-a"), nil); !errors.Is(err, ErrNotRegistered) {
		t.Fatal("reauth created an account")
	}
	if err := m.Login(ctx, "a", writer("synthetic-a"), nil); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), v.data...)
	if err := m.Reauthenticate(ctx, "a", writer("synthetic-b"), nil); !errors.Is(err, ErrMismatch) {
		t.Fatal("reauth replaced account identity")
	}
	if string(before) != string(v.data) || v.writes != 1 {
		t.Fatal("old credentials modified on mismatch")
	}
	if err := m.Reauthenticate(ctx, "a", writer("synthetic-a"), nil); err != nil || v.writes != 2 {
		t.Fatal("same-account reauth failed")
	}
}

func TestAuthFormatChecks(t *testing.T) {
	base := syntheticAuth("synthetic-a", time.Now().Add(time.Hour))
	for _, kind := range []string{"api", "missing-refresh", "account-mismatch", "header-injection", "invalid-jwt"} {
		t.Run(kind, func(t *testing.T) {
			var data map[string]any
			json.Unmarshal(base, &data)
			tokens := data["tokens"].(map[string]any)
			switch kind {
			case "api":
				data["OPENAI_API_KEY"] = "synthetic-api-key"
			case "missing-refresh":
				delete(tokens, "refresh_token")
			case "account-mismatch":
				tokens["account_id"] = "synthetic-other"
			case "header-injection":
				tokens["access_token"] = "synthetic\r\nsecret"
			case "invalid-jwt":
				tokens["access_token"] = "synthetic-invalid"
			}
			encoded, _ := json.Marshal(data)
			if _, err := ParseAuth(encoded); !errors.Is(err, ErrAuthFormat) {
				t.Fatal("unsupported credentials accepted")
			}
		})
	}
}

func TestAccessRequiresRegisteredUnexpiredSlot(t *testing.T) {
	v := &memoryVault{}
	m := New(v, t.TempDir())
	if _, err := m.Access("a", time.Now()); !errors.Is(err, ErrNotRegistered) {
		t.Fatal("missing account accepted")
	}
	if err := m.Login(context.Background(), "a", writer("synthetic-a"), nil); err != nil {
		t.Fatal(err)
	}
	access, err := m.Access("a", time.Now())
	if err != nil || access.Token == "" || access.AccountID != "synthetic-a" {
		t.Fatal("access unavailable")
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", access, access, access), "synthetic") {
		t.Fatal("access formatting leaked")
	}
	if _, err := m.Access("a", time.Now().Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatal("expired credentials accepted")
	}
	if v.writes != 1 {
		t.Fatal("access lookup modified credentials")
	}
}
