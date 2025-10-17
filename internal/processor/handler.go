package processor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	github "github.com/google/go-github/v75/github"

	"github.com/ealebed/gh-app-cherry-pick-poc/internal/cherry"
	"github.com/ealebed/gh-app-cherry-pick-poc/internal/githubapp"
	qenv "github.com/ealebed/gh-app-cherry-pick-poc/internal/queue"
)

// Processor handles GitHub events from queue envelopes.
type Processor struct {
	AppID         int64
	PrivateKeyPEM []byte
	WebhookSecret []byte
	GitUserName   string
	GitUserEmail  string

	// Configurable timeout for a single merged-PR processing (clone/fetch/cherry/push).
	CherryTimeout time.Duration

	// Test seams
	NewClients   func(appID, installationID int64, pem []byte) (*githubapp.Clients, error)
	GetToken     func(ctx context.Context, appID, installationID int64, pem []byte) (string, error)
	CherryRunner CherryPickRunner
}

// sanitizeForLog removes control characters that could break log lines and
// caps the length to prevent log flooding. Keep tabs for readability.
func sanitizeForLog(s string) string {
	if s == "" {
		return s
	}
	// Strip CR, LF and other control chars except TAB.
	sb := strings.Builder{}
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r':
			// drop
		case r < 0x20 && r != '\t':
			// other control chars -> drop
		case r == '\u2028' || r == '\u2029':
			// line/paragraph separators -> drop
		default:
			sb.WriteRune(r)
		}
	}
	out := sb.String()
	const max = 512
	if len(out) > max {
		out = out[:max] + "…"
	}
	return out
}

// safeErr renders an error as a sanitized string for logging.
func safeErr(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeForLog(err.Error())
}

// verifySig validates X-Hub-Signature-256 for a payload.
func (p *Processor) verifySig(headers map[string]string, body []byte) bool {
	sig := headers["X-Hub-Signature-256"]
	if sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, p.WebhookSecret)
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(sig)), []byte(strings.ToLower(want)))
}

// HandleEvent satisfies ingest/sqs.Handler. It re-wraps inputs into our envelope
// and forwards to HandleFromEnvelope with a computed signature.
func (p *Processor) HandleEvent(ctx context.Context, event, delivery string, body []byte) (int, error) {
	mac := hmac.New(sha256.New, p.WebhookSecret)
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return p.HandleFromEnvelope(ctx, qenv.Envelope{
		Headers: map[string]string{
			"X-GitHub-Event":      event,
			"X-GitHub-Delivery":   delivery,
			"X-Hub-Signature-256": sig,
		},
		Body: body,
	})
}

