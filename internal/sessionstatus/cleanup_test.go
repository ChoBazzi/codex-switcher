package sessionstatus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func privateStatusDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "status")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeSample(t *testing.T, dir string, s Session) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, s.Conversation+".json")
	if err := os.WriteFile(name, b, 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestCleanupRetention(t *testing.T) {
	dir := privateStatusDir(t)
	now := time.Now().UTC()
	for i, tc := range []struct {
		state             string
		age, heartbeatAge time.Duration
		removed           bool
	}{
		{"closed", 15 * 24 * time.Hour, 15 * 24 * time.Hour, true},
		{"failed", 15 * 24 * time.Hour, 15 * 24 * time.Hour, true},
		{"active", 15 * 24 * time.Hour, 15 * 24 * time.Hour, true}, // crashed publisher
		{"closed", 14 * 24 * time.Hour, 14 * 24 * time.Hour, false},
		{"active", 15 * 24 * time.Hour, time.Second, false}, // long-running publisher
		{"waiting", time.Second, 15 * 24 * time.Hour, false},
		{"closed", -time.Hour, -time.Hour, false},
	} {
		s := fixture("a", "a")
		s.Conversation = fmt.Sprintf("%032x", i)
		s.State, s.UpdatedAt, s.Heartbeat = tc.state, now.Add(-tc.age), now.Add(-tc.heartbeatAge)
		name := writeSample(t, dir, s)
		if err := Cleanup(dir, now); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(name)
		if os.IsNotExist(err) != tc.removed {
			t.Fatalf("case %d: removed=%v, err=%v", i, tc.removed, err)
		}
	}
}

func TestCleanupBeyondReadLimit(t *testing.T) {
	dir := privateStatusDir(t)
	now := time.Now().UTC()
	for i := 0; i < 4100; i++ {
		s := fixture("a", "a")
		s.Conversation = fmt.Sprintf("%032x", i)
		s.State, s.UpdatedAt, s.Heartbeat = "closed", now.Add(-15*24*time.Hour), now.Add(-15*24*time.Hour)
		writeSample(t, dir, s)
	}
	if _, err := Read(dir, now); err == nil {
		t.Fatal("expected read cap")
	}
	if err := Cleanup(dir, now); err != nil {
		t.Fatal(err)
	}
	sessions, err := Read(dir, now)
	if err != nil || len(sessions) != 0 {
		t.Fatal(sessions, err)
	}
}

func TestCleanupRejectsUnsafeFile(t *testing.T) {
	dir := privateStatusDir(t)
	target := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(target, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, strings.Repeat("a", 32)+".json")); err != nil {
		t.Fatal(err)
	}
	if Cleanup(dir, time.Now()) == nil {
		t.Fatal("accepted symlink")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupPreservesPublisher(t *testing.T) {
	dir := privateStatusDir(t)
	p, err := Start(dir, fixture("a", "a"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close("closed")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			p.Set("active")
		}
	}()
	for i := 0; i < 50; i++ {
		if err := Cleanup(dir, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	s, err := Read(dir, time.Now())
	if err != nil || len(s) != 1 || s[0].State != "active" {
		t.Fatal(s, err)
	}
}

func TestReadKeepsActiveBeforeClosed(t *testing.T) {
	dir := privateStatusDir(t)
	now := time.Now().UTC()
	for i := 0; i < 65; i++ {
		s := fixture("a", "a")
		s.Conversation = fmt.Sprintf("%032x", i)
		s.State, s.UpdatedAt, s.Heartbeat = "closed", now, now
		if i == 64 {
			s.State, s.UpdatedAt = "active", now.Add(-time.Hour)
		}
		writeSample(t, dir, s)
	}
	s, err := Read(dir, now)
	if err != nil || len(s) != 64 || s[0].State != "active" {
		t.Fatal(s, err)
	}
}
