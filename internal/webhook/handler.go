package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	github "github.com/google/go-github/v75/github"

	"github.com/ealebed/gh-app-cherry-pick-poc/internal/cherry"
	"github.com/ealebed/gh-app-cherry-pick-poc/internal/githubapp"
)

// Server handles GitHub webhooks for the cherry-pick app.
type Server struct {
	AppID         int64
	PrivateKeyPEM []byte
	WebhookSecret []byte
	GitUserName   string
	GitUserEmail  string
}

func (s *Server) verifySig(r *http.Request, body []byte) bool {
	sig := r.Header.Get("X-Hub-Signature-256")
	if sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, s.WebhookSecret)
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(sig)), []byte(strings.ToLower(want)))
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	deliveryID := r.Header.Get("X-GitHub-Delivery")
	event := r.Header.Get("X-GitHub-Event")

	body, _ := io.ReadAll(r.Body)
	if !s.verifySig(r, body) {
		slog.Error("webhook.sig_mismatch", "delivery", deliveryID, "event", event)
		http.Error(w, "signature mismatch", http.StatusUnauthorized)
		return
	}

	slog.Debug("webhook.received", "delivery", deliveryID, "event", event)

	switch event {
	case "pull_request":
		var e github.PullRequestEvent
		if err := json.Unmarshal(body, &e); err != nil {
			slog.Error("webhook.bad_payload", "delivery", deliveryID, "err", err)
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		// Handle asynchronously; respond fast to GitHub.
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("webhook.panic", "delivery", deliveryID, "panic", r)
				}
			}()
			s.handlePREvent(context.Background(), deliveryID, &e)
		}()
		w.WriteHeader(http.StatusAccepted)

	default:
		slog.Debug("webhook.ignore_event", "delivery", deliveryID, "event", event)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handlePREvent(ctx context.Context, deliveryID string, e *github.PullRequestEvent) {
	if e.GetInstallation() == nil {
		slog.Warn("pr.no_installation", "delivery", deliveryID)
		return
	}
	instID := e.GetInstallation().GetID()
	repo := e.GetRepo()
	owner, name := repo.GetOwner().GetLogin(), repo.GetName()

	action := e.GetAction()
	merged := e.GetPullRequest().GetMerged()
	prNum := e.GetPullRequest().GetNumber()

	slog.Info("pr.event",
		"delivery", deliveryID,
		"action", action,
		"merged", merged,
		"repo", owner+"/"+name,
		"pr", prNum,
		"inst", instID,
	)

	// Compute targetsOverride for "labeled" on an already-merged PR:
	// process *only the newly added label* to avoid reprocessing previous targets.
	var targetsOverride []string
	if action == "labeled" && merged && e.Label != nil {
		targetsOverride = cherry.ParseTargetBranches([]*github.Label{e.Label})
	}

	// We act on merged PRs, and also on label-added events for PRs that are already merged.
	if (action == "closed" && merged) || (action == "labeled" && merged) {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		s.processMergedPR(cctx, deliveryID, instID, owner, name, prNum, targetsOverride)
	} else {
		slog.Debug("pr.skip", "delivery", deliveryID, "reason", "not_merged_or_not_labeled_after_merge")
	}
}