// HandleFromEnvelope processes one queue envelope (from internal/queue.Parser).
func (p *Processor) HandleFromEnvelope(ctx context.Context, env qenv.Envelope) (int, error) {
	deliveryID := env.Headers["X-GitHub-Delivery"]
	event := env.Headers["X-GitHub-Event"]
	body := []byte(env.Body)

	if !p.verifySig(env.Headers, body) {
		slog.Error("webhook.sig_mismatch", "delivery", sanitizeForLog(deliveryID), "event", event)
		return http.StatusUnauthorized, fmt.Errorf("signature mismatch")
	}
	slog.Debug("webhook.received", "delivery", sanitizeForLog(deliveryID), "event", event)

	switch event {
	case "pull_request":
		var e github.PullRequestEvent
		if err := json.Unmarshal(body, &e); err != nil {
			slog.Error("webhook.bad_payload", "delivery", sanitizeForLog(deliveryID), "err", safeErr(err))
			return http.StatusBadRequest, fmt.Errorf("bad payload: %w", err)
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("webhook.panic", "delivery", sanitizeForLog(deliveryID), "panic", r)
				}
			}()
			p.handlePREvent(context.Background(), deliveryID, &e)
		}()
		return http.StatusAccepted, nil

	case "create":
		var e github.CreateEvent
		if err := json.Unmarshal(body, &e); err != nil {
			slog.Error("webhook.bad_payload", "delivery", sanitizeForLog(deliveryID), "err", safeErr(err))
			return http.StatusBadRequest, fmt.Errorf("bad payload: %w", err)
		}
		go func() {
			ctx2, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			p.handleCreateEvent(ctx2, deliveryID, &e)
		}()
		return http.StatusAccepted, nil

	case "label":
		// Repo-level label delete: remove that label from open PRs
		// and ALSO clean up autocherry artifacts for that target.
		var e github.LabelEvent
		if err := json.Unmarshal(body, &e); err != nil {
			slog.Error("webhook.bad_payload", "delivery", sanitizeForLog(deliveryID), "err", safeErr(err))
			return http.StatusBadRequest, fmt.Errorf("bad payload: %w", err)
		}
		go func() {
			ctx2, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if e.GetAction() != "deleted" || e.GetRepo() == nil || e.GetLabel() == nil {
				return
			}
			repo := e.GetRepo()
			owner, name := repo.GetOwner().GetLogin(), repo.GetName()
			labelName := e.GetLabel().GetName()
			if !strings.HasPrefix(labelName, "cherry-pick to ") {
				return
			}
			inst := e.GetInstallation()
			if inst == nil {
				slog.Warn("label.no_installation", "delivery", sanitizeForLog(deliveryID), "repo", owner+"/"+name)
				return
			}
			clients, err := p.buildClients(inst.GetID())
			if err != nil {
				slog.Error("gh.client_error", "delivery", sanitizeForLog(deliveryID), "err", safeErr(err))
				return
			}
			gh := realGH{c: clients.REST}

			// 1) Detach from OPEN PRs (in case UI still shows it lingering).
			_ = p.removeLabelFromOpenPRs(ctx2, gh, owner, name, labelName)

			// 2) Fallback cleanup: label is already deleted, so we can't query history by label.
			target := strings.TrimSpace(strings.TrimPrefix(labelName, "cherry-pick to "))
			if target == "" {
				return
			}
			if err := p.cleanupOpenAutoCherryForTarget(ctx2, gh, owner, name, target); err != nil {
				slog.Error("labels.cleanup_fallback_error", "delivery", sanitizeForLog(deliveryID), "label", labelName, "err", safeErr(err))
			} else {
				slog.Info("labels.cleanup_fallback_done", "delivery", sanitizeForLog(deliveryID), "label", labelName)
			}
		}()
		return http.StatusAccepted, nil

	default:
		slog.Debug("webhook.ignore_event", "delivery", sanitizeForLog(deliveryID), "event", event)
		return http.StatusNoContent, nil
	}
}

