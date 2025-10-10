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

	// Optional injections for tests
	NewClients   func(appID, installationID int64, pem []byte) (*githubapp.Clients, error)
	GetToken     func(ctx context.Context, appID, installationID int64, pem []byte) (string, error)
	CherryRunner CherryPickRunner
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
	defer func() {
		if cerr := r.Body.Close(); cerr != nil {
			slog.Warn("http.body_close_error", "err", cerr)
		}
	}()

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

	// If the PR was already merged and a new label was added, only process that one label.
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

func (s *Server) cherryRunner() CherryPickRunner {
	if s.CherryRunner != nil {
		return s.CherryRunner
	}
	return realCherryRunner{actor: cherry.GitActor{
		Name:  s.GitUserName,
		Email: s.GitUserEmail,
	}}
}

// processMergedPR keeps the production path (creating real clients/token) then
// delegates to processMergedPRWith which is fully testable via injected fakes.
func (s *Server) processMergedPR(ctx context.Context, deliveryID string, installationID int64, owner, repo string, prNum int, targetsOverride []string) {
	// Build real clients (or injected)
	var (
		clients *githubapp.Clients
		err     error
	)
	if s.NewClients != nil {
		clients, err = s.NewClients(s.AppID, installationID, s.PrivateKeyPEM)
	} else {
		clients, err = githubapp.NewClients(s.AppID, installationID, s.PrivateKeyPEM)
	}
	if err != nil {
		slog.Error("gh.client_error", "delivery", deliveryID, "err", err)
		return
	}

	// Get an installation token (or injected)
	var token string
	if s.GetToken != nil {
		token, err = s.GetToken(ctx, s.AppID, installationID, s.PrivateKeyPEM)
	} else {
		itr, ierr := ghinstallation.New(http.DefaultTransport, s.AppID, installationID, s.PrivateKeyPEM)
		if ierr != nil {
			slog.Error("gh.installation_transport_error", "delivery", deliveryID, "err", ierr)
			return
		}
		token, err = itr.Token(ctx)
	}
	if err != nil || token == "" {
		slog.Error("gh.installation_token_error", "delivery", deliveryID, "err", err)
		return
	}

	gh := realGH{c: clients.REST}
	s.processMergedPRWith(ctx, deliveryID, gh, owner, repo, prNum, targetsOverride, token)
}

