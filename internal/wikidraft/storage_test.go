package wikidraft

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

func syntheticSnapshot(t *testing.T, id string) checkpoint.Snapshot {
	t.Helper()
	m := checkpoint.Metadata{ID: id, Origin: checkpoint.Origin{Project: "p", Worktree: "w", Branch: "b", Session: "s"}}
	s, err := checkpoint.CheckBytes(checkpoint.Format(m, "g", "c", "d", "t"), m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBackupRotationIntegrityAndIsolation(t *testing.T) {
	base := t.TempDir()
	for _, id := range []string{"one", "two", "three", "four", "four"} {
		if err := Backup(base, syntheticSnapshot(t, id)); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(base, "p", "w", "b", "s", "snapshots.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved []checkpoint.Snapshot
	if json.Unmarshal(b, &saved) != nil || len(saved) != 3 || saved[0].Metadata.ID != "four" || saved[2].Metadata.ID != "two" {
		t.Fatal("rotation or dedup failed")
	}
	bad := syntheticSnapshot(t, "bad")
	bad.Markdown += "changed"
	if Backup(base, bad) == nil {
		t.Fatal("hash mismatch accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(b, after) {
		t.Fatal("invalid snapshot changed archive")
	}
	other := syntheticSnapshot(t, "other")
	other.Metadata.Origin.Session = "other"
	other, err = checkpoint.CheckBytes(checkpoint.Format(other.Metadata, "g", "c", "d", "t"), other.Metadata)
	if err != nil || Backup(base, other) != nil {
		t.Fatal("independent archive failed")
	}
	after, _ = os.ReadFile(path)
	if !bytes.Equal(b, after) {
		t.Fatal("sessions mixed")
	}
	userHome := t.TempDir()
	if err := os.Symlink(base, filepath.Join(userHome, ".codex")); err != nil {
		t.Fatal(err)
	}
	if BackupInHome(userHome, syntheticSnapshot(t, "symlink")) == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestRecheckPreservedCandidate(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	origin := syntheticSnapshot(t, "unused").Metadata.Origin
	d, err := New(home, origin)
	if err != nil {
		t.Fatal(err)
	}
	id, meta := d.ID(), d.request.Metadata()
	if err := d.MarkSent(); err != nil {
		t.Fatal(err)
	}
	d.Write([]byte("incomplete"))
	if err := d.Retain(false); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := Recheck(home, project, id, origin); err == nil {
		t.Fatal("partial file accepted")
	}
	// User repairs a candidate locally; no generation is scheduled.
	if err := writeAtomic(home, "wiki-candidate.md", checkpoint.Format(meta, "g", "c", "d", "t")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(home, "wiki-request.json"))
	if _, err := Recheck(home, project, id, origin); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(home, "wiki-request.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("recheck reset request state/deadline")
	}
	if _, err := Recheck(home, project, "other", origin); err == nil {
		t.Fatal("foreign request accepted")
	}
	newer, err := New(home, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer newer.Close()
	if _, err := Recheck(home, project, id, origin); err == nil {
		t.Fatal("superseded request accepted")
	}
	if err := newer.MarkSent(); err != nil {
		t.Fatal(err)
	}
	newer.Write(checkpoint.Format(newer.request.Metadata(), "g", "c", "d", "t"))
	if err := newer.Retain(true); err != nil {
		t.Fatal(err)
	}
	if _, err := Recheck(home, project, newer.ID(), origin); err == nil {
		t.Fatal("cancelled request accepted")
	}
}

func TestExpiredRegistrationRechecksWithoutNewDeadline(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	snap := syntheticSnapshot(t, "expired")
	r := registration{snap.Metadata, checkpoint.TimedOut, time.Now().Add(-time.Hour)}
	b, _ := json.Marshal(r)
	if writeAtomic(home, "wiki-request.json", b) != nil || writeAtomic(home, "wiki-candidate.md", []byte(snap.Markdown)) != nil {
		t.Fatal("fixture write failed")
	}
	if _, err := Recheck(home, project, r.Metadata.ID, r.Metadata.Origin); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(home, "wiki-request.json"))
	if !bytes.Equal(b, after) {
		t.Fatal("timer restarted")
	}
}
