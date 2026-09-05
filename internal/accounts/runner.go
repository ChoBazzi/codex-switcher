package accounts

import (
	"context"
	"io"
	"os"
	"os/exec"
	"time"
)

type CodexRunner struct{ Binary string }

func (r CodexRunner) Run(ctx context.Context, dir string, waiting func()) error {
	cmd := exec.CommandContext(ctx, r.Binary, "login", "-c", `cli_auth_credentials_store="file"`, "-c", `forced_login_method="chatgpt"`)
	cmd.Dir = dir
	cmd.Env = loginEnvironment(dir)
	// CLI output may contain OAuth URLs and state. It is never forwarded to logs.
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return ErrLogin
	}
	waiting()
	if cmd.Wait() != nil {
		return ErrLogin
	}
	return nil
}

func loginEnvironment(dir string) []string {
	env := []string{"CODEX_HOME=" + dir}
	for _, key := range []string{"HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
