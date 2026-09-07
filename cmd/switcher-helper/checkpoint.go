package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/clirecord"
	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
	"github.com/ChoBazzi/codex-switcher/internal/wikidraft"
)

func checkpointCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "recheck" {
		return errors.New("usage: checkpoint recheck -C DIRECTORY --conversation HANDLE --id CHECKPOINT_ID")
	}
	f := flag.NewFlagSet("checkpoint recheck", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	directory := f.String("C", ".", "project root")
	handle := f.String("conversation", "", "source conversation")
	id := f.String("id", "", "exact checkpoint request")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || !clirecord.ValidHandle(*handle) || *id == "" {
		return checkpoint.ErrIdentity
	}
	dir, err := filepath.Abs(*directory)
	if err != nil {
		return projectidentity.ErrIdentity
	}
	origin, err := projectidentity.Resolve(context.Background(), dir)
	if err != nil {
		return err
	}
	parent, err := os.UserConfigDir()
	if err != nil {
		return clirecord.ErrRecord
	}
	db, err := affinity.Open(filepath.Join(parent, "com.bazzi.codex-switcher", "affinity"))
	if err != nil {
		return err
	}
	defer db.Close()
	record, err := clirecord.LoadForCheckpoint(filepath.Join(parent, "com.bazzi.codex-switcher", "conversations"), *handle, origin)
	if err != nil {
		return err
	}
	defer record.Close()
	if reserved, err := db.CheckpointReserved(record.Origin().Session, *id); err != nil {
		return err
	} else if reserved {
		return affinity.ErrConflict
	}
	snap, err := wikidraft.Recheck(record.Home, dir, *id, record.Origin())
	if err != nil {
		return err
	}
	if err := backupCheckpoint(snap); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"event": "checkpoint_rechecked", "checkpoint_id": snap.Metadata.ID, "sha256": snap.SHA256, "backup_saved": true, "switch_ready": false})
}

func backupCheckpoint(snap checkpoint.Snapshot) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return wikidraft.ErrPublish
	}
	// Fixed location from ADR 0006; no environment-provided alternate Codex home.
	return wikidraft.BackupInHome(home, snap)
}
