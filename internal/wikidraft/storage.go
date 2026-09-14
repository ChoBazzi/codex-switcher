package wikidraft

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/applock"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

type registration struct {
	Metadata checkpoint.Metadata `json:"metadata"`
	State    checkpoint.State    `json:"state"`
	Deadline time.Time           `json:"deadline"`
}

// Retain preserves the latest candidate for explicit local recheck, including
// invalid/late drafts. It does not publish or make the source resumable.
func (d *Draft) Retain(cancelled bool) error {
	s := d.request.Status()
	state := s.State
	if cancelled || state == checkpoint.Pending {
		state = checkpoint.Cancelled
	}
	if state == checkpoint.Writing {
		state = checkpoint.Failed
	}
	if err := writeAtomic(d.home, "wiki-candidate.md", d.Bytes()); err != nil {
		return err
	}
	b, err := json.Marshal(registration{d.request.Metadata(), state, s.Deadline})
	if err != nil {
		return ErrPublish
	}
	return writeAtomic(d.home, "wiki-request.json", b)
}

// Recheck requires the exact latest request ID. No CLI, account, or network API
// is referenced here. The caller holds the conversation lock.
func Recheck(home, project, id string, origin checkpoint.Origin) (checkpoint.Snapshot, error) {
	b, err := readRegular(filepath.Join(home, "wiki-request.json"), 8192)
	if err != nil {
		return checkpoint.Snapshot{}, err
	}
	var r registration
	if json.Unmarshal(b, &r) != nil || r.Metadata.ID != id || id == "" || r.Metadata.Origin != origin {
		return checkpoint.Snapshot{}, checkpoint.ErrIdentity
	}
	if r.State != checkpoint.Failed && r.State != checkpoint.TimedOut && r.State != checkpoint.Validated {
		return checkpoint.Snapshot{}, checkpoint.ErrState
	}
	b, err = readRegular(filepath.Join(home, "wiki-candidate.md"), checkpoint.MaxBytes)
	if err != nil {
		return checkpoint.Snapshot{}, err
	}
	snap, err := checkpoint.CheckBytes(b, r.Metadata)
	if err != nil {
		return checkpoint.Snapshot{}, err
	}
	if err := publishSnapshot(project, snap); err != nil {
		return checkpoint.Snapshot{}, err
	}
	return snap, nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrPublish
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrPublish
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, ErrPublish
	}
	return b, nil
}

// Backup atomically keeps the three most recent distinct validated snapshots.
// The archive is a single private JSON bundle so pruning cannot delete an
// existing good snapshot before the replacement is durably written.
func Backup(base string, snap checkpoint.Snapshot) error {
	checked, err := checkpoint.CheckBytes([]byte(snap.Markdown), snap.Metadata)
	if err != nil || checked.SHA256 != snap.SHA256 {
		return checkpoint.ErrIdentity
	}
	o := snap.Metadata.Origin
	dir, err := isolatedDirectory(base, []string{o.Project, o.Worktree, o.Branch, o.Session})
	if err != nil {
		return err
	}
	lock, err := applock.Acquire(dir)
	if err != nil {
		return ErrPublish
	}
	defer lock.Close()
	path := filepath.Join(dir, "snapshots.json")
	var previous []checkpoint.Snapshot
	if _, err := os.Lstat(path); err == nil {
		b, err := readRegular(path, 18*checkpoint.MaxBytes+16384)
		if err != nil || json.Unmarshal(b, &previous) != nil || len(previous) > 3 {
			return ErrPublish
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrPublish
	}
	for _, old := range previous {
		check, err := checkpoint.CheckBytes([]byte(old.Markdown), old.Metadata)
		if err != nil || old.Metadata.Origin != o || check.SHA256 != old.SHA256 {
			return ErrPublish
		}
	}
	next := []checkpoint.Snapshot{snap}
	for _, old := range previous {
		if old.Metadata.ID == snap.Metadata.ID && old.SHA256 == snap.SHA256 {
			continue
		}
		if len(next) < 3 {
			next = append(next, old)
		}
	}
	b, err := json.Marshal(next)
	if err != nil {
		return ErrPublish
	}
	return writeAtomic(dir, "snapshots.json", b)
}

func BackupInHome(home string, snap checkpoint.Snapshot) error {
	base, err := isolatedDirectory(home, []string{".codex", "switcher", "projects"})
	if err != nil {
		return err
	}
	return Backup(base, snap)
}

func isolatedDirectory(base string, parts []string) (string, error) {
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", ErrPublish
	}
	defer root.Close()
	rel := "."
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || filepath.Base(part) != part {
			return "", ErrPublish
		}
		rel = filepath.Join(rel, part)
		if err := root.Mkdir(rel, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", ErrPublish
		}
		info, err := root.Lstat(rel)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrPublish
		}
	}
	return filepath.Join(base, rel), nil
}