func (p *Processor) handlePREvent(ctx context.Context, deliveryID string, e *github.PullRequestEvent) {
	if e.GetInstallation() == nil {
		slog.Warn("pr.no_installation", "delivery", sanitizeForLog(deliveryID))
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

	// If labeled after merge, process only that label.
	var targetsOverride []string
	if action == "labeled" && merged && e.Label != nil {
		targetsOverride = cherry.ParseTargetBranches([]*github.Label{e.Label})
	}

	switch {
	case (action == "closed" && merged) || (action == "labeled" && merged):
		// Use configurable timeout; default to 2m if not set.
		to := p.CherryTimeout
		if to <= 0 {
			to = 2 * time.Minute
		}
		cctx, cancel := context.WithTimeout(ctx, to)
		defer cancel()
		p.processMergedPR(cctx, deliveryID, instID, owner, name, prNum, targetsOverride)

	case action == "unlabeled" && merged && e.Label != nil:
		targets := cherry.ParseTargetBranches([]*github.Label{e.Label})
		if len(targets) == 0 {
			return
		}
		clients, err := p.buildClients(instID)
		if err != nil {
			slog.Error("gh.client_error", "delivery", sanitizeForLog(deliveryID), "err", safeErr(err))
			return
		}
		gh := realGH{c: clients.REST}

		// Determine merge SHA (fallback to last commit).
		pr, _, err := gh.PR().Get(ctx, owner, name, prNum)
		if err != nil {
			return
		}
		mergeSHA := pr.GetMergeCommitSHA()
		if mergeSHA == "" {
			commits, _, _ := gh.PR().ListCommits(ctx, owner, name, prNum, &github.ListOptions{PerPage: 250})
			if len(commits) > 0 {
				mergeSHA = commits[len(commits)-1].GetSHA()
			}
		}
		if mergeSHA == "" {
			return
		}
		short := mergeSHA
		if len(short) > 7 {
			short = mergeSHA[:7]
		}

		for _, target := range targets {
			safeTarget := strings.ReplaceAll(target, "/", "-")
			workBranch := fmt.Sprintf("autocherry/%s/%s", safeTarget, short)
			if err := p.processUnlabeled(ctx, gh, owner, name, prNum, target, workBranch); err != nil {
				slog.Error("unlabeled.cleanup_error", "delivery", sanitizeForLog(deliveryID), "target", target, "err", safeErr(err))
			} else {
				_, _, _ = gh.Issues().CreateComment(ctx, owner, name, prNum, &github.IssueComment{
					Body: github.Ptr(fmt.Sprintf("ℹ️ Removed label for `%s`: closed any open auto-cherry-pick PR and deleted work branch `%s`.", target, workBranch)),
				})
			}
		}
	default:
		slog.Debug("pr.skip", "delivery", sanitizeForLog(deliveryID), "reason", "not_merged_or_not_labeled_after_merge")
	}
}

func (p *Processor) cherryRunner() CherryPickRunner {
	if p.CherryRunner != nil {
		return p.CherryRunner
	}
	return realCherryRunner{actor: cherry.GitActor{
		Name:  p.GitUserName,
		Email: p.GitUserEmail,
	}}
}

func (p *Processor) processMergedPR(ctx context.Context, deliveryID string, installationID int64, owner, repo string, prNum int, targetsOverride []string) {
	clients, err := p.buildClients(installationID)
	if err != nil {
		slog.Error("gh.client_error", "delivery", sanitizeForLog(deliveryID), "err", safeErr(err))
		return
	}

	// Installation token for git push.
	var token string
	if p.GetToken != nil {
		token, err = p.GetToken(ctx, p.AppID, installationID, p.PrivateKeyPEM)
	} else {
		itr, ierr := ghinstallation.New(http.DefaultTransport, p.AppID, installationID, p.PrivateKeyPEM)
		if ierr != nil {
			slog.Error("gh.installation_transport_error", "delivery", sanitizeForLog(deliveryID), "err", safeErr(ierr))
			return
		}
		token, err = itr.Token(ctx)
	}
	if err != nil || token == "" {
		slog.Error("gh.installation_token_error", "delivery", sanitizeForLog(deliveryID), "err", safeErr(err))
		return
	}

	gh := realGH{c: clients.REST}
	p.processMergedPRWith(ctx, deliveryID, gh, owner, repo, prNum, targetsOverride, token)
}

func (p *Processor) buildClients(installationID int64) (*githubapp.Clients, error) {
	if p.NewClients != nil {
		return p.NewClients(p.AppID, installationID, p.PrivateKeyPEM)
	}
	return githubapp.NewClients(p.AppID, installationID, p.PrivateKeyPEM)
}

func (p *Processor) processMergedPRWith(
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
		slog.Error("gh.get_pr_error", "delivery", sanitizeForLog(deliveryID), "repo", owner+"/"+repo, "pr", prNum, "err", safeErr(err))
		return
	}

	// Targets: override or parse labels.
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
		slog.Debug("pr.labels", "delivery", sanitizeForLog(deliveryID), "pr", prNum, "labels", lbls)
		targets = cherry.ParseTargetBranches(pr.Labels)
	}
	slog.Info("pr.targets", "delivery", sanitizeForLog(deliveryID), "pr", prNum, "targets", targets)
	if len(targets) == 0 {
		return
	}

	// Determine merged commit SHA.
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
	slog.Info("pr.merge_sha", "delivery", sanitizeForLog(deliveryID), "pr", prNum, "sha", mergeSHA)

	// Is the merged commit a merge?
	rc, _, err := gh.Repos().GetCommit(ctx, owner, repo, mergeSHA, nil)
	isMerge := (err == nil && rc != nil && len(rc.Parents) > 1)
	if isMerge {
		slog.Info("pr.merge_sha_is_merge_commit", "delivery", sanitizeForLog(deliveryID), "sha", mergeSHA, "parents", len(rc.Parents))
	}

	// Short SHA for branch name suffix.
	short := mergeSHA
	if len(short) > 7 {
		short = mergeSHA[:7]
	}

	for _, target := range targets {
		// Ensure target branch exists.
		if _, _, err := gh.Git().GetRef(ctx, owner, repo, "refs/heads/"+target); err != nil {
			_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf("⚠️ Target branch `%s` not found; skipping auto cherry-pick.", target)),
			})
			continue
		}

		safeTarget := strings.ReplaceAll(target, "/", "-")
		workBranch := fmt.Sprintf("autocherry/%s/%s", safeTarget, short)

		// Idempotency: work branch already exists?
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

		slog.Info("cherry.start", "delivery", sanitizeForLog(deliveryID), "target", target, "sha", mergeSHA, "isMerge", isMerge)

		// Run cherry-pick via injected runner.
		workBranchOut, cpErr := p.cherryRunner().Pick(ctx, owner, repo, token, target, mergeSHA, isMerge)
		if cpErr != nil {
			if errors.Is(cpErr, cherry.ErrNoopCherryPick) {
				_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
					Body: github.Ptr(fmt.Sprintf("ℹ️ Auto cherry-pick to `%s`: no changes needed on target (commit already present or empty diff). Skipping PR.", target)),
				})
				slog.Info("cherry.noop", "delivery", sanitizeForLog(deliveryID), "target", target, "sha", mergeSHA)
				continue
			}
			slog.Warn("cherry.conflict", "delivery", sanitizeForLog(deliveryID), "target", target, "err", safeErr(cpErr))
			_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf(
					"⚠️ Auto cherry-pick to `%s` failed. Please create a patch branch from `%s` and cherry-pick `%s` manually.\n\nDetails: `%v`",
					target, target, mergeSHA, cpErr)),
			})
			continue
		}

		slog.Info("cherry.pushed", "delivery", sanitizeForLog(deliveryID), "work_branch", workBranchOut, "target", target)

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
			slog.Error("gh.create_pr_error", "delivery", sanitizeForLog(deliveryID), "target", target, "err", safeErr(err))
			_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
				Body: github.Ptr(fmt.Sprintf("⚠️ Auto cherry-pick to `%s`: failed to open PR: %v", target, err)),
			})
			continue
		}
		slog.Info("gh.pr_opened", "delivery", sanitizeForLog(deliveryID), "url", newPR.GetHTMLURL(), "target", target)

		_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
			Body: github.Ptr(fmt.Sprintf("✅ Auto cherry-pick to `%s` opened: %s", target, newPR.GetHTMLURL())),
		})
	}
}

