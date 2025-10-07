package gitexec

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type Runner struct {
	WorkDir string
	Env     []string
}

func NewRunner(baseDir string, extraEnv ...string) (*Runner, error) {
	dir := baseDir
	if dir == "" {
		dir = os.TempDir()
	}
	td, err := os.MkdirTemp(dir, "cherry-*")
	if err != nil {
		return nil, err
	}
	return &Runner{WorkDir: td, Env: append(os.Environ(), extraEnv...)}, nil
}

var reToken = regexp.MustCompile(`x-access-token:[^@]+@`)

func (r *Runner) run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.WorkDir
	cmd.Env = r.Env

	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out

	// redact token in logged args
	safeArgs := make([]string, len(args))
	for i, a := range args {
		safeArgs[i] = reToken.ReplaceAllString(a, "x-access-token:***@")
	}

	slog.Debug("git.exec", "cwd", r.WorkDir, "cmd", name, "args", safeArgs)
	err := cmd.Run()
	if err != nil {
		s := out.String()
		slog.Error("git.fail", "cmd", name, "args", safeArgs, "err", err, "out", s)
		return fmt.Errorf("%s %s failed: %v\n%s", name, strings.Join(safeArgs, " "), err, s)
	}
	if s := strings.TrimSpace(out.String()); s != "" {
		slog.Debug("git.out", "cmd", name, "out", s)
	}
	return nil
}

func (r *Runner) Clean() { _ = os.RemoveAll(r.WorkDir) }

func (r *Runner) CloneWithToken(ctx context.Context, owner, repo, token string) error {
	url := fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", token, owner, repo)
	if err := r.run(ctx, "git", "init"); err != nil {
		return err
	}
	if err := r.run(ctx, "git", "remote", "add", "origin", url); err != nil {
		return err
	}
	_ = r.run(ctx, "git", "symbolic-ref", "HEAD", "refs/heads/master")
	return nil
}

func (r *Runner) ConfigUser(ctx context.Context, name, email string) error {
	if err := r.run(ctx, "git", "config", "user.name", name); err != nil {
		return err
	}
	return r.run(ctx, "git", "config", "user.email", email)
}

func (r *Runner) Fetch(ctx context.Context, refspec ...string) error {
	args := append([]string{"fetch", "--prune", "origin"}, refspec...)
	return r.run(ctx, "git", args...)
}

func (r *Runner) CheckoutBranchFrom(ctx context.Context, newBranch, fromRef string) error {
	// fromRef can be "origin/<branch>" or a full ref
	if !strings.HasPrefix(fromRef, "refs/") && !strings.HasPrefix(fromRef, "origin/") {
		fromRef = "refs/remotes/" + fromRef
	}
	return r.run(ctx, "git", "checkout", "-B", newBranch, fromRef)
}

func (r *Runner) CherryPick(ctx context.Context, sha string) error {
	return r.run(ctx, "git", "cherry-pick", "-x", sha)
}

func (r *Runner) CherryPickSkip(ctx context.Context) error {
	return r.run(ctx, "git", "cherry-pick", "--skip")
}

func (r *Runner) CherryPickWithMainline(ctx context.Context, mainline int, sha string) error {
	return r.run(ctx, "git", "cherry-pick", "-m", fmt.Sprint(mainline), "-x", sha)
}

func (r *Runner) AbortCherryPick(ctx context.Context) {
	_ = r.run(ctx, "git", "cherry-pick", "--abort")
}

func (r *Runner) Push(ctx context.Context, branch string) error {
	return r.run(ctx, "git", "push", "-u", "origin", branch)
}
