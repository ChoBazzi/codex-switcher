package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Request, string, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	r, err := New(dir, Origin{"p", "w", "b", "s"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	return r, dir, &now
}

func publish(t *testing.T, dir string, b []byte) {
	t.Helper()
	tmp := filepath.Join(dir, "checkpoint.tmp")
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "checkpoint.md")); err != nil {
		t.Fatal(err)
	}
}

func valid(r *Request) []byte { return Format(r.Metadata(), "goal", "changes", "없음", "todo") }

func TestDeadlineAndManualRecheck(t *testing.T) {
	r, dir, now := fixture(t)
	*now = now.Add(time.Hour)
	if r.Status().State != Pending {
		t.Fatal("timer started before dispatch")
	}
	if err := r.MarkSent(); err != nil {
		t.Fatal(err)
	}
	deadline := r.Status().Deadline
	*now = now.Add(Timeout)
	publish(t, dir, valid(r))
	if _, err := r.Validate(); !errors.Is(err, ErrTimeout) {
		t.Fatalf("late automatic validation: %v", err)
	}
	if _, err := r.Recheck(); err != nil {
		t.Fatal(err)
	}
	if r.Status().Deadline != deadline {
		t.Fatal("manual check reset deadline")
	}
	if _, err := r.Consume(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Recheck(); !errors.Is(err, ErrState) {
		t.Fatal("consumed request revived")
	}
}

func TestSnapshotSurvivesFileMutation(t *testing.T) {
	r, dir, _ := fixture(t)
	r.MarkSent()
	original := valid(r)
	publish(t, dir, original)
	s, err := r.Validate()
	if err != nil {
		t.Fatal(err)
	}
	publish(t, dir, []byte("changed"))
	got, err := r.Consume()
	if err != nil || got.Markdown != string(original) || got.SHA256 != s.SHA256 {
		t.Fatal("snapshot not pinned")
	}
}

func TestRejectInvalidFiles(t *testing.T) {
	for _, kind := range []string{"id", "session", "partial", "empty", "oversize", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			r, dir, _ := fixture(t)
			r.MarkSent()
			meta := r.Metadata()
			if kind == "id" {
				meta.ID = "old"
			}
			if kind == "session" {
				meta.Origin.Session = "other"
			}
			b := Format(meta, "goal", "changes", "none", "todo")
			if kind == "partial" {
				b = b[:len(b)-10]
			}
			if kind == "empty" {
				b = Format(meta, "", "changes", "none", "todo")
			}
			if kind == "oversize" {
				b = []byte(strings.Repeat("x", MaxBytes+1))
			}
			if kind == "symlink" {
				outside := filepath.Join(t.TempDir(), "other.md")
				if err := os.WriteFile(outside, b, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(dir, "checkpoint.md")); err != nil {
					t.Fatal(err)
				}
			} else if kind == "directory" {
				if err := os.Mkdir(filepath.Join(dir, "checkpoint.md"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				publish(t, dir, b)
			}
			if _, err := r.Validate(); err == nil {
				t.Fatal("invalid file accepted")
			}
		})
	}
}

func TestCancelledCannotRevive(t *testing.T) {
	r, dir, _ := fixture(t)
	r.MarkSent()
	r.Cancel()
	publish(t, dir, valid(r))
	if _, err := r.Recheck(); !errors.Is(err, ErrState) {
		t.Fatal("cancelled request revived")
	}
}

func TestFailureRequiresManualCheck(t *testing.T) {
	r, dir, _ := fixture(t)
	r.MarkSent()
	if _, err := r.Validate(); !errors.Is(err, ErrFile) {
		t.Fatal(err)
	}
	publish(t, dir, valid(r))
	if _, err := r.Validate(); !errors.Is(err, ErrState) {
		t.Fatal("automatic retry allowed")
	}
	if _, err := r.Recheck(); err != nil {
		t.Fatal(err)
	}
}
