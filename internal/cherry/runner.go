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

// ErrNoopCherryPick means the cherry-pick produced no net changes on the target
// branch (commit already present or merge resolved to empty diff).
var ErrNoopCherryPick = errors.New("cherry-pick produced no changes")

// DoCherryPick cherry-picks a single commit SHA onto target branch and pushes a new work branch.
func DoCherryPick(ctx context.Context, owner, repo, token, targetBranch, sha string, actor GitActor) (string, error) {
	return doCherryPick(ctx, owner, repo, token, targetBranch, sha, actor, 0)
}

// DoCherryPickWithMainline cherry-picks a merge commit with -m <mainline>.
func DoCherryPickWithMainline(ctx context.Context, owner, repo, token, targetBranch, sha string, mainline int, actor GitActor) (string, error) {
	return doCherryPick(ctx, owner, repo, token, targetBranch, sha, actor, mainline)
}

func doCherryPick(ctx context.Context, owner, repo, token, targetBranch, sha string, actor GitActor, mainline int) (string, error) {
	r, err := gitexec.NewRunner("", "GIT_ASKPASS=true")
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

	// Fetch target branch tip and ensure the commit object exists locally.
	if err := r.Fetch(ctx,
		"master:refs/remotes/origin/master",
		fmt.Sprintf("refs/heads/%s:refs/remotes/origin/%s", targetBranch, targetBranch),
		sha,
	); err != nil {
		return "", err
	}

	short := sha
	if len(short) > 7 {
		short = sha[:7]
	}
	safeTarget := strings.ReplaceAll(targetBranch, "/", "-")
	workBranch := fmt.Sprintf("autocherry/%s/%s", safeTarget, short)

	// Base the work branch on the target branch
	if err := r.CheckoutBranchFrom(ctx, workBranch, "origin/"+targetBranch); err != nil {
		return "", err
	}

	// Cherry-pick (handle merge/no-op cases)
	var cpErr error
	if mainline > 0 {
		slog.Debug("git.cherry_pick_mainline", "sha", sha, "mainline", mainline)
		cpErr = r.CherryPickWithMainline(ctx, mainline, sha)
	} else {
		cpErr = r.CherryPick(ctx, sha)
	}

	if cpErr != nil {
		// Git reports no-op cherry-picks as an "error" — detect and treat as no-op.
		if isNoopCherryPickErr(cpErr) {
			_ = r.CherryPickSkip(ctx) // clean state
			slog.Info("git.noop_cherry_pick", "sha", sha, "target", targetBranch)
			return "", ErrNoopCherryPick
		}
		// Real conflict/error
		if mainline > 0 {
			return "", fmt.Errorf("conflict cherry-picking %s to %s (mainline %d): %w", sha, targetBranch, mainline, cpErr)
		}
		return "", fmt.Errorf("conflict cherry-picking %s to %s: %w", sha, targetBranch, cpErr)
	}

	// Push the work branch so we can open a PR
	if err := r.Push(ctx, workBranch); err != nil {
		return "", err
	}
	return workBranch, nil
}

func isNoopCherryPickErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "previous cherry-pick is now empty") ||
		strings.Contains(s, "The previous cherry-pick is now empty") ||
		strings.Contains(s, "nothing to commit, working tree clean")
}
