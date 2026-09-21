package accounts

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/applock"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
)

var ErrRefresh = fmt.Errorf("%w: account_refresh_requires_login", ErrExpired)

// Refresher performs exactly one exchange. Credentials and server errors must
// never appear in diagnostics. Only the helper enables this capability.
type Refresher interface {
	Refresh(context.Context, Credentials) (Credentials, error)
}

type OAuthRefresher struct {
	client   *http.Client
	endpoint string
}

func NewOAuthRefresher() *OAuthRefresher {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	// A fresh connection and a non-replayable body prevent transport retries.
	transport.DisableKeepAlives = true
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	return &OAuthRefresher{client: &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		endpoint: "https://auth.openai.com/oauth/token"}
}

func (o *OAuthRefresher) Refresh(ctx context.Context, old Credentials) (Credentials, error) {
	body, err := json.Marshal(map[string]string{"grant_type": "refresh_token", "client_id": "app_EMoamEEZ73f0CkXaXp7hrann", "refresh_token": old.RefreshToken})
	if err != nil {
		return Credentials{}, ErrRefresh
	}
	payload := &refreshBody{data: body, reader: bytes.NewReader(body)}
	defer payload.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, payload)
	if err != nil {
		return Credentials{}, ErrRefresh
	}
	req.ContentLength = int64(len(body))
	req.Close = true
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := o.client.Do(req)
	if err != nil {
		return Credentials{}, ErrRefresh
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Credentials{}, ErrRefresh
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxAuthBytes+1))
	if err != nil {
		return Credentials{}, ErrRefresh
	}
	defer clear(data)
	var tokens struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		ID      string `json:"id_token"`
	}
	if len(data) > MaxAuthBytes || json.Unmarshal(data, &tokens) != nil || tokens.Access == "" {
		return Credentials{}, ErrRefresh
	}
	next := old
	next.AccessToken = tokens.Access
	if tokens.Refresh != "" {
		next.RefreshToken = tokens.Refresh
	}
	if tokens.ID != "" {
		next.IDToken = tokens.ID
	}
	return next, nil
}

func NewRefreshing(v credentialstore.Vault, stateDir string, refresher Refresher) *Manager {
	m := New(v, stateDir)
	m.refresher = refresher
	return m
}

// RequestAccess is called only for explicit model requests and scheduled or
// explicit usage reads. Access/Status remain local-only, including UI selection.
func (m *Manager) RequestAccess(slot string, now time.Time) (Access, error) {
	return m.RequestAccessContext(context.Background(), slot, now)
}

// Cancellation removes a queued request immediately. Once the durable refresh
// intent is written, finish the single exchange and commit under its independent
// timeout before returning cancellation. Never discard rotated credentials.
func (m *Manager) RequestAccessContext(ctx context.Context, slot string, now time.Time) (Access, error) {
	a, err := m.requestAccessContext(ctx, slot, now)
	if ctx.Err() != nil {
		return Access{}, ctx.Err()
	}
	return a, err
}

