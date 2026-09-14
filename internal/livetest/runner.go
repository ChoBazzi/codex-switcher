package livetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Run launches a new ephemeral CLI with a fixed greeting prompt in a private
// empty directory. Neither real tokens nor prior conversation files enter it.
func Run(ctx context.Context, binary, parent, endpoint, secret, model string) (answer string, err error) {
	profile, err := Profile(endpoint, secret, model)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, "live-test-")
	if err != nil {
		return "", errors.New("live_test_directory_failed")
	}
	defer func() {
		if os.RemoveAll(dir) != nil {
			err = errors.New("live_test_cleanup_required")
		}
	}()
	if os.WriteFile(filepath.Join(dir, "switcher-live-test.config.toml"), []byte(profile), 0600) != nil {
		return "", errors.New("live_test_profile_failed")
	}
	cmd := exec.CommandContext(ctx, binary, "exec", "--strict-config", "--ephemeral", "--skip-git-repo-check", "--profile", "switcher-live-test", "--json", "짧게 안녕이라고만 답해줘. 도구를 사용하지 마.")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir(), "CODEX_HOME=" + dir,
		"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off"}
	var stdout, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	if cmd.Run() != nil || ctx.Err() != nil {
		return "", errors.New("live_cli_request_failed")
	}
	if stdout.overflow {
		return "", errors.New("live_cli_output_too_large")
	}
	for _, line := range bytes.Split(stdout.data.Bytes(), []byte{'\n'}) {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "item.completed" && event.Item.Type == "agent_message" {
			answer = event.Item.Text
		}
	}
	if answer == "" {
		return "", errors.New("live_cli_answer_missing")
	}
	return answer, nil
}

type boundedOutput struct {
	data     bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (1 << 20) - b.data.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data.Write(p)
	return n, nil
}
