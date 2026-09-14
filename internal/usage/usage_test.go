package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
)

const sample = `{"account_id":"synthetic-private-id","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20,"limit_window_seconds":18000,"reset_at":2000000000},"secondary_window":{"used_percent":50,"limit_window_seconds":604800,"reset_at":2000600000}}}`

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name, body, level string
		remaining         *float64
	}{
		{"normal", sample, "at_or_below_50", number(50)},
		{"ten", strings.Replace(sample, `"used_percent":50`, `"used_percent":90`, 1), "at_or_below_10", number(10)},
		{"zero_used", `{"rate_limit":{"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`, "above_50", number(100)},
		{"exhausted", `{"rate_limit":{"primary_window":{"used_percent":100},"secondary_window":{"used_percent":0}}}`, "at_or_below_10", number(0)},
		{"missing_window", `{"rate_limit":{"primary_window":{"used_percent":20}}}`, "unknown", nil},
		{"missing_percent", `{"rate_limit":{"primary_window":{},"secondary_window":{"used_percent":20}}}`, "unknown", nil},
		{"null", `{"rate_limit":null}`, "unknown", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Parse([]byte(tc.body))
			if err != nil || d.CheckpointLevel != tc.level {
				t.Fatalf("parse: %v %+v", err, d)
			}
			if (d.RemainingPercent == nil) != (tc.remaining == nil) {
				t.Fatal("unknown changed into a value")
			}
			if tc.remaining != nil && *tc.remaining != *d.RemainingPercent {
				t.Fatal("incorrect minimum")
			}
			out, _ := json.Marshal(d)
			if strings.Contains(string(out), "synthetic-private-id") {
				t.Fatal("identifier leaked")
			}
		})
	}
	d, _ := Parse([]byte(sample))
	if *d.Primary.LimitSeconds != 18000 || d.Primary.ResetAt.Unix() != 2000000000 {
		t.Fatal("window metadata lost")
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{"rate_limit":false}`, `{"rate_limit":{"primary_window":{"used_percent":-1}}}`, `{"rate_limit":{"primary_window":{"used_percent":101}}}`, `{"rate_limit":{"primary_window":{"used_percent":"20"}}}`, `{"rate_limit":{"primary_window":{"limit_window_seconds":0}}}`, `{"rate_limit":{"primary_window":{"reset_at":253402300800}}}`, sample + `{}`, strings.Repeat(" ", MaxBytes+1)} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("accepted invalid body: %.100s", body)
		}
	}
}

func number(v float64) *float64 { return &v }
func access() accounts.Access {
	return accounts.Access{Token: "synthetic-token", AccountID: "synthetic-id", ExpiresAt: time.Now().Add(time.Hour)}
}

func TestClientSingleAttempt(t *testing.T) {
	for _, status := range []int{200, 302, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls, redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-token" || r.Header.Get("ChatGPT-Account-ID") != "synthetic-id" {
					t.Error("incorrect credentials or method")
				}
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprint(w, sample)
				} else {
					fmt.Fprint(w, "synthetic-private-error")
				}
			}))
			defer srv.Close()
			c, err := newClient(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			d, err := c.Fetch(context.Background(), access())
			if calls.Load() != 1 || redirected.Load() != 0 {
				t.Fatal("request retried or redirected")
			}
			if status == 200 {
				if err != nil || d.RemainingPercent == nil {
					t.Fatal("success missing")
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) || e.HTTPStatus != status || strings.Contains(err.Error(), "synthetic") {
				t.Fatal("unsafe/missing error")
			}
			if status == 429 && e.Code != "usage_rate_limited" {
				t.Fatal("429 inferred quota exhaustion")
			}
		})
	}
}

func TestClientInvalidDataAndAccess(t *testing.T) {
	for _, body := range []string{`{`, strings.Repeat("x", MaxBytes+1)} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		c, _ := newClient(srv.URL)
		_, err := c.Fetch(context.Background(), access())
		c.Close()
		srv.Close()
		if err == nil {
			t.Fatal("accepted invalid body")
		}
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c, _ := newClient(srv.URL)
	defer c.Close()
	if _, err := c.Fetch(context.Background(), accounts.Access{}); err == nil {
		t.Fatal("accepted expired access")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Fetch(ctx, access()); err == nil {
		t.Fatal("ignored cancellation")
	}
	if calls.Load() != 0 {
		t.Fatal("sent rejected request")
	}
	for _, endpoint := range []string{"https://example.com", "http://localhost/usage", "http://127.0.0.1/?token=x", "http://user@127.0.0.1"} {
		if _, err := newClient(endpoint); err == nil {
			t.Fatal("accepted untrusted endpoint")
		}
	}
}

type sourceFunc func(string, time.Time) (accounts.Access, error)

func (f sourceFunc) Access(s string, n time.Time) (accounts.Access, error) { return f(s, n) }

type fetchFunc func(context.Context, accounts.Access) (Data, error)

func (f fetchFunc) Fetch(c context.Context, a accounts.Access) (Data, error) { return f(c, a) }

func TestMonitorStaleAndIndependentSlots(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int32
	m := NewMonitor(sourceFunc(func(s string, _ time.Time) (accounts.Access, error) { a := access(); a.AccountID = s; return a, nil }), fetchFunc(func(_ context.Context, a accounts.Access) (Data, error) {
		calls.Add(1)
		if fail.Load() && a.AccountID == "a" {
			return Data{}, failure("usage_rate_limited", 429)
		}
		return Parse([]byte(sample))
	}))
	first := m.Refresh(context.Background(), []string{"a", "b"})
	if first[0].Stale || first[0].State != "ok" {
		t.Fatal("initial sample failed")
	}
	fail.Store(true)
	next := m.Refresh(context.Background(), []string{"a", "b"})
	if calls.Load() != 4 || !next[0].Stale || next[0].State != "rate_limited" || next[0].Usage == nil || !next[0].LastSuccess.Equal(*first[0].LastSuccess) || next[1].Stale {
		t.Fatal("independent stale retention failed")
	}
	if !first[0].At(first[0].LastSuccess.Add(StaleAfter)).Stale {
		t.Fatal("sample does not age")
	}
	if first[0].At(*first[0].LastSuccess).Stale {
		t.Fatal("fresh sample marked stale")
	}
}

func TestMonitorUnavailableAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		err   error
		state string
	}{{accounts.ErrExpired, "auth_expired"}, {accounts.ErrNotRegistered, "not_registered"}, {errors.New("synthetic-secret"), "auth_error"}} {
		m := NewMonitor(sourceFunc(func(string, time.Time) (accounts.Access, error) { return accounts.Access{}, tc.err }), fetchFunc(func(context.Context, accounts.Access) (Data, error) {
			t.Error("fetched unavailable account")
			return Data{}, nil
		}))
		s := m.Refresh(context.Background(), []string{"a"})[0]
		if s.State != tc.state || !s.Stale || s.Usage != nil || strings.Contains(s.ErrorCode, "synthetic") {
			t.Fatal("unsafe/unexpected state")
		}
	}
	m := NewMonitor(sourceFunc(func(string, time.Time) (accounts.Access, error) { return access(), nil }), fetchFunc(func(context.Context, accounts.Access) (Data, error) { return Parse([]byte(`{"rate_limit":null}`)) }))
	if s := m.Refresh(context.Background(), []string{"a"})[0]; s.State != "unknown" || s.Usage.RemainingPercent != nil {
		t.Fatal("unknown invented quota")
	}
}

func TestMonitorScheduleNoImmediateRetry(t *testing.T) {
	var calls atomic.Int32
	m := NewMonitor(sourceFunc(func(string, time.Time) (accounts.Access, error) { return access(), nil }), fetchFunc(func(context.Context, accounts.Access) (Data, error) {
		calls.Add(1)
		return Data{}, failure("usage_http_error", 500)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	outputs := make(chan []Snapshot, 2)
	done := make(chan error, 1)
	go func() {
		done <- m.run(ctx, []string{"a"}, ticks, func(s []Snapshot) error { outputs <- s; return nil })
	}()
	select {
	case <-outputs:
	case <-time.After(time.Second):
		t.Fatal("no initial poll")
	}
	select {
	case <-outputs:
		t.Fatal("immediate retry")
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case ticks <- time.Now():
	case <-time.After(time.Second):
		t.Fatal("not waiting for tick")
	}
	select {
	case <-outputs:
	case <-time.After(time.Second):
		t.Fatal("no periodic poll")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not stop")
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected retries")
	}
}
