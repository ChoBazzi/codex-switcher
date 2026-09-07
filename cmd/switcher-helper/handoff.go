package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/clirecord"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
	"github.com/ChoBazzi/codex-switcher/internal/wikidraft"
)

func handoffCommand(args []string, out io.Writer) error {
	client := usage.NewClient()
	defer client.Close()
	return handoffCommandWithUsage(args, out, accounts.New(credentialstore.New(), ""), client)
}

func handoffCommandWithUsage(args []string, out io.Writer, access usage.AccessSource, fetcher usage.Fetcher) error {
	if len(args) == 0 || (args[0] != "prepare" && args[0] != "cancel") {
		return errors.New("usage: handoff prepare|cancel -C DIRECTORY --conversation HANDLE [--checkpoint ID --to a|b --confirm-boundary | --id HANDOFF_ID]")
	}
	f := flag.NewFlagSet("handoff", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	directory := f.String("C", ".", "project root")
	handle := f.String("conversation", "", "source conversation")
	cp := f.String("checkpoint", "", "latest checkpoint ID")
	target := f.String("to", "", "target slot")
	id := f.String("id", "", "reservation ID to cancel")
	boundary := f.Bool("confirm-boundary", false, "confirm no unresolved work and the Wiki is current")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || !clirecord.ValidHandle(*handle) {
		return clirecord.ErrRecord
	}
	prepare := args[0] == "prepare"
	if prepare && (*cp == "" || (*target != "a" && *target != "b") || !*boundary || *id != "") {
		return errors.New("handoff_prepare_requires_checkpoint_target_and_boundary")
	}
	if !prepare && (*id == "" || *cp != "" || *target != "" || *boundary) {
		return errors.New("handoff_cancel_requires_id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir, err := filepath.Abs(*directory)
	if err != nil {
		return projectidentity.ErrIdentity
	}
	o, err := projectidentity.Resolve(ctx, dir)
	if err != nil {
		return err
	}
	parent, err := os.UserConfigDir()
	if err != nil {
		return affinity.ErrStorage
	}
	base := filepath.Join(parent, "com.bazzi.codex-switcher")
	db, err := affinity.Open(filepath.Join(base, "affinity"))
	if err != nil {
		return err
	}
	defer db.Close()
	var r *clirecord.Record
	if prepare {
		r, err = clirecord.Load(filepath.Join(base, "conversations"), *handle, o)
	} else {
		r, err = clirecord.LoadForCheckpoint(filepath.Join(base, "conversations"), *handle, o)
	}
	if err != nil {
		return err
	}
	defer r.Close()
	if !prepare {
		if err := db.CancelHandoff(*id, r.Origin()); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"event": "handoff_cancelled", "handoff_id": *id})
	}
	if err := db.Ready(r.Origin()); err != nil {
		return err
	}
	if reserved, err := db.CheckpointReserved(r.Origin().Session, *cp); err != nil {
		return err
	} else if reserved {
		return affinity.ErrConflict
	}
	snap, err := wikidraft.Recheck(r.Home, dir, *cp, r.Origin())
	if err != nil {
		return err
	}
	if err := backupCheckpoint(snap); err != nil {
		return err
	}
	if err := wikidraft.Pin(base, snap); err != nil {
		return err
	}
	router, err := routing.NewPersistent(access, 90, db)
	if err != nil {
		return err
	}
	router.Update(usage.NewMonitor(access, fetcher).Refresh(ctx, []string{*target}))
	reservation, err := router.PrepareHandoff(r.Origin(), *target, snap, true, time.Now())
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"event": "handoff_prepared", "handoff_id": reservation.ID, "target": reservation.Target, "awaiting_user_input": true, "model_requests": 0})
}
