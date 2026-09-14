package routing

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/handoff"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

type source struct{ expired atomic.Bool }

func (s *source) Access(slot string, now time.Time) (accounts.Access, error) {
	if s.expired.Load() && slot == "a" {
		return accounts.Access{}, accounts.ErrExpired
	}
	return accounts.Access{Token: "synthetic-" + slot, AccountID: "synthetic-account-" + slot, ExpiresAt: now.Add(time.Hour)}, nil
}
func origin(id string) checkpoint.Origin {
	return checkpoint.Origin{Project: "synthetic-project", Worktree: "synthetic-tree", Branch: "dev", Session: id}
}
func sample(slot string, used float64, now time.Time) usage.Snapshot {
	d, _ := usage.Parse([]byte(fmt.Sprintf(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":%v},"secondary_window":{"used_percent":0}}}`, used)))
	return usage.Snapshot{Slot: slot, State: "ok", LastAttempt: now, LastSuccess: &now, Usage: &d}
}
func TestSelectionAndAffinity(t *testing.T) {
	now := time.Now()
	auth := &source{}
	r, _ := New(auth, 90)
	r.Update([]usage.Snapshot{sample("a", 20, now), sample("b", 40, now)})
	if _, err := r.Register(origin("one"), false, now); !errors.Is(err, handoff.ErrInputRequired) {
		t.Fatal("missing user input accepted")
	}
	s, err := r.Register(origin("one"), true, now)
	if err != nil || s.Account != "a" {
		t.Fatal("incorrect initial selection")
	}
	later := now.Add(time.Second)
	r.Update([]usage.Snapshot{sample("a", 95, later), sample("b", 10, later)})
	id, err := r.Resolve(origin("one"), later)
	if err != nil || id.AccountID != "synthetic-account-a" {
		t.Fatal("existing session moved at threshold")
	}
	s, err = r.Register(origin("two"), true, later)
	if err != nil || s.Account != "b" {
		t.Fatal("new session did not select b")
	}
	if _, err = r.Register(origin("one"), true, later); !errors.Is(err, handoff.ErrConflict) {
		t.Fatal("session overwritten")
	}
	auth.expired.Store(true)
	if _, err = r.Resolve(origin("one"), later); !errors.Is(err, ErrUnavailable) {
		t.Fatal("expired session rerouted")
	}
	s, _ = r.Session("one")
	if s.Account != "a" {
		t.Fatal("lost original affinity")
	}
	wrong := origin("two")
	wrong.Worktree = "other"
	if _, err = r.Resolve(wrong, later); !errors.Is(err, ErrIdentity) {
		t.Fatal("worktree mismatch accepted")
	}
	if _, err = r.Resolve(origin("unknown"), later); !errors.Is(err, ErrIdentity) {
		t.Fatal("unknown session implicitly registered")
	}
}
func TestUnavailableSamples(t *testing.T) {
	now := time.Now()
	for _, change := range []func(*usage.Snapshot){
		func(s *usage.Snapshot) { s.Stale = true },
		func(s *usage.Snapshot) { s.State = "rate_limited" },
		func(s *usage.Snapshot) { s.LastSuccess = nil },
		func(s *usage.Snapshot) { v := now.Add(-usage.StaleAfter); s.LastSuccess = &v },
		func(s *usage.Snapshot) { v := now.Add(time.Second); s.LastSuccess = &v },
		func(s *usage.Snapshot) { s.Usage.Allowed = nil },
		func(s *usage.Snapshot) { *s.Usage.Allowed = false },
		func(s *usage.Snapshot) { *s.Usage.LimitReached = true },
		func(s *usage.Snapshot) { s.Usage.Secondary.UsedPercent = nil },
		func(s *usage.Snapshot) { s.Usage.Primary.ResetAt = &now },
		func(s *usage.Snapshot) { *s.Usage.Primary.UsedPercent = 100 },
	} {
		r, _ := New(&source{}, 90)
		s := sample("a", 0, now)
		change(&s)
		r.Update([]usage.Snapshot{s})
		if _, err := r.Register(origin("one"), true, now); !errors.Is(err, ErrUnavailable) {
			t.Fatal("unsafe new-session admission")
		}
	}
}

func TestNewSessionAcceptsPositiveQuotaBelowCheckpoint(t *testing.T) {
	for _, used := range []float64{90, 95, 99.9} {
		now := time.Now()
		r, _ := New(&source{}, 90)
		r.Update([]usage.Snapshot{sample("a", used, now), sample("b", 100, now)})
		s, err := r.Register(origin("low-quota"), true, now)
		if err != nil || s.Account != "a" {
			t.Fatalf("used=%v: %v", used, err)
		}
	}
}

func TestRegistrationAvailabilityReasons(t *testing.T) {
	now := time.Now()
	r, _ := New(&source{}, 90)
	if _, err := r.Register(origin("missing"), true, now); !errors.Is(err, ErrUsageUnavailable) {
		t.Fatal(err)
	}
	auth := &source{}
	auth.expired.Store(true)
	r, _ = New(auth, 90)
	r.Update([]usage.Snapshot{sample("a", 0, now), sample("b", 100, now)})
	if _, err := r.Register(origin("expired"), true, now); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatal(err)
	}
}
func TestSnapshotIsolationAndOrdering(t *testing.T) {
	now := time.Now()
	r, _ := New(&source{}, 90)
	s := sample("a", 10, now)
	r.Update([]usage.Snapshot{s})
	*s.Usage.Primary.UsedPercent = 100
	*s.LastSuccess = now.Add(-time.Hour)
	if _, err := r.Register(origin("one"), true, now); err != nil {
		t.Fatal("caller mutated cached sample")
	}
	failed := sample("a", 10, now.Add(time.Second))
	failed.State = "fetch_error"
	r.Update([]usage.Snapshot{failed})
	r.Update([]usage.Snapshot{sample("a", 0, now)})
	if _, err := r.Resolve(origin("one"), now.Add(time.Second)); err == nil {
		t.Fatal("old success replaced latest failure")
	}
}
func TestConcurrentRegistration(t *testing.T) {
	now := time.Now()
	r, _ := New(&source{}, 90)
	r.Update([]usage.Snapshot{sample("a", 0, now), sample("b", 0, now)})
	var wg sync.WaitGroup
	var wins atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := r.Register(origin("same"), true, now)
			if err == nil {
				wins.Add(1)
				if s.Account != "a" {
					t.Error("tie not deterministic")
				}
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("non-atomic binding")
	}
}

func TestProxyFailureNeverMovesToOtherAccount(t *testing.T) {
	now := time.Now()
	r, _ := New(&source{}, 90)
	r.Update([]usage.Snapshot{sample("a", 0, now), sample("b", 20, now)})
	_, err := r.Register(origin("one"), true, now)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Header.Get("Authorization") != "Bearer synthetic-a" {
			t.Error("wrong upstream account")
		}
		w.WriteHeader(429)
	}))
	defer up.Close()
	h, err := proxy.New(up.URL, func(req *http.Request) (proxy.Identity, error) { return r.Resolve(origin("one"), now) })
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, status := range []int{429, 409} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":"synthetic"}`)))
		if w.Code != status {
			t.Fatalf("status %d, want %d", w.Code, status)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("failed request resent")
	}
	s, _ := r.Session("one")
	if s.Account != "a" {
		t.Fatal("failure moved session")
	}
}
