package sessionstatus

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(id, slot string) Session {
	return Session{Conversation: strings.Repeat(id, 32), Project: strings.Repeat("1", 64), Worktree: strings.Repeat("2", 64), Branch: strings.Repeat("3", 64), Slot: slot}
}

func TestLifecycleAndCrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "status")
	p, err := Start(dir, fixture("a", "a"))
	if err != nil {
		t.Fatal(err)
	}
	p.Set("active")
	s, err := Read(dir, time.Now())
	if err != nil || len(s) != 1 || s[0].State != "active" {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = Read(dir, time.Now().Add(6*time.Second))
	if err != nil || s[0].State != "disconnected" {
		t.Fatal(s, err)
	}
	p.Close("closed")
	s, err = Read(dir, time.Now().Add(6*time.Second))
	if err != nil || s[0].State != "closed" {
		t.Fatal(s, err)
	}
}

func TestMultipleSessionsAndFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "status")
	a, e := Start(dir, fixture("a", "a"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := Start(dir, fixture("b", "b"))
	if e != nil {
		t.Fatal(e)
	}
	a.Set("active")
	b.Set("active")
	a.Close("failed")
	b.Close("closed")
	s, e := Read(dir, time.Now())
	if e != nil || len(s) != 2 {
		t.Fatal(s, e)
	}
	seen := map[string]string{}
	for _, v := range s {
		seen[v.Slot] = v.State
	}
	if seen["a"] != "failed" || seen["b"] != "closed" {
		t.Fatal(seen)
	}
	s, e = Read(dir, time.Now().Add(25*time.Hour))
	if e != nil || len(s) != 0 {
		t.Fatal(s, e)
	}
}

func TestInvalidAndSymlink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "status")
	bad := fixture("a", "a")
	bad.Project = "actual-directory-name"
	if _, e := Start(dir, bad); e == nil {
		t.Fatal("accepted non-hash")
	}
	p, e := Start(dir, fixture("a", "a"))
	if e != nil {
		t.Fatal(e)
	}
	p.Close("closed")
	name := filepath.Join(dir, strings.Repeat("b", 32)+".json")
	if os.Symlink(filepath.Join(dir, strings.Repeat("a", 32)+".json"), name) != nil {
		t.Fatal("symlink")
	}
	if _, e := Read(dir, time.Now()); e == nil {
		t.Fatal("accepted symlink")
	}
}

func TestAPIAuthAndReadOnly(t *testing.T) {
	secret := strings.Repeat("x", 32)
	dir := filepath.Join(t.TempDir(), "status")
	h := Handler(dir, secret)
	for _, tc := range []struct {
		method, token, origin string
		want                  int
	}{
		{"GET", "", "", 401}, {"GET", "wrong", "", 401}, {"POST", secret, "", 405}, {"GET", secret, "https://example.invalid", 403}, {"GET", secret, "", 200},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1/control/sessions", nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%d != %d", w.Code, tc.want)
		}
		if w.Code == 200 {
			var out struct{ Sessions []Session }
			if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Sessions) != 0 {
				t.Fatal("invalid empty snapshot")
			}
		}
	}
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("read API created state")
	}
}

func TestConcurrentObservation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "status")
	p, e := Start(dir, fixture("a", "a"))
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close("closed")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			p.Set("active")
		}
	}()
	for i := 0; i < 100; i++ {
		if _, e := Read(dir, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	<-done
}

func TestAPIPublishesOnlyMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "status")
	p, err := Start(dir, fixture("a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close("closed")
	p.Set("active")
	secret := strings.Repeat("private-test-secret-", 2)
	r := httptest.NewRequest("GET", "http://127.0.0.1/control/sessions", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	Handler(dir, secret).ServeHTTP(w, r)
	var out struct {
		Event    string    `json:"event"`
		Sessions []Session `json:"sessions"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Event != "session_snapshot" || len(out.Sessions) != 1 || out.Sessions[0].State != "active" || out.Sessions[0].Slot != "b" {
		t.Fatal("incorrect API snapshot")
	}
	if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), dir) {
		t.Fatal("private values in API response")
	}
	r.Header.Add("Authorization", "Bearer "+secret)
	w = httptest.NewRecorder()
	Handler(dir, secret).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("duplicate auth accepted")
	}
}
