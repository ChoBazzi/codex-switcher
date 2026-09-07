package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

func TestProjectCommand(t *testing.T) {
	var out bytes.Buffer
	if err := projectCommand([]string{t.TempDir()}, &out); err != nil {
		t.Fatal(err)
	}
	var origin checkpoint.Origin
	if err := json.Unmarshal(out.Bytes(), &origin); err != nil || len(origin.Project) != 64 || origin.Session != "" {
		t.Fatal("invalid project output")
	}
	out.Reset()
	if err := projectCommand([]string{"a", "b"}, &out); err == nil || out.Len() != 0 {
		t.Fatal("invalid arguments accepted")
	}
}