func (s *Server) processMergedPR(ctx context.Context, deliveryID string, installationID int64, owner, repo string, prNum int, targetsOverride []string) {
	cli, err := githubapp.NewClients(s.AppID, installationID, s.PrivateKeyPEM)
	if err != nil {
		slog.Error("gh.client_error", "delivery", deliveryID, "err", err)
		return
	}

	// Load PR details
	pr, _, err := cli.REST.PullRequests.Get(ctx, owner, repo, prNum)
	if err != nil {
		slog.Error("gh.get_pr_error", "delivery", deliveryID, "repo", owner+"/"+repo, "pr", prNum, "err", err)
		return
	}

	// Labels (log for visibility)
	lbls := []string{}
	for _, l := range pr.Labels {
		if l != nil && l.Name != nil {
			lbls = append(lbls, l.GetName())
		}
	}
	slog.Debug("pr.labels", "delivery", deliveryID, "pr", prNum, "labels", lbls)

	// Target selection
	var targets []string
	if len(targetsOverride) > 0 {
		targets = targetsOverride
	} else {
		targets = cherry.ParseTargetBranches(pr.Labels)
	}
	slog.Info("pr.targets", "delivery", deliveryID, "pr", prNum, "targets", targets)
	if len(targets) == 0 {
		slog.Info("pr.no_targets", "delivery", deliveryID, "pr", prNum)
		return
	}

	// Determine merged commit SHA
	mergeSHA := pr.GetMergeCommitSHA()
	if mergeSHA == "" {
		commits, _, err := cli.REST.PullRequests.ListCommits(ctx, owner, repo, prNum, &github.ListOptions{PerPage: 250})
		if err != nil || len(commits) == 0 {
			slog.Error("gh.list_commits_error", "delivery", deliveryID, "pr", prNum, "err", err)
			if _, _, err2 := cli.REST.Issues.CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf("⚠️ Could not determine merged commit SHA for PR #%d: %v", prNum, err)),
			}); err2 != nil {
				slog.Warn("gh.comment_error", "delivery", deliveryID, "stage", "sha_missing", "err", err2)
			}
			return
		}
		mergeSHA = commits[len(commits)-1].GetSHA()
	}
	slog.Info("pr.merge_sha", "delivery", deliveryID, "pr", prNum, "sha", mergeSHA)

	// Detect if the merged commit is itself a merge (parents > 1)
	rc, _, err := cli.REST.Repositories.GetCommit(ctx, owner, repo, mergeSHA, nil)
	if err != nil {
		slog.Warn("gh.get_commit_warn", "delivery", deliveryID, "sha", mergeSHA, "err", err)
	}
	isMerge := rc != nil && len(rc.Parents) > 1
	if isMerge {
		slog.Info("pr.merge_sha_is_merge_commit", "delivery", deliveryID, "sha", mergeSHA, "parents", len(rc.Parents))
	}

	// Installation token for raw git operations
	itr, err := ghinstallation.New(http.DefaultTransport, s.AppID, installationID, s.PrivateKeyPEM)
	if err != nil {
		slog.Error("gh.installation_transport_error", "delivery", deliveryID, "err", err)
		return
	}
	token, err := itr.Token(ctx)
	if err != nil || token == "" {
		slog.Error("gh.installation_token_error", "delivery", deliveryID, "err", err)
		return
	}

	// Common short SHA for branch naming
	short := mergeSHA
	if len(short) > 7 {
		short = mergeSHA[:7]
	}

	for _, target := range targets {
		// Ensure target branch exists (full ref)
		if _, _, err := cli.REST.Git.GetRef(ctx, owner, repo, "refs/heads/"+target); err != nil {
			slog.Warn("target.missing", "delivery", deliveryID, "target", target)
			if _, _, err2 := cli.REST.Issues.CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf("⚠️ Target branch `%s` not found; skipping auto cherry-pick.", target)),
			}); err2 != nil {
				slog.Warn("gh.comment_error", "delivery", deliveryID, "stage", "target_missing", "err", err2)
			}
			continue
		}

		// Idempotency: if our work branch already exists, try to find an open PR and skip
		safeTarget := strings.ReplaceAll(target, "/", "-")
		workBranch := fmt.Sprintf("autocherry/%s/%s", safeTarget, short)

		if _, _, err := cli.REST.Git.GetRef(ctx, owner, repo, "refs/heads/"+workBranch); err == nil {
			// Work branch exists; check if there's an open PR head=owner:workBranch base=target
			prs, _, lerr := cli.REST.PullRequests.List(ctx, owner, repo, &github.PullRequestListOptions{
				State: "open",
				Head:  fmt.Sprintf("%s:%s", owner, workBranch),
				Base:  target,
				ListOptions: github.ListOptions{
					PerPage: 1,
				},
			})
			if lerr == nil && len(prs) > 0 {
				url := prs[0].GetHTMLURL()
				slog.Info("cherry.already_open", "delivery", deliveryID, "target", target, "branch", workBranch, "url", url)
				_, _, _ = cli.REST.Issues.CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
					Body: github.Ptr(fmt.Sprintf("ℹ️ Auto cherry-pick to `%s` is already open: %s", target, url)),
				})
				continue
			}
			// Branch exists but no open PR — avoid overwriting; just inform and skip.
			slog.Info("cherry.branch_exists_no_pr", "delivery", deliveryID, "target", target, "branch", workBranch)
			_, _, _ = cli.REST.Issues.CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf("ℹ️ Work branch `%s` already exists for `%s`; skipping duplicate cherry-pick.", workBranch, target)),
			})
			continue
		}

		slog.Info("cherry.start", "delivery", deliveryID, "target", target, "sha", mergeSHA, "isMerge", isMerge)

		var (
			newWorkBranch string
			cpErr         error
		)
		if isMerge {
			// Use -m 1: parent 1 is the base branch of the original merge
			newWorkBranch, cpErr = cherry.DoCherryPickWithMainline(ctx, owner, repo, token, target, mergeSHA, 1, cherry.GitActor{
				Name:  s.GitUserName,
				Email: s.GitUserEmail,
			})
		} else {
			newWorkBranch, cpErr = cherry.DoCherryPick(ctx, owner, repo, token, target, mergeSHA, cherry.GitActor{
				Name:  s.GitUserName,
				Email: s.GitUserEmail,
			})
		}

		if cpErr != nil {
			// No-op cherry-pick: nothing to apply on this target -> comment and continue.
			if errors.Is(cpErr, cherry.ErrNoopCherryPick) {
				_, _, _ = cli.REST.Issues.CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
					Body: github.Ptr(fmt.Sprintf("ℹ️ Auto cherry-pick to `%s`: no changes needed on target (commit already present or empty diff). Skipping PR.", target)),
				})
				slog.Info("cherry.noop", "delivery", deliveryID, "target", target, "sha", mergeSHA)
				continue
			}

			// Real conflict/error: notify and continue to next target
			slog.Warn("cherry.conflict", "delivery", deliveryID, "target", target, "err", cpErr)
			if _, _, err2 := cli.REST.Issues.CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf(
					"⚠️ Auto cherry-pick to `%s` failed. Please create a patch branch from `%s` and cherry-pick `%s` manually.\n\nDetails: `%v`",
					target, target, mergeSHA, cpErr)),
			}); err2 != nil {
				slog.Warn("gh.comment_error", "delivery", deliveryID, "stage", "conflict", "err", err2)
			}
			continue
		}

		slog.Info("cherry.pushed", "delivery", deliveryID, "work_branch", newWorkBranch, "target", target)

		// Open PR into target
		title := fmt.Sprintf("Auto cherry-pick: PR #%d — %s", pr.GetNumber(), pr.GetTitle())
		body := fmt.Sprintf("Automated cherry-pick of PR #%d into `%s`.\n\nCommit: `%s`", pr.GetNumber(), target, mergeSHA)
		newPR, _, err := cli.REST.PullRequests.Create(ctx, owner, repo, &github.NewPullRequest{
			Title: github.Ptr(title),
			Head:  github.Ptr(newWorkBranch),
			Base:  github.Ptr(target),
			Body:  github.Ptr(body),
		})
		if err != nil {
			slog.Error("gh.create_pr_error", "delivery", deliveryID, "target", target, "err", err)
			continue
		}
		slog.Info("gh.pr_opened", "delivery", deliveryID, "url", newPR.GetHTMLURL(), "target", target)

		if _, _, err2 := cli.REST.Issues.CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
			Body: github.Ptr(fmt.Sprintf("✅ Auto cherry-pick opened: %s", newPR.GetHTMLURL())),
		}); err2 != nil {
			slog.Warn("gh.comment_error", "delivery", deliveryID, "stage", "announce", "err", err2)
		}
	}
}
