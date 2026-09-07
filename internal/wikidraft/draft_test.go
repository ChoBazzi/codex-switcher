package wikidraft

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

func TestPublishAndPreservePrevious(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	origin := checkpoint.Origin{Project: "synthetic", Worktree: "work", Branch: "branch", Session: "session"}
	d, err := New(home, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !strings.Contains(d.Prompt(), d.request.Metadata().ID) {
		t.Fatal("missing request identity")
	}
	if err := d.MarkSent(); err != nil {
		t.Fatal(err)
	}
	b := checkpoint.Format(d.request.Metadata(), "synthetic goal", "none", "none", "synthetic next step")
	if _, err := d.Write(b); err != nil {
		t.Fatal(err)
	}
	snap, err := d.Publish(project)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, ".codex-switcher", "work", "branch", "session", "checkpoint.md")
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(saved, b) || snap.Markdown != string(b) {
		t.Fatal("published bytes differ")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe permissions")
	}
	bad, err := New(home, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	if err := bad.MarkSent(); err != nil {
		t.Fatal(err)
	}
	bad.Write(b) // A prior request is not this request.
	if _, err := bad.Publish(project); !errors.Is(err, checkpoint.ErrIdentity) {
		t.Fatalf("old ID accepted: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(saved, after) {
		t.Fatal("failed write replaced prior checkpoint")
	}
}

func TestDraftLimitsAndSymlink(t *testing.T) {
	origin := checkpoint.Origin{Project: "synthetic", Worktree: "work", Branch: "branch", Session: "session"}
	for _, mode := range []string{"oversize", "unsent", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			project := t.TempDir()
			d, err := New(t.TempDir(), origin)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if mode != "unsent" {
				if err := d.MarkSent(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "oversize" {
				if _, err := d.Write(bytes.Repeat([]byte("x"), checkpoint.MaxBytes+1)); !errors.Is(err, checkpoint.ErrSize) {
					t.Fatal("oversized write accepted")
				}
			} else {
				d.Write(checkpoint.Format(d.request.Metadata(), "g", "c", "d", "t"))
			}
			if mode == "symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Join(project, ".codex-switcher")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := d.Publish(project); err == nil {
				t.Fatal("invalid publication accepted")
			}
		})
	}
}
