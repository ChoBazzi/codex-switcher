// Package projectidentity derives local launcher metadata without model calls.
package projectidentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

var ErrIdentity = errors.New("project_identity_unavailable")

// Resolve returns an origin with no session: only the CLI start event may set it.
// For non-Git directories, dir itself is the explicit project root.
func Resolve(ctx context.Context, dir string) (checkpoint.Origin, error) {
	root, err := canonical(dir)
	if err != nil {
		return checkpoint.Origin{}, ErrIdentity
	}
	project, worktree, branch := root, root, "non-git"
	gitFound := false
	for p := root; ; p = filepath.Dir(p) {
		_, err := os.Lstat(filepath.Join(p, ".git"))
		if err == nil {
			gitFound = true
			break
		}
		if !os.IsNotExist(err) {
			return checkpoint.Origin{}, ErrIdentity
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if gitFound {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		git := func(args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Dir = root
			for _, entry := range os.Environ() {
				// Inherited Git overrides must not redirect project discovery.
				if !strings.HasPrefix(entry, "GIT_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
			out, err := cmd.Output()
			value := strings.TrimSuffix(string(out), "\n")
			if err != nil || value == "" || strings.ContainsAny(value, "\x00\r\n") {
				return "", ErrIdentity
			}
			return value, nil
		}
		worktree, err = git("rev-parse", "--show-toplevel")
		if err != nil {
			return checkpoint.Origin{}, ErrIdentity
		}
		project, err = git("rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return checkpoint.Origin{}, ErrIdentity
		}
		worktree, err = canonical(worktree)
		if err != nil {
			return checkpoint.Origin{}, ErrIdentity
		}
		project, err = canonical(project)
		if err != nil {
			return checkpoint.Origin{}, ErrIdentity
		}
		ref, refErr := git("symbolic-ref", "--quiet", "HEAD")
		if refErr == nil {
			branch = "ref:" + ref
		} else {
			head, headErr := git("rev-parse", "--verify", "HEAD")
			if headErr != nil {
				return checkpoint.Origin{}, ErrIdentity
			}
			branch = "detached:" + head
		}
	}
	if ctx.Err() != nil {
		return checkpoint.Origin{}, ErrIdentity
	}
	return checkpoint.Origin{Project: digest("project", project), Worktree: digest("worktree", worktree), Branch: digest("branch", branch)}, nil
}

func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", ErrIdentity
	}
	return abs, nil
}

func digest(kind, value string) string {
	sum := sha256.Sum256([]byte("switcher-origin-v1\x00" + kind + "\x00" + value))
	return hex.EncodeToString(sum[:])
}