// processMergedPRWith contains the core logic and is exercised by unit tests via fakes.
func (s *Server) processMergedPRWith(
	ctx context.Context,
	deliveryID string,
	gh GH,
	owner, repo string,
	prNum int,
	targetsOverride []string,
	token string,
) {
	// Load PR
	pr, _, err := gh.PR().Get(ctx, owner, repo, prNum)
	if err != nil {
		slog.Error("gh.get_pr_error", "delivery", deliveryID, "repo", owner+"/"+repo, "pr", prNum, "err", err)
		return
	}

	// Select targets
	var targets []string
	if len(targetsOverride) > 0 {
		targets = targetsOverride
	} else {
		lbls := []string{}
		for _, l := range pr.Labels {
			if l != nil && l.Name != nil {
				lbls = append(lbls, l.GetName())
			}
		}
		slog.Debug("pr.labels", "delivery", deliveryID, "pr", prNum, "labels", lbls)
		targets = cherry.ParseTargetBranches(pr.Labels)
	}
	slog.Info("pr.targets", "delivery", deliveryID, "pr", prNum, "targets", targets)
	if len(targets) == 0 {
		return
	}

	// Determine merged commit SHA
	mergeSHA := pr.GetMergeCommitSHA()
	if mergeSHA == "" {
		commits, _, err := gh.PR().ListCommits(ctx, owner, repo, prNum, &github.ListOptions{PerPage: 250})
		if err != nil || len(commits) == 0 {
			_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf("⚠️ Could not determine merged commit SHA for PR #%d: %v", prNum, err)),
			})
			return
		}
		mergeSHA = commits[len(commits)-1].GetSHA()
	}
	slog.Info("pr.merge_sha", "delivery", deliveryID, "pr", prNum, "sha", mergeSHA)

	// Is the merged commit itself a merge?
	rc, _, err := gh.Repos().GetCommit(ctx, owner, repo, mergeSHA, nil)
	isMerge := (err == nil && rc != nil && len(rc.Parents) > 1)
	if isMerge {
		slog.Info("pr.merge_sha_is_merge_commit", "delivery", deliveryID, "sha", mergeSHA, "parents", len(rc.Parents))
	}

	// For idempotency and branch naming
	short := mergeSHA
	if len(short) > 7 {
		short = mergeSHA[:7]
	}

	for _, target := range targets {
		// Ensure target branch exists
		if _, _, err := gh.Git().GetRef(ctx, owner, repo, "refs/heads/"+target); err != nil {
			_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf("⚠️ Target branch `%s` not found; skipping auto cherry-pick.", target)),
			})
			continue
		}

		safeTarget := strings.ReplaceAll(target, "/", "-")
		workBranch := fmt.Sprintf("autocherry/%s/%s", safeTarget, short)

		// Idempotency: if our work branch already exists, check for an open PR and skip.
		if _, _, err := gh.Git().GetRef(ctx, owner, repo, "refs/heads/"+workBranch); err == nil {
			prs, _, _ := gh.PR().List(ctx, owner, repo, &github.PullRequestListOptions{
				State:       "open",
				Head:        fmt.Sprintf("%s:%s", owner, workBranch),
				Base:        target,
				ListOptions: github.ListOptions{PerPage: 1},
			})
			if len(prs) > 0 {
				_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
					Body: github.Ptr(fmt.Sprintf("ℹ️ Auto cherry-pick to `%s` is already open: %s", target, prs[0].GetHTMLURL())),
				})
				continue
			}
			_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf("ℹ️ Work branch `%s` already exists for `%s`; skipping duplicate cherry-pick.", workBranch, target)),
			})
			continue
		}

		slog.Info("cherry.start", "delivery", deliveryID, "target", target, "sha", mergeSHA, "isMerge", isMerge)

		// Run cherry-pick via injectable runner
		workBranchOut, cpErr := s.cherryRunner().Pick(ctx, owner, repo, token, target, mergeSHA, isMerge)
		if cpErr != nil {
			// No-op cherry-pick: nothing to apply on this target
			if errors.Is(cpErr, cherry.ErrNoopCherryPick) {
				_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
					Body: github.Ptr(fmt.Sprintf("ℹ️ Auto cherry-pick to `%s`: no changes needed on target (commit already present or empty diff). Skipping PR.", target)),
				})
				slog.Info("cherry.noop", "delivery", deliveryID, "target", target, "sha", mergeSHA)
				continue
			}

			// Real conflict/error
			slog.Warn("cherry.conflict", "delivery", deliveryID, "target", target, "err", cpErr)
			_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf(
					"⚠️ Auto cherry-pick to `%s` failed. Please create a patch branch from `%s` and cherry-pick `%s` manually.\n\nDetails: `%v`",
					target, target, mergeSHA, cpErr)),
			})
			continue
		}

		slog.Info("cherry.pushed", "delivery", deliveryID, "work_branch", workBranchOut, "target", target)

		// Open PR into target
		title := fmt.Sprintf("Auto cherry-pick: PR #%d — %s", pr.GetNumber(), pr.GetTitle())
		body := fmt.Sprintf("Automated cherry-pick of PR #%d into `%s`.\n\nCommit: `%s`", pr.GetNumber(), target, mergeSHA)
		newPR, _, err := gh.PR().Create(ctx, owner, repo, &github.NewPullRequest{
			Title: github.Ptr(title),
			Head:  github.Ptr(workBranchOut),
			Base:  github.Ptr(target),
			Body:  github.Ptr(body),
		})
		if err != nil {
			slog.Error("gh.create_pr_error", "delivery", deliveryID, "target", target, "err", err)
			_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf("⚠️ Auto cherry-pick to `%s`: failed to open PR: %v", target, err)),
			})
			continue
		}
		slog.Info("gh.pr_opened", "delivery", deliveryID, "url", newPR.GetHTMLURL(), "target", target)

		_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
			Body: github.Ptr(fmt.Sprintf("✅ Auto cherry-pick to `%s` opened: %s", target, newPR.GetHTMLURL())),
		})
	}
}

// ---- Cherry-pick runner seam ----

type CherryPickRunner interface {
	Pick(ctx context.Context, owner, repo, token, target, sha string, isMerge bool) (workBranch string, err error)
}

type realCherryRunner struct {
	actor cherry.GitActor
}

func (r realCherryRunner) Pick(ctx context.Context, owner, repo, token, target, sha string, isMerge bool) (string, error) {
	if isMerge {
		return cherry.DoCherryPickWithMainline(ctx, owner, repo, token, target, sha, 1, r.actor)
	}
	return cherry.DoCherryPick(ctx, owner, repo, token, target, sha, r.actor)
}
