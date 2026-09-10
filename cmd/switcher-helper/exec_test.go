package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/routing"
	"io"
	"strings"
	"testing"
)

func TestRegistrationDiagnosticRedactsUnderlyingError(t *testing.T) {
	err := errors.Join(routing.ErrUsageUnavailable, errors.New("synthetic-secret"))
	code := registrationFailureCode(err)
	if code != "routing_usage_unavailable" {
		t.Fatal(code)
	}
	var out bytes.Buffer
	if writeExecDiagnostics(&out, proxy.Diagnostics{}, err, code) != nil {
		t.Fatal("write")
	}
	if strings.Contains(out.String(), "synthetic-secret") || !strings.Contains(out.String(), `"session_registration_code":"routing_usage_unavailable"`) {
		t.Fatal(out.String())
	}
	if registrationFailureCode(errors.New("synthetic-secret")) != "cli_session_binding_unavailable" {
		t.Fatal("unknown error leaked")
	}
}

func TestExecRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {" "}, {"a", "b"}, {"--unknown"}, {"--model", "bad\nmodel", "synthetic"}, {"--checkpoint"}, {"--checkpoint", "--resume", "00000000000000000000000000000000", "extra prompt"}} {
		if execCommand(args, io.Discard, io.Discard) == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
}

func TestExecDiagnostics(t *testing.T) {
	var out bytes.Buffer
	d := proxy.Diagnostics{Requests: 1, Attempts: 0, Status: 400, Rejection: "unsupported_persistent_request"}
	if writeExecDiagnostics(&out, d, errors.New("synthetic-secret")) != nil {
		t.Fatal("write failed")
	}
	var event map[string]any
	if json.Unmarshal(out.Bytes(), &event) != nil || event["succeeded"] != false || event["local_rejection_code"] != d.Rejection || event["cli_stage"] != "run_or_cleanup_failed" {
		t.Fatal("invalid diagnostic")
	}
	if strings.Contains(out.String(), "synthetic-secret") {
		t.Fatal("error leaked")
	}
}

func TestExecHandoffRejectsAmbiguousInvocation(t *testing.T) {
	for _, args := range [][]string{
		{"--handoff", "", "synthetic"},
		{"--handoff", "synthetic"},
		{"--handoff", "synthetic", "--resume", "00000000000000000000000000000000", "synthetic"},
		{"--handoff", "synthetic", "--checkpoint", "--resume", "00000000000000000000000000000000"},
	} {
		if execCommand(args, io.Discard, io.Discard) == nil {
			t.Fatal("ambiguous handoff accepted")
		}
	}
}
