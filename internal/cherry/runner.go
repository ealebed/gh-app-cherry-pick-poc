package cherry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ealebed/gh-app-cherry-pick-poc/internal/gitexec"
)

type GitActor struct {
	Name  string
	Email string
}

// --- test seam: minimal interface our code needs ---
type gitRunner interface {
	Clean() // NOTE: no error return to match gitexec.Runner
	CloneWithToken(ctx context.Context, owner, repo, token string) error
	ConfigUser(ctx context.Context, name, email string) error
	Fetch(ctx context.Context, refs ...string) error
	CheckoutBranchFrom(ctx context.Context, newBranch, fromRef string) error
	CherryPick(ctx context.Context, sha string) error
	CherryPickWithMainline(ctx context.Context, mainline int, sha string) error
	Push(ctx context.Context, branch string) error
}

// injectable constructor (overridden in tests)
var newGitRunner = func(cwd string, env ...string) (gitRunner, error) {
	return gitexec.NewRunner(cwd, env...)
}

// ErrNoopCherryPick signals the commit is already present / empty diff
var ErrNoopCherryPick = errors.New("noop cherry-pick")

// isNoopCherryPickErr detects “empty” cherry-pick scenarios from git output.
func isNoopCherryPickErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "previous cherry-pick is now empty") ||
		strings.Contains(s, "nothing to commit") ||
		strings.Contains(s, "working tree clean")
}

// DoCherryPick cherry-picks a single non-merge commit onto target branch and pushes a new work branch.
func DoCherryPick(ctx context.Context, owner, repo, token, targetBranch, sha string, actor GitActor) (string, error) {
	return doCherryPick(ctx, owner, repo, token, targetBranch, sha, actor, 0)
}

// DoCherryPickWithMainline cherry-picks a merge commit with -m <mainline>.
func DoCherryPickWithMainline(ctx context.Context, owner, repo, token, targetBranch, sha string, mainline int, actor GitActor) (string, error) {
	return doCherryPick(ctx, owner, repo, token, targetBranch, sha, actor, mainline)
}

func doCherryPick(ctx context.Context, owner, repo, token, targetBranch, sha string, actor GitActor, mainline int) (string, error) {
	r, err := newGitRunner("", "GIT_ASKPASS=true")
	if err != nil {
		return "", err
	}
	defer r.Clean()

	if err := r.CloneWithToken(ctx, owner, repo, token); err != nil {
		return "", err
	}
	if err := r.ConfigUser(ctx, actor.Name, actor.Email); err != nil {
		return "", err
	}

	// Fetch target branch and the specific commit (and also master as a common case)
	if err := r.Fetch(ctx,
		"master:refs/remotes/origin/master",
		fmt.Sprintf("refs/heads/%s:refs/remotes/origin/%s", targetBranch, targetBranch),
		sha, // ensure the object exists locally
	); err != nil {
		return "", err
	}

	short := sha
	if len(short) > 7 {
		short = sha[:7]
	}
	safeTarget := strings.ReplaceAll(targetBranch, "/", "-")
	workBranch := fmt.Sprintf("autocherry/%s/%s", safeTarget, short)

	// Base new branch on the target branch
	if err := r.CheckoutBranchFrom(ctx, workBranch, "origin/"+targetBranch); err != nil {
		return "", err
	}

	// Cherry-pick
	if mainline > 0 {
		slog.Debug("git.cherry_pick_mainline", "sha", sha, "mainline", mainline)
		if err := r.CherryPickWithMainline(ctx, mainline, sha); err != nil {
			if isNoopCherryPickErr(err) {
				slog.Info("cherry.noop", "target", targetBranch, "sha", sha)
				return "", ErrNoopCherryPick
			}
			return "", fmt.Errorf("conflict cherry-picking %s to %s (mainline %d): %w", sha, targetBranch, mainline, err)
		}
	} else {
		if err := r.CherryPick(ctx, sha); err != nil {
			if isNoopCherryPickErr(err) {
				slog.Info("cherry.noop", "target", targetBranch, "sha", sha)
				return "", ErrNoopCherryPick
			}
			return "", fmt.Errorf("conflict cherry-picking %s to %s: %w", sha, targetBranch, err)
		}
	}

	// Push work branch
	if err := r.Push(ctx, workBranch); err != nil {
		return "", err
	}
	return workBranch, nil
}
