package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/applock"
)

type refreshFunc func(context.Context, Credentials) (Credentials, error)

func (f refreshFunc) Refresh(ctx context.Context, c Credentials) (Credentials, error) {
	return f(ctx, c)
}
func refreshFixture(t *testing.T, f Refresher) (*Manager, *memoryVault) {
	t.Helper()
	v := &memoryVault{}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m := NewRefreshing(v, dir, f)
	c, err := ParseAuth(syntheticAuth("alpha", time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	v.data, _ = json.Marshal(registry{Version: 1, Accounts: []record{{Slot: "a", Credentials: c}}})
	return m, v
}
func renewed(t *testing.T, id string) Credentials {
	t.Helper()
	c, err := ParseAuth(syntheticAuth(id, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	c.RefreshToken = "synthetic-rotated"
	return c
}

func TestRefreshSingleFlightAndLocalInspection(t *testing.T) {
	var calls atomic.Int32
	next := renewed(t, "alpha")
	m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) { calls.Add(1); return next, nil }))
	if _, err := m.Access("a", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || v.writes != 0 {
		t.Fatal("local inspection exchanged credentials")
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := m.RequestAccess("a", time.Now())
			if err != nil || a.Token != next.AccessToken {
				t.Error("renewed credentials unavailable")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || v.writes != 2 {
		t.Fatal("duplicate exchange or missing durable intent")
	}
	second := NewRefreshing(v, m.tempParent, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
		t.Error("exchange repeated after restart")
		return Credentials{}, ErrRefresh
	}))
	if _, err := second.RequestAccess("a", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshFailureNeverReplaysAcrossRestart(t *testing.T) {
	for _, kind := range []string{"network", "identity", "user", "expired", "commit"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			var vault *memoryVault
			m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
				calls++
				switch kind {
				case "network":
					return Credentials{}, errors.New("synthetic-secret-server-error")
				case "identity":
					return renewed(t, "other"), nil
				case "user":
					return ParseAuth(syntheticUserAuth("alpha", "synthetic-other-user", time.Now().Add(time.Hour)))
				case "expired":
					c, _ := ParseAuth(syntheticAuth("alpha", time.Now().Add(time.Second)))
					return c, nil
				case "commit":
					vault.fail = true
				}
				return renewed(t, "alpha"), nil
			}))
			vault = v
			if _, err := m.RequestAccess("a", time.Now()); err == nil {
				t.Fatal("invalid renewal accepted")
			}
			v.fail = false
			restarted := NewRefreshing(v, m.tempParent, m.refresher)
			if _, err := restarted.RequestAccess("a", time.Now()); !errors.Is(err, ErrRefresh) {
				t.Fatal("ambiguous renewal not blocked")
			}
			if calls != 1 {
				t.Fatal("failed exchange replayed")
			}
			if owner, err := restarted.HistoryCredential("a"); !errors.Is(err, ErrRefresh) || owner != ([32]byte{}) {
				t.Fatal("failed refresh retained usable history ownership")
			}
			states, _ := restarted.Status()
			if states[0].State != "expired" {
				t.Fatal("reauthentication not surfaced")
			}
			if err := restarted.Reauthenticate(context.Background(), "a", writer("alpha"), nil); err != nil {
				t.Fatal(err)
			}
			if _, err := restarted.RequestAccess("a", time.Now()); err != nil {
				t.Fatal("explicit login did not clear marker")
			}
		})
	}
}
func TestRefreshLockAndIntentFailurePreventExchange(t *testing.T) {
	m, v := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
		t.Fatal("exchange during account mutation")
		return Credentials{}, ErrRefresh
	}))
	lock, err := applock.Acquire(m.tempParent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.RequestAccess("a", time.Now()); !errors.Is(err, applock.ErrBusy) {
		t.Fatal("account operation lock bypassed")
	}
	lock.Close()
	v.fail = true
	if _, err := m.RequestAccess("a", time.Now()); err == nil {
		t.Fatal("vault failure ignored")
	}
}
func TestOAuthSingleAttemptAndRotation(t *testing.T) {
	for _, kind := range []string{"success", "redirect", "error", "partial", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			next := renewed(t, "alpha")
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.Header.Get("Authorization") != "" {
					t.Error("unexpected request credentials")
				}
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["grant_type"] != "refresh_token" || body["refresh_token"] != "synthetic-refresh-secret" {
					t.Error("invalid exchange")
				}
				switch kind {
				case "redirect":
					w.Header().Set("Location", "/again")
					w.WriteHeader(307)
					return
				case "error":
					w.WriteHeader(500)
					io.WriteString(w, "synthetic-secret-error")
					return
				case "partial":
					io.WriteString(w, `{"access_token":`)
					return
				case "oversize":
					for range MaxAuthBytes + 1 {
						io.WriteString(w, "x")
					}
					return
				}
				json.NewEncoder(w).Encode(map[string]string{"access_token": next.AccessToken, "refresh_token": next.RefreshToken})
			})
			f := NewOAuthRefresher()
			f.endpoint = "http://synthetic.invalid/token"
			f.client.Transport = refreshTransport(func(r *http.Request) (*http.Response, error) {
				if r.GetBody != nil {
					t.Error("refresh request is replayable")
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				response := w.Result()
				response.Request = r
				return response, nil
			})
			old, _ := ParseAuth(syntheticAuth("alpha", time.Now().Add(time.Minute)))
			result, err := f.Refresh(context.Background(), old)
			if kind == "success" {
				if err != nil || result.RefreshToken != next.RefreshToken || result.IDToken != old.IDToken {
					t.Fatal("rotation or optional ID handling failed")
				}
			} else if err != ErrRefresh {
				t.Fatal("server error not redacted")
			}
			if calls.Load() != 1 {
				t.Fatal("exchange retried")
			}
		})
	}
}

type refreshTransport func(*http.Request) (*http.Response, error)

func (f refreshTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestActivityExcludesMutationAndAllowsRenewal(t *testing.T) {
	m, _ := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) { return renewed(t, "alpha"), nil }))
	lease, err := m.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	if rejected, err := m.BeginTurn(); err == nil || rejected != nil {
		t.Fatal("failed lease returned a live handle")
	}
	if other, err := AcquireActivity(m.tempParent); err == nil {
		other.Close()
		t.Fatal("account mutation can overlap tool turn")
	}
	if _, err := m.RequestAccess("a", time.Now()); err != nil {
		t.Fatal("turn lease prevented renewal")
	}
	lease.Close()
	other, err := AcquireActivity(m.tempParent)
	if err != nil {
		t.Fatal("turn lease not released")
	}
	other.Close()
}