func (m *Manager) requestAccessContext(requestCtx context.Context, slot string, now time.Time) (Access, error) {
	if err := requestCtx.Err(); err != nil {
		return Access{}, err
	}
	if m.refresher == nil {
		return m.Access(slot, now)
	}
	if a, ready, err := m.requestAccessWithoutRefresh(requestCtx, slot, now); ready {
		return a, err
	}
	if err := m.operationMu.LockContext(requestCtx); err != nil {
		return Access{}, err
	}
	defer m.operationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := requestCtx.Err(); err != nil {
		return Access{}, err
	}
	if !validSlot(slot) {
		return Access{}, ErrSlot
	}
	r, err := m.read()
	if err != nil {
		return Access{}, err
	}
	find := func(r registry) int {
		for i, a := range r.Accounts {
			if a.Slot == slot {
				return i
			}
		}
		return -1
	}
	i := find(r)
	if i < 0 {
		return Access{}, ErrNotRegistered
	}
	if r.Accounts[i].RefreshBlocked {
		return Access{}, ErrRefresh
	}
	if r.Accounts[i].Credentials.ExpiresAt.After(now.Add(2 * time.Minute)) {
		return r.Accounts[i].access(), nil
	}
	if m.refresher == nil || m.tempParent == "" {
		return Access{}, ErrExpired
	}
	// Same lock as login/logout, held through exchange and Keychain commit. Re-read
	// after acquisition: another process may already have rotated or removed it.
	lock, err := applock.Acquire(m.tempParent)
	if err != nil {
		return Access{}, err
	}
	defer lock.Close()
	r, err = m.read()
	if err != nil {
		return Access{}, err
	}
	i = find(r)
	if i < 0 {
		return Access{}, ErrNotRegistered
	}
	if r.Accounts[i].RefreshBlocked {
		return Access{}, ErrRefresh
	}
	old := r.Accounts[i].Credentials
	if old.ExpiresAt.After(now.Add(2 * time.Minute)) {
		return r.Accounts[i].access(), nil
	}
	save := func() error {
		data, e := json.Marshal(r)
		if e != nil {
			return ErrStore
		}
		defer clear(data)
		if m.vault.Write(data) != nil {
			return ErrStore
		}
		return nil
	}
	// Persist before dispatch: a crash or lost response must not replay a possibly
	// consumed refresh token. Explicit reauthentication clears this marker.
	if err := requestCtx.Err(); err != nil {
		return Access{}, err
	}
	r.Accounts[i].RefreshBlocked = true
	if err = save(); err != nil {
		return Access{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Keep mutation and interprocess locks through commit, but do not hold the
	// local-read mutex over network I/O. Other requests join via operationMu.
	m.refreshSlot = slot
	next, err := func() (Credentials, error) {
		m.mu.Unlock()
		defer m.mu.Lock()
		return m.refresher.Refresh(ctx, old)
	}()
	m.refreshSlot = ""
	if err != nil {
		return Access{}, ErrRefresh
	}
	data, err := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": next})
	if err != nil {
		return Access{}, ErrRefresh
	}
	defer clear(data)
	next, err = ParseAuth(data)
	if err != nil || !sameIdentity(old, next) || !next.ExpiresAt.After(time.Now().Add(2*time.Minute)) {
		return Access{}, ErrRefresh
	}
	owner := r.Accounts[i].access().HistoryCredential()
	r.Accounts[i].Credentials = next
	r.Accounts[i].History = &historyBinding{Owner: owner, Current: r.Accounts[i].access().credentialDigest()}
	r.Accounts[i].RefreshBlocked = false
	if err = save(); err != nil {
		return Access{}, err
	}
	return r.Accounts[i].access(), nil
}

// Only read committed credentials here. Login/logout still exclude this read
// through mu; its context-aware wait also covers local read contention. A same-slot
// exchange must join the mutation queue and read its final committed outcome.
func (m *Manager) requestAccessWithoutRefresh(ctx context.Context, slot string, now time.Time) (Access, bool, error) {
	if err := m.mu.LockContext(ctx); err != nil {
		return Access{}, true, err
	}
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Access{}, true, err
	}
	if !validSlot(slot) {
		return Access{}, true, ErrSlot
	}
	if m.refreshSlot == slot {
		return Access{}, false, nil
	}
	r, err := m.read()
	if err != nil {
		return Access{}, true, err
	}
	for _, a := range r.Accounts {
		if a.Slot != slot {
			continue
		}
		if a.RefreshBlocked {
			return Access{}, true, ErrRefresh
		}
		if a.Credentials.ExpiresAt.After(now.Add(2 * time.Minute)) {
			return a.access(), true, nil
		}
		// Re-read under the mutation lock before making any refresh decision.
		return Access{}, false, nil
	}
	return Access{}, true, ErrNotRegistered
}

// RoundTripper may close a request asynchronously after Do returns. Coordinate
// clearing with Read rather than modifying a transport-owned backing buffer.
type refreshBody struct {
	mu     sync.Mutex
	data   []byte
	reader *bytes.Reader
	closed bool
}

func (b *refreshBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.EOF
	}
	return b.reader.Read(p)
}
func (b *refreshBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	clear(b.data)
	b.closed = true
	return nil
}
