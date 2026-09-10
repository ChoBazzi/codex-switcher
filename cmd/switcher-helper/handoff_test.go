package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/clirecord"
	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
	"github.com/ChoBazzi/codex-switcher/internal/wikidraft"
)

type handoffAuth struct{}

func (handoffAuth) Access(slot string, now time.Time) (accounts.Access, error) {
	return accounts.Access{Token: "synthetic", AccountID: "synthetic", ExpiresAt: now.Add(time.Hour)}, nil
}

type handoffQuota struct{ calls int }

func (q *handoffQuota) Fetch(context.Context, accounts.Access) (usage.Data, error) {
	q.calls++
	return usage.Parse([]byte(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`))
}

func TestPrepareAndCancelCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	o, err := projectidentity.Resolve(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := os.UserConfigDir()
	base := filepath.Join(config, "com.bazzi.codex-switcher")
	r, err := clirecord.Create(filepath.Join(base, "conversations"), o, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Started("11111111-1111-4111-8111-111111111111"); err != nil {
		t.Fatal(err)
	}
	if err := r.Complete(); err != nil {
		t.Fatal(err)
	}
	db, err := affinity.Open(filepath.Join(base, "affinity"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Register(r.Origin(), "a", time.Now()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	draft, err := wikidraft.New(r.Home, r.Origin())
	if err != nil {
		t.Fatal(err)
	}
	if err := draft.MarkSent(); err != nil {
		t.Fatal(err)
	}
	prompt := draft.Prompt()
	draft.Write([]byte(prompt[strings.Index(prompt, "<!-- codex-switcher:"):]))
	if err := draft.Retain(false); err != nil {
		t.Fatal(err)
	}
	if _, err := draft.Publish(dir); err != nil {
		t.Fatal(err)
	}
	handle, cp := r.Handle, draft.ID()
	draft.Close()
	r.Close()
	quota := &handoffQuota{}
	var out bytes.Buffer
	args := []string{"prepare", "-C", dir, "--conversation", handle, "--checkpoint", cp, "--to", "b", "--confirm-boundary"}
	if err := handoffCommandWithUsage(args, &out, handoffAuth{}, quota); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if json.Unmarshal(out.Bytes(), &event) != nil || event["event"] != "handoff_prepared" || event["model_requests"] != float64(0) || quota.calls != 1 {
		t.Fatal("prepare result invalid")
	}
	id := event["handoff_id"].(string)
	var status bytes.Buffer
	statusArgs := []string{"status", "-C", dir, "--conversation", handle, "--checkpoint", cp}
	if err := handoffCommandWithUsage(statusArgs, &status, handoffAuth{}, quota); err != nil {
		t.Fatal(err)
	}
	var statusEvent map[string]any
	if json.Unmarshal(status.Bytes(), &statusEvent) != nil || statusEvent["state"] != "pending" || statusEvent["handoff_id"] != id || quota.calls != 1 {
		t.Fatal("status did not recover exact reservation without network")
	}
	if err := checkpointCommand([]string{"recheck", "-C", dir, "--conversation", handle, "--id", cp}, &out); err == nil {
		t.Fatal("reserved checkpoint rechecked")
	}
	if err := handoffCommandWithUsage([]string{"cancel", "-C", dir, "--conversation", handle, "--id", id}, &out, handoffAuth{}, quota); err != nil {
		t.Fatal(err)
	}
	if quota.calls != 1 {
		t.Fatal("cancel made usage request")
	}
	status.Reset()
	if err := handoffCommandWithUsage(statusArgs, &status, handoffAuth{}, quota); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(status.Bytes(), &statusEvent) != nil || statusEvent["state"] != "absent" || quota.calls != 1 {
		t.Fatal("cancelled status invalid")
	}
	db, err = affinity.Open(filepath.Join(base, "affinity"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Handoff(id); err == nil {
		t.Fatal("cancelled reservation retained")
	}
}
