package projectidentity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitIdentity(t *testing.T) {
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Synthetic", "GIT_AUTHOR_EMAIL=synthetic@example.invalid", "GIT_COMMITTER_NAME=Synthetic", "GIT_COMMITTER_EMAIL=synthetic@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	resolve := func(dir string) [3]string {
		t.Helper()
		o, err := Resolve(context.Background(), dir)
		if err != nil || o.Session != "" || len(o.Project) != 64 || len(o.Worktree) != 64 || len(o.Branch) != 64 {
			t.Fatalf("invalid identity: %+v %v", o, err)
		}
		return [3]string{o.Project, o.Worktree, o.Branch}
	}
	git(root, "init", "-b", "main")
	unborn := resolve(root)
	git(root, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "synthetic")
	base := resolve(root)
	if base != unborn {
		t.Fatal("first commit changed branch identity")
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if resolve(sub) != base {
		t.Fatal("subdirectory split project")
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if resolve(link) != base {
		t.Fatal("symlink split project")
	}
	wt := filepath.Join(t.TempDir(), "worktree")
	git(root, "worktree", "add", "-b", "feature/synthetic", wt)
	other := resolve(wt)
	if base[0] != other[0] || base[1] == other[1] || base[2] == other[2] {
		t.Fatal("worktree isolation failed")
	}
	git(root, "checkout", "--detach")
	detached := resolve(root)
	if base[0] != detached[0] || base[1] != detached[1] || base[2] == detached[2] {
		t.Fatal("detached identity failed")
	}
	// Launcher metadata ignores inherited repository redirection.
	t.Setenv("GIT_DIR", filepath.Join(wt, ".git"))
	t.Setenv("GIT_WORK_TREE", wt)
	if resolve(root) != detached {
		t.Fatal("environment redirected identity")
	}
}

func TestNonGitAndInvalid(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	a, err := Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Resolve(ctx, t.TempDir())
	if err != nil || a.Project == b.Project || a.Worktree == b.Worktree {
		t.Fatal("non-git roots collided")
	}
	if _, err := Resolve(ctx, filepath.Join(root, "missing")); err != ErrIdentity {
		t.Fatal("missing directory accepted")
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("invalid synthetic git marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(ctx, root); err != ErrIdentity {
		t.Fatal("broken git silently treated as non-git")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Resolve(canceled, t.TempDir()); err != ErrIdentity {
		t.Fatal("canceled resolution succeeded")
	}
}