// Branch create: ensure label + enforce retention.
func (p *Processor) handleCreateEvent(ctx context.Context, deliveryID string, e *github.CreateEvent) {
	if e.GetRefType() != "branch" || e.GetRepo() == nil {
		return
	}
	ref := e.GetRef()
	repo := e.GetRepo()
	owner, name := repo.GetOwner().GetLogin(), repo.GetName()

	re := regexp.MustCompile(`^([a-z0-9-]+-release)/(\d{4})$`)
	m := re.FindStringSubmatch(ref)
	if len(m) != 3 {
		slog.Debug("create.ignore_branch", "delivery", sanitizeForLog(deliveryID), "ref", ref)
		return
	}

	inst := e.GetInstallation()
	if inst == nil {
		slog.Warn("create.no_installation", "delivery", sanitizeForLog(deliveryID), "repo", owner+"/"+name)
		return
	}
	clients, err := p.buildClients(inst.GetID())
	if err != nil {
		slog.Error("gh.client_error", "delivery", sanitizeForLog(deliveryID), "err", safeErr(err))
		return
	}
	gh := realGH{c: clients.REST}

	label := "cherry-pick to " + ref
	if err := p.ensureLabel(ctx, gh, owner, name, label); err != nil {
		slog.Error("labels.ensure_error", "delivery", sanitizeForLog(deliveryID), "label", label, "err", safeErr(err))
	} else {
		slog.Info("labels.created_or_exists", "delivery", sanitizeForLog(deliveryID), "label", label)
	}

	// Retain only latest 5 labels per family, with pre-deletion cleanup.
	if err := p.enforceLabelRetention(ctx, gh, owner, name, 5); err != nil {
		slog.Error("labels.retention_error", "delivery", sanitizeForLog(deliveryID), "err", safeErr(err))
	}
}

