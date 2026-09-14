package wikidraft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

func TestPinnedHandoffBodyIsolation(t *testing.T) {
	base := t.TempDir()
	snap := syntheticSnapshot(t, "synthetic-checkpoint")
	snap.Metadata.Origin.Session = "11111111-1111-4111-8111-111111111111"
	snap, err := checkpoint.CheckBytes(checkpoint.Format(snap.Metadata, "g", "c", "d", "t"), snap.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := Pin(base, snap); err != nil {
		t.Fatal(err)
	}
	r := affinity.Reservation{Source: snap.Metadata.Origin, CheckpointID: snap.Metadata.ID, Digest: snap.SHA256}
	pinned, err := ReadPin(base, r)
	if err != nil || pinned.Markdown != snap.Markdown {
		t.Fatal("pin changed")
	}
	prompt, err := HandoffPrompt(pinned, "synthetic user input")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "codex-switcher:") || strings.Contains(prompt, "checkpoint-complete:") || strings.Contains(prompt, snap.Metadata.ID) || !strings.Contains(prompt, "synthetic user input") {
		t.Fatal("metadata leaked or input lost")
	}
	if err := os.WriteFile(filepath.Join(base, "handoff-snapshots", snap.SHA256+".json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPin(base, r); err == nil {
		t.Fatal("modified pin accepted")
	}
	if Pin(base, snap) == nil {
		t.Fatal("damaged pin silently overwritten")
	}
}
