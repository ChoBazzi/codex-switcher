package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChoBazzi/codex-switcher/internal/clirecord"
	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
	"github.com/ChoBazzi/codex-switcher/internal/wikidraft"
)

func TestCheckpointCommandRejectsMissingIdentity(t *testing.T) {
	for _, args := range [][]string{nil, {"write"}, {"recheck"}, {"recheck", "--conversation", "../escape", "--id", "x"}, {"recheck", "--unknown"}} {
		if checkpointCommand(args, io.Discard) == nil {
			t.Fatal("invalid local recheck accepted")
		}
	}
}

func TestCheckpointRecheckCommandNoResumeRequired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	o, err := projectidentity.Resolve(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(config, "com.bazzi.codex-switcher", "conversations")
	r, err := clirecord.Create(parent, o, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Started("0199a213-81c0-7800-8aa1-bbab2a035a53"); err != nil {
		t.Fatal(err)
	}
	d, err := wikidraft.New(r.Home, r.Origin())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MarkSent(); err != nil {
		t.Fatal(err)
	}
	prompt := d.Prompt()
	if _, err := d.Write([]byte(prompt[strings.Index(prompt, "<!-- codex-switcher:"):])); err != nil {
		t.Fatal(err)
	}
	if err := d.Retain(false); err != nil {
		t.Fatal(err)
	}
	id, handle := d.ID(), r.Handle
	d.Close()
	r.Close()
	var out bytes.Buffer
	if err := checkpointCommand([]string{"recheck", "-C", dir, "--conversation", handle, "--id", id}, &out); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if json.Unmarshal(out.Bytes(), &event) != nil || event["event"] != "checkpoint_rechecked" || event["backup_saved"] != true || event["switch_ready"] != false {
		t.Fatal("incorrect recheck result")
	}
	if _, err := clirecord.Load(parent, handle, o); err == nil {
		t.Fatal("recheck enabled model resume")
	}
}
