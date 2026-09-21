// Package accounts manages up to five local account slots without exposing credentials.
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
)

var (
	ErrSlot          = errors.New("account_slot_must_be_a_to_e")
	ErrOccupied      = errors.New("account_slot_already_registered")
	ErrDuplicate     = errors.New("account_already_registered")
	ErrStore         = errors.New("account_store_unavailable")
	ErrLogin         = errors.New("browser_login_failed")
	ErrCancelled     = errors.New("browser_login_cancelled")
	ErrCleanup       = errors.New("login_cleanup_required")
	ErrExpired       = errors.New("login_credentials_expired")
	ErrNotRegistered = errors.New("account_slot_not_registered")
	ErrMismatch      = errors.New("reauth_account_mismatch")
)

type State string

const (
	Launching      State = "launching"
	BrowserWaiting State = "browser_waiting"
	Importing      State = "importing"
	Succeeded      State = "succeeded"
	Failed         State = "failed"
	Cancelled      State = "cancelled"
)

type Runner interface {
	Run(context.Context, string, func()) error
}
type record struct {
	Slot           string          `json:"slot"`
	Credentials    Credentials     `json:"credentials"`
	RefreshBlocked bool            `json:"refresh_blocked,omitempty"`
	Registration   string          `json:"registration,omitempty"`
	History        *historyBinding `json:"history,omitempty"`
}
type registry struct {
	Version  int      `json:"version"`
	Accounts []record `json:"accounts"`
}
type Status struct {
	Slot       string     `json:"slot"`
	Registered bool       `json:"registered"`
	State      string     `json:"state"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// Callers must additionally hold the application process lock for mutations.
type Manager struct {
	operationMu operationLock // Credential mutations serialize; request waiters can cancel.
	mu          operationLock // Local credential reads/writes; request waiters can cancel.
	refreshSlot string        // guarded by mu; the durable blocked marker remains authoritative on restart
	vault       credentialstore.Vault
	tempParent  string
	refresher   Refresher
}

func New(v credentialstore.Vault, tempParent string) *Manager {
	return &Manager{vault: v, tempParent: tempParent}
}

func (m *Manager) read() (registry, error) {
	r := registry{Version: 1}
	data, err := m.vault.Read()
	if errors.Is(err, credentialstore.ErrNotFound) {
		return r, nil
	}
	if err != nil {
		return r, ErrStore
	}
	defer clear(data)
	if len(data) > credentialstore.MaxBytes || json.Unmarshal(data, &r) != nil || r.Version != 1 || len(r.Accounts) > accountslot.Capacity {
		return registry{}, ErrStore
	}
	slots := map[string]bool{}
	for i, a := range r.Accounts {
		c := a.Credentials
		if a.Registration != "" && !safeValue(a.Registration, 256) {
			return registry{}, ErrStore
		}
		if !validSlot(a.Slot) || slots[a.Slot] || !safeValue(c.AccountID, 256) || !safeValue(c.AccessToken, 32<<10) || !safeValue(c.RefreshToken, 8192) || !safeValue(c.IDToken, 32<<10) || c.ExpiresAt.IsZero() {
			return registry{}, ErrStore
		}
		claims, err := accessClaims(c.AccessToken)
		if err != nil || claims.Auth.Account != "" && claims.Auth.Account != c.AccountID || claims.Auth.User != "" && !safeValue(claims.Auth.User, 256) {
			return registry{}, ErrStore
		}
		c.UserID = claims.Auth.User
		for _, previous := range r.Accounts[:i] {
			if previous.Credentials.AccountID == c.AccountID && (sameIdentity(previous.Credentials, c) || previous.Credentials.UserID == "" || c.UserID == "") {
				return registry{}, ErrStore
			}
		}
		r.Accounts[i].Credentials = c
		slots[a.Slot] = true
	}
	return r, nil
}

func (m *Manager) Status() ([]Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read()
	if err != nil {
		return nil, err
	}
	statuses := make([]Status, accountslot.Capacity)
	for i, slot := range accountslot.All() {
		statuses[i] = Status{Slot: slot, State: "not_registered"}
	}
	for i := range statuses {
		for _, a := range r.Accounts {
			if a.Slot == statuses[i].Slot {
				state := "stored_unverified"
				if a.RefreshBlocked || !a.Credentials.ExpiresAt.After(time.Now()) {
					state = "expired"
				}
				expires := a.Credentials.ExpiresAt
				statuses[i] = Status{Slot: a.Slot, Registered: true, State: state, ExpiresAt: &expires}
			}
		}
	}
	return statuses, nil
}

// Logout forgets one slot only. The caller holds the account operation lock
// and invalidates that slot's routing before replacing credentials.
func (m *Manager) Logout(slot string) error {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validSlot(slot) {
		return ErrSlot
	}
	r, err := m.read()
	if err != nil {
		return err
	}
	for i, account := range r.Accounts {
		if account.Slot != slot {
			continue
		}
		r.Accounts = append(r.Accounts[:i], r.Accounts[i+1:]...)
		encoded, err := json.Marshal(r)
		if err != nil {
			return ErrStore
		}
		defer clear(encoded)
		if m.vault.Write(encoded) != nil {
			return ErrStore
		}
		return nil
	}
	return nil
}

// Access returns only the credentials required for a model request, never the
// refresh or ID token. It performs no network request or automatic refresh.
func (m *Manager) Access(slot string, now time.Time) (Access, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validSlot(slot) {
		return Access{}, ErrSlot
	}
	r, err := m.read()
	if err != nil {
		return Access{}, err
	}
	for _, a := range r.Accounts {
		if a.Slot == slot {
			if a.RefreshBlocked || !a.Credentials.ExpiresAt.After(now.Add(30*time.Second)) {
				return Access{}, ErrExpired
			}
			return a.access(), nil
		}
	}
	return Access{}, ErrNotRegistered
}

type Access struct {
	Token, AccountID string
	ExpiresAt        time.Time
	UserID           string `json:"-"`
	Registration     string `json:"-"`
	history          *historyBinding
}

func (r record) access() Access {
	return Access{Token: r.Credentials.AccessToken, AccountID: r.Credentials.AccountID,
		ExpiresAt: r.Credentials.ExpiresAt, UserID: r.Credentials.UserID, Registration: r.Registration, history: r.History}
}

func (Access) String() string   { return "[redacted access]" }
func (Access) GoString() string { return "[redacted access]" }

func (m *Manager) Login(ctx context.Context, slot string, runner Runner, report func(State)) error {
	return m.login(ctx, slot, runner, report, false)
}

func (m *Manager) Reauthenticate(ctx context.Context, slot string, runner Runner, report func(State)) error {
	return m.login(ctx, slot, runner, report, true)
}

func (m *Manager) login(ctx context.Context, slot string, runner Runner, report func(State), replace bool) (err error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if report == nil {
		report = func(State) {}
	}
	defer func() {
		if err == nil {
			report(Succeeded)
		} else if errors.Is(err, ErrCancelled) {
			report(Cancelled)
		} else {
			report(Failed)
		}
	}()
	if !validSlot(slot) {
		return ErrSlot
	}
	r, err := m.read()
	if err != nil {
		return err
	}
	index := -1
	for i, a := range r.Accounts {
		if a.Slot == slot {
			if !replace {
				return ErrOccupied
			}
			index = i
		}
	}
	if replace && index == -1 {
		return ErrNotRegistered
	}
	if ctx.Err() != nil {
		return ErrCancelled
	}
	dir, err := os.MkdirTemp(m.tempParent, "login-")
	if err != nil {
		return ErrLogin
	}
	// This exact newly created private directory is the only cleanup target.
	defer func() {
		if os.RemoveAll(dir) != nil {
			err = ErrCleanup
		}
	}()
	if os.Chmod(dir, 0700) != nil {
		return ErrLogin
	}
	config := []byte("cli_auth_credentials_store = \"file\"\nforced_login_method = \"chatgpt\"\ncheck_for_update_on_startup = false\n")
	if os.WriteFile(filepath.Join(dir, "config.toml"), config, 0600) != nil {
		return ErrLogin
	}
	report(Launching)
	if runner.Run(ctx, dir, func() { report(BrowserWaiting) }) != nil {
		if ctx.Err() != nil {
			return ErrCancelled
		}
		return ErrLogin
	}
	if ctx.Err() != nil {
		return ErrCancelled
	}
	report(Importing)
	data, err := readAuth(dir)
	if err != nil {
		return err
	}
	defer clear(data)
	c, err := ParseAuth(data)
	if err != nil {
		return err
	}
	if !c.ExpiresAt.After(time.Now().Add(30 * time.Second)) {
		return ErrExpired
	}
	if replace {
		if r.Accounts[index].Credentials.UserID == "" {
			return ErrIdentity
		}
		if !sameIdentity(r.Accounts[index].Credentials, c) {
			return ErrMismatch
		}
	}
	for _, a := range r.Accounts {
		if a.Slot != slot && a.Credentials.AccountID == c.AccountID {
			if a.Credentials.UserID == "" {
				return ErrIdentity
			}
			if sameIdentity(a.Credentials, c) {
				return ErrDuplicate
			}
		}
	}
	// Remove plaintext before committing to Keychain. Failure never falls back.
	if os.Remove(filepath.Join(dir, "auth.json")) != nil {
		return ErrCleanup
	}
	if ctx.Err() != nil {
		return ErrCancelled
	}
	if replace {
		r.Accounts[index].Credentials = c
		r.Accounts[index].RefreshBlocked = false
		r.Accounts[index].Registration = rand.Text()
		r.Accounts[index].History = nil
	} else {
		r.Accounts = append(r.Accounts, record{Slot: slot, Credentials: c, Registration: rand.Text()})
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return ErrStore
	}
	defer clear(encoded)
	if m.vault.Write(encoded) != nil {
		return ErrStore
	}
	return nil
}

func validSlot(s string) bool { return accountslot.Valid(s) }

func readAuth(dir string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrAuthFormat
	}
	defer root.Close()
	f, err := root.OpenFile("auth.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrAuthFormat
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxAuthBytes {
		return nil, ErrAuthFormat
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return nil, ErrAuthFormat
	}
	// The containing login directory has been private since before CLI launch.
	if f.Chmod(0600) != nil {
		return nil, ErrAuthFormat
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxAuthBytes+1))
	if err != nil || len(data) > MaxAuthBytes {
		clear(data)
		return nil, ErrAuthFormat
	}
	return data, nil
}