func (p *Processor) ensureLabel(ctx context.Context, gh GH, owner, repo, name string) error {
	labels, _, err := gh.Issues().ListLabels(ctx, owner, repo, &github.ListOptions{PerPage: 100})
	if err != nil {
		return fmt.Errorf("list repo labels: %w", err)
	}
	for _, l := range labels {
		if l != nil && l.Name != nil && l.GetName() == name {
			return nil
		}
	}
	_, _, err = gh.Issues().CreateLabel(ctx, owner, repo, &github.Label{
		Name:  github.Ptr(name),
		Color: github.Ptr("ededed"),
	})
	return err
}

func (p *Processor) enforceLabelRetention(ctx context.Context, gh GH, owner, repo string, keep int) error {
	if keep <= 0 {
		return nil
	}
	labels, _, err := gh.Issues().ListLabels(ctx, owner, repo, &github.ListOptions{PerPage: 200})
	if err != nil {
		return err
	}

	re := regexp.MustCompile(`^cherry-pick to ([a-z0-9-]+-release)/(\d+)$`)
	type item struct {
		full string
		fam  string
		n    int
	}
	buckets := map[string][]item{}

	for _, l := range labels {
		if l == nil || l.Name == nil {
			continue
		}
		name := l.GetName()
		m := re.FindStringSubmatch(name)
		if len(m) != 3 {
			continue
		}
		fam := m[1]
		num := parseInt(m[2])
		if num < 0 {
			continue
		}
		buckets[fam] = append(buckets[fam], item{full: name, fam: fam, n: num})
	}

	for fam, items := range buckets {
		sort.Slice(items, func(i, j int) bool { return items[i].n < items[j].n })
		if len(items) <= keep {
			continue
		}
		toDelete := items[0 : len(items)-keep]
		for _, it := range toDelete {
			// Pre-deletion cleanup: for merged PRs that used this label,
			// close any auto-cherry PRs & delete work branches.
			if err := p.cleanupForLabel(ctx, gh, owner, repo, it.full); err != nil {
				slog.Warn("labels.pre_delete_cleanup_error", "label", it.full, "err", err)
			}
			_, _ = gh.Issues().DeleteLabel(ctx, owner, repo, it.full)
		}
		slog.Debug("labels.retained", "family", fam, "kept", keep, "deleted", len(toDelete))
	}
	return nil
}

// For labels that still exist (retention path): find PRs by label (state=all).
// For merged PRs, compute work branch and call processUnlabeled.
func (p *Processor) cleanupForLabel(ctx context.Context, gh GH, owner, repo, labelName string) error {
	const prefix = "cherry-pick to "
	if !strings.HasPrefix(labelName, prefix) {
		return nil
	}
	target := strings.TrimSpace(strings.TrimPrefix(labelName, prefix))
	if target == "" {
		return nil
	}

	issues, _, err := gh.Issues().ListByRepo(ctx, owner, repo, &github.IssueListByRepoOptions{
		State:       "all",
		Labels:      []string{labelName},
		ListOptions: github.ListOptions{PerPage: 100},
	})
	if err != nil {
		return fmt.Errorf("list issues by label %q: %w", labelName, err)
	}

	for _, is := range issues {
		if is == nil || is.Number == nil || is.PullRequestLinks == nil {
			continue
		}
		prNum := is.GetNumber()
		pr, _, err := gh.PR().Get(ctx, owner, repo, prNum)
		if err != nil || !pr.GetMerged() {
			continue
		}

		mergeSHA := pr.GetMergeCommitSHA()
		if mergeSHA == "" {
			commits, _, lerr := gh.PR().ListCommits(ctx, owner, repo, prNum, &github.ListOptions{PerPage: 250})
			if lerr != nil || len(commits) == 0 {
				continue
			}
			mergeSHA = commits[len(commits)-1].GetSHA()
		}
		if mergeSHA == "" {
			continue
		}
		short := mergeSHA
		if len(short) > 7 {
			short = mergeSHA[:7]
		}
		safeTarget := strings.ReplaceAll(target, "/", "-")
		workBranch := fmt.Sprintf("autocherry/%s/%s", safeTarget, short)

		if err := p.processUnlabeled(ctx, gh, owner, repo, prNum, target, workBranch); err != nil {
			slog.Warn("labels.pre_delete_cleanup_unlabeled_error", "pr", prNum, "target", target, "err", err)
			continue
		}
		_, _, _ = gh.Issues().CreateComment(ctx, owner, repo, prNum, &github.IssueComment{
			Body: github.Ptr(fmt.Sprintf("ℹ️ Repo label `%s` is being removed; cleaned up auto cherry-pick for `%s` (closed PR and deleted `%s`).", labelName, target, workBranch)),
		})
	}
	return nil
}

