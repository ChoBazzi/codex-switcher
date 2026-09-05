package applock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExclusiveLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Acquire(dir); !errors.Is(err, ErrBusy) {
		if second != nil {
			second.Close()
		}
		t.Fatal("concurrent lock accepted")
	}
	first.Close()
	third, err := Acquire(dir)
	if err != nil {
		t.Fatal("lock not released")
	}
	third.Close()
}

func TestRejectSymlinkLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(dir, "account-operation.lock")); err != nil {
		t.Fatal(err)
	}
	if l, err := Acquire(dir); err == nil {
		l.Close()
		t.Fatal("symlink lock accepted")
	}
}
