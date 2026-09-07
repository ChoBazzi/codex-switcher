// Package clirun runs one explicit user turn in an isolated CLI process.
package clirun

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/livetest"
)

var ErrRun = errors.New("cli_run_failed")

type runFailure struct{ stage string }

func (e *runFailure) Error() string { return ErrRun.Error() }
func (e *runFailure) Unwrap() error { return ErrRun }
func failure(stage string) error    { return &runFailure{stage: stage} }

// FailureStage never returns arbitrary error text.
func FailureStage(err error) string {
	if err == nil {
		return "completed"
	}
	var e *runFailure
	if errors.As(err, &e) {
		return e.stage
	}
	return "run_or_cleanup_failed"
}

type Options struct {
	Binary, Parent, Directory, Endpoint, Secret, Model, Prompt string
	// Home is caller-owned, private and locked; empty preserves ephemeral tests.
	Home, Resume string
}

// Run never retries. Resume must be verified by the caller before invocation.
// Only agent text reaches output; raw events and stderr never enter diagnostics.
func Run(ctx context.Context, o Options, started func(string) error, output io.Writer) (err error) {
	if strings.TrimSpace(o.Prompt) == "" || len(o.Prompt) > 1<<20 || started == nil || output == nil {
		return failure("invalid_arguments")
	}
	profile, err := livetest.Profile(o.Endpoint, o.Secret, o.Model)
	if err != nil {
		return failure("profile_validation")
	}
	dir := o.Home
	if dir == "" {
		if o.Resume != "" {
			return failure("invalid_arguments")
		}
		dir, err = os.MkdirTemp(o.Parent, "cli-run-")
		if err != nil {
			return failure("directory_creation")
		}
		defer func() {
			if os.RemoveAll(dir) != nil {
				err = errors.New("cli_run_cleanup_required")
			}
		}()
	}
	profilePath := filepath.Join(dir, "switcher-live-test.config.toml")
	if writeProfile(dir, profilePath, profile) != nil {
		return failure("profile_write")
	}
	defer func() {
		if os.Remove(profilePath) != nil {
			err = errors.New("cli_run_cleanup_required")
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"exec", "--strict-config", "--skip-git-repo-check", "--profile", "switcher-live-test", "--json"}
	if o.Home == "" {
		args = append(args, "--ephemeral")
	}
	if o.Resume != "" {
		args = append(args, "resume", o.Resume)
	}
	args = append(args, "-")
	cmd := exec.CommandContext(ctx, o.Binary, args...)
	cmd.Dir = o.Directory
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir(), "CODEX_HOME=" + dir,
		"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
	cmd.Stdin = strings.NewReader(o.Prompt)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	// os/exec owns the copying goroutine, so WaitDelay also bounds inherited pipes.
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	if cmd.Start() != nil {
		reader.Close()
		writer.Close()
		return failure("process_start")
	}
	done := make(chan error, 1)
	go func() { e := cmd.Wait(); writer.Close(); done <- e }()
	scanErr := consume(reader, started, output)
	if scanErr != nil {
		cancel()
	}
	reader.Close()
	runErr := <-done
	if scanErr != nil {
		return scanErr
	}
	if ctx.Err() != nil {
		return failure("canceled")
	}
	if runErr != nil {
		return failure("process_exit")
	}
	return nil
}

func writeProfile(dir, target, profile string) error {
	f, err := os.CreateTemp(dir, "profile-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := io.WriteString(f, profile)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), target)
}

func consume(input io.Reader, started func(string) error, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	seen, completed := false, false
	for scanner.Scan() {
		var event struct {
			Type   string `json:"type"`
			Thread string `json:"thread_id"`
			Item   struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type == "" {
			return failure("event_parse")
		}
		switch event.Type {
		case "thread.started":
			if seen || completed {
				return failure("event_order")
			}
			if started(event.Thread) != nil {
				return failure("session_registration")
			}
			seen = true
		case "turn.completed":
			if !seen || completed {
				return failure("event_order")
			}
			completed = true
		case "turn.failed", "error":
			return failure("cli_error_event")
		case "item.completed":
			if !seen || completed {
				return failure("event_order")
			}
			if event.Item.Type == "agent_message" {
				if _, err := io.WriteString(output, event.Item.Text+"\n"); err != nil {
					return failure("answer_output")
				}
			}
		}
	}
	if scanner.Err() != nil {
		return failure("event_read")
	}
	if !seen {
		return failure("missing_start_event")
	}
	if !completed {
		return failure("missing_completion_event")
	}
	return nil
}