// Fallback for label *already deleted* (UI): close any open autocherry PRs for target
// by scanning open PRs with base=target and head branch prefix "autocherry/<safeTarget>/".
func (p *Processor) cleanupOpenAutoCherryForTarget(ctx context.Context, gh GH, owner, repo, target string) error {
	if target == "" {
		return nil
	}
	safeTarget := strings.ReplaceAll(target, "/", "-")
	prefix := "autocherry/" + safeTarget + "/"

	prs, _, err := gh.PR().List(ctx, owner, repo, &github.PullRequestListOptions{
		State:       "open",
		Base:        target,
		ListOptions: github.ListOptions{PerPage: 100},
	})
	if err != nil {
		return err
	}

	for _, pr := range prs {
		if pr == nil || pr.Number == nil || pr.Head == nil || pr.Head.Ref == nil {
			continue
		}
		headRef := pr.Head.GetRef()
		if !strings.HasPrefix(headRef, prefix) {
			continue
		}
		// Close PR
		_, _, _ = gh.PR().Edit(ctx, owner, repo, pr.GetNumber(), &github.PullRequest{
			State: github.Ptr("closed"),
		})
		// Delete branch (best-effort)
		_, _ = gh.Git().DeleteRef(ctx, owner, repo, "refs/heads/"+headRef)
	}
	return nil
}

func (p *Processor) removeLabelFromOpenPRs(ctx context.Context, gh GH, owner, repo, label string) error {
	issues, _, err := gh.Issues().ListByRepo(ctx, owner, repo, &github.IssueListByRepoOptions{
		State:       "open",
		Labels:      []string{label},
		ListOptions: github.ListOptions{PerPage: 100},
	})
	if err != nil {
		return err
	}
	for _, is := range issues {
		if is == nil || is.Number == nil {
			continue
		}
		_, _ = gh.Issues().RemoveLabelForIssue(ctx, owner, repo, is.GetNumber(), label)
	}
	return nil
}

func (p *Processor) processUnlabeled(ctx context.Context, gh GH, owner, repo string, mainPRNumber int, target, workBranch string) error {
	prs, _, err := gh.PR().List(ctx, owner, repo, &github.PullRequestListOptions{
		State:       "open",
		Base:        target,
		Head:        owner + ":" + workBranch, // exact match
		ListOptions: github.ListOptions{PerPage: 50},
	})
	if err != nil {
		return err
	}
	for _, pr := range prs {
		if pr == nil || pr.Number == nil {
			continue
		}
		_, _, _ = gh.PR().Edit(ctx, owner, repo, pr.GetNumber(), &github.PullRequest{
			State: github.Ptr("closed"),
		})
	}
	_, derr := gh.Git().DeleteRef(ctx, owner, repo, "refs/heads/"+workBranch)
	if derr != nil && !isNotFound(derr) {
		return derr
	}
	return nil
}

// parseInt best-effort atoi; returns -1 on error.
func parseInt(s string) int {
	n := 0
	if s == "" {
		return -1
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func isNotFound(err error) bool {
	var e *github.ErrorResponse
	return errors.As(err, &e) && e.Response != nil && e.Response.StatusCode == http.StatusNotFound
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
