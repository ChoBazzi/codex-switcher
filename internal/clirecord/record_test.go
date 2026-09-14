package clirecord

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

func TestCompletedRecordOnDiskAndFirstResume(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "private")
	origin := checkpoint.Origin{Project: "synthetic", Worktree: "work", Branch: "main"}
	r, err := Create(parent, origin, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := r.Started("0199a213-81c0-7800-8aa1-bbab2a035a53"); err != nil {
		t.Fatal(err)
	}
	if err := r.Complete(); err != nil {
		t.Fatal(err)
	}
	path, handle := filepath.Join(r.Home, "record.json"), r.Handle
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved metadata
	if json.Unmarshal(before, &saved) != nil || !saved.Ready {
		t.Fatal("completed record not ready on disk")
	}
	if !errors.Is(r.verify([]byte(`{"ready":false}`)), ErrVerification) {
		t.Fatal("mismatch accepted")
	}
	r.Close()
	other := origin
	other.Branch = "other"
	if _, err := Load(parent, handle, other); err == nil {
		t.Fatal("wrong branch accepted")
	}
	r, err = Load(parent, handle, origin)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("resume validation changed record")
	}
}

func TestRecordLifecycle(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "private")
	origin := checkpoint.Origin{Project: "synthetic", Worktree: "work", Branch: "main"}
	r, err := Create(parent, origin, "synthetic-model")
	if err != nil {
		t.Fatal(err)
	}
	handle := r.Handle
	if !ValidHandle(handle) {
		t.Fatal("bad handle")
	}
	if err := r.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := r.Started("0199a213-81c0-7800-8aa1-bbab2a035a53"); err != nil {
		t.Fatal(err)
	}
	if err := r.Complete(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(parent, handle, origin); err == nil {
		t.Fatal("concurrent load accepted")
	}
	r.Close()
	other := origin
	other.Branch = "other"
	if _, err := Load(parent, handle, other); err == nil {
		t.Fatal("foreign branch accepted")
	}
	r, err = Load(parent, handle, origin)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model() != "synthetic-model" || r.Origin().Session == "" {
		t.Fatal("metadata lost")
	}
	if err := r.Begin(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err := Load(parent, handle, origin); err == nil {
		t.Fatal("unfinished run resumed")
	}
	inspect, err := LoadForCheckpoint(parent, handle, origin)
	if err != nil {
		t.Fatal(err)
	}
	inspect.Close()
	if _, err := Load(parent, handle, origin); err == nil {
		t.Fatal("checkpoint inspection enabled resume")
	}
	for _, handle := range []string{"../escape", "", "00000000000000000000000000000000"} {
		if _, err := Load(parent, handle, origin); err == nil {
			t.Fatal("unknown record accepted")
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "00000000000000000000000000000000")); !os.IsNotExist(err) {
		t.Fatal("missing record created")
	}
}
