package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	github "github.com/google/go-github/v75/github"

	"github.com/ealebed/gh-app-cherry-pick-poc/internal/cherry"
)

//
// ---------- Helpers ----------
//

func signBody(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// mergedPR builds a minimal merged PR object with labels.
func mergedPR(number int, title, mergeSHA string, labels ...string) *github.PullRequest {
	var gl []*github.Label
	for _, l := range labels {
		gl = append(gl, &github.Label{Name: github.Ptr(l)})
	}
	return &github.PullRequest{
		Number:         github.Ptr(number),
		Title:          github.Ptr(title),
		Merged:         github.Ptr(true),
		MergeCommitSHA: github.Ptr(mergeSHA),
		Labels:         gl,
	}
}

// repoCommitWithParents returns a RepositoryCommit whose Parents slice has n entries.
func repoCommitWithParents(n int) *github.RepositoryCommit {
	ps := make([]*github.Commit, n)
	for i := range ps {
		ps[i] = &github.Commit{}
	}
	return &github.RepositoryCommit{Parents: ps}
}

//
// ---------- Unified fakes for GH + cherry runner ----------
//

type fakePRFull struct {
	// inputs/fixtures
	prGet      *github.PullRequest
	commits    []*github.RepositoryCommit
	list       []*github.PullRequest
	createErr  error
	listErr    error
	commitsErr error

	// outputs/observations
	createdPR *github.PullRequest
	edited    []*github.PullRequest
}

func (f *fakePRFull) Get(ctx context.Context, owner, repo string, number int) (*github.PullRequest, *github.Response, error) {
	return f.prGet, nil, nil
}
func (f *fakePRFull) ListCommits(ctx context.Context, owner, repo string, number int, opt *github.ListOptions) ([]*github.RepositoryCommit, *github.Response, error) {
	if f.commits != nil {
		return f.commits, nil, f.commitsErr
	}
	// default: return one with the PR's merge SHA (may be empty)
	return []*github.RepositoryCommit{{SHA: f.prGet.MergeCommitSHA}}, nil, f.commitsErr
}
func (f *fakePRFull) List(ctx context.Context, owner, repo string, opts *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error) {
	return f.list, nil, f.listErr
}
func (f *fakePRFull) Create(ctx context.Context, owner, repo string, pr *github.NewPullRequest) (*github.PullRequest, *github.Response, error) {
	if f.createErr != nil {
		return nil, nil, f.createErr
	}
	f.createdPR = &github.PullRequest{
		HTMLURL: github.Ptr("https://example.com/newpr"),
	}
	return f.createdPR, nil, nil
}
func (f *fakePRFull) Edit(ctx context.Context, owner, repo string, number int, pr *github.PullRequest) (*github.PullRequest, *github.Response, error) {
	cp := *pr
	f.edited = append(f.edited, &cp)
	return &cp, &github.Response{Response: &http.Response{StatusCode: 200}}, nil
}

type fakeIssuesFull struct {
	// fixtures
	listByRepo []*github.Issue

	// repo-labels store & errors
	labels    []*github.Label
	createErr error
	deleteErr error
	listErr   error

	// observations
	comments []*github.IssueComment
	removed  []struct {
		Num  int
		Name string
	}
	created []*github.Label
	deleted []string
}

func (f *fakeIssuesFull) CreateComment(ctx context.Context, owner, repo string, number int, comment *github.IssueComment) (*github.IssueComment, *github.Response, error) {
	f.comments = append(f.comments, comment)
	return comment, nil, nil
}
func (f *fakeIssuesFull) ListByRepo(ctx context.Context, owner, repo string, opts *github.IssueListByRepoOptions) ([]*github.Issue, *github.Response, error) {
	var out []*github.Issue
	for _, is := range f.listByRepo {
		if is == nil {
			continue
		}

		// Filter by state if provided (e.g., "open")
		if opts != nil && opts.State != "" {
			if !strings.EqualFold(is.GetState(), opts.State) {
				continue
			}
		}

		// Filter by labels (support multiple)
		if opts != nil && len(opts.Labels) > 0 {
			labelMatch := false
			for _, need := range opts.Labels {
				for _, l := range is.Labels {
					if l.GetName() == need {
						labelMatch = true
						break
					}
				}
				if labelMatch {
					break
				}
			}
			if !labelMatch {
				continue
			}
		}

		out = append(out, is)
	}
	return out, &github.Response{Response: &http.Response{StatusCode: 200}}, nil
}
func (f *fakeIssuesFull) RemoveLabelForIssue(ctx context.Context, owner, repo string, number int, name string) (*github.Response, error) {
	f.removed = append(f.removed, struct {
		Num  int
		Name string
	}{Num: number, Name: name})
	return &github.Response{Response: &http.Response{StatusCode: 200}}, nil
}

// Repo label management (lives under IssuesService in go-github)
func (f *fakeIssuesFull) ListLabels(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Label, *github.Response, error) {
	if f.listErr != nil {
		return nil, nil, f.listErr
	}
	return f.labels, &github.Response{Response: &http.Response{StatusCode: 200}}, nil
}
func (f *fakeIssuesFull) CreateLabel(ctx context.Context, owner, repo string, l *github.Label) (*github.Label, *github.Response, error) {
	if f.createErr != nil {
		return nil, nil, f.createErr
	}
	f.created = append(f.created, l)
	return l, &github.Response{Response: &http.Response{StatusCode: 201}}, nil
}
func (f *fakeIssuesFull) DeleteLabel(ctx context.Context, owner, repo, name string) (*github.Response, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, name)
	return &github.Response{Response: &http.Response{StatusCode: 204}}, nil
}

type fakeGitFull struct {
	refs        map[string]bool // existing refs, e.g. "refs/heads/devops-release/0021"
	deletedRefs []string
}

func (f *fakeGitFull) GetRef(ctx context.Context, owner, repo, ref string) (*github.Reference, *github.Response, error) {
	if f.refs[ref] {
		return &github.Reference{Ref: github.Ptr(ref)}, nil, nil
	}
	return nil, nil, &github.ErrorResponse{Response: &http.Response{StatusCode: 404}}
}
func (f *fakeGitFull) DeleteRef(ctx context.Context, owner, repo, ref string) (*github.Response, error) {
	f.deletedRefs = append(f.deletedRefs, ref)
	return &github.Response{Response: &http.Response{StatusCode: 204}}, nil
}

type fakeReposFull struct {
	// fixtures
	commit *github.RepositoryCommit
}

func (f *fakeReposFull) GetCommit(ctx context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error) {
	if f.commit != nil {
		return f.commit, nil, nil
	}
	// default: not a merge
	return repoCommitWithParents(1), nil, nil
}

type fakeGH struct {
	pr    *fakePRFull
	iss   *fakeIssuesFull
	git   *fakeGitFull
	repos *fakeReposFull
}

func (f fakeGH) PR() PullRequestsAPI    { return f.pr }
func (f fakeGH) Issues() IssuesAPI      { return f.iss }
func (f fakeGH) Git() GitAPI            { return f.git }
func (f fakeGH) Repos() RepositoriesAPI { return f.repos }

type fakeCherry struct {
	workBranch string
	err        error
}

func (f fakeCherry) Pick(ctx context.Context, owner, repo, token, target, sha string, isMerge bool) (string, error) {
	return f.workBranch, f.err
}

//
// ---------- ServeHTTP + signature tests ----------
//

func TestServeHTTP_UnauthorizedSignature(t *testing.T) {
	s := &Server{WebhookSecret: []byte("secret")}
	body := []byte(`{"action":"opened"}`)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	w := httptest.NewRecorder()

	s.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestServeHTTP_PullRequestAccepted(t *testing.T) {
	s := &Server{WebhookSecret: []byte("secret")}
	body := []byte(`{"action":"opened","installation":{"id":1},"pull_request":{"merged":false}}`)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", signBody(s.WebhookSecret, body))
	w := httptest.NewRecorder()

	s.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("got status %d, want %d", w.Code, http.StatusAccepted)
	}
	// tiny wait to let async goroutine (which will early-return) run
	time.Sleep(10 * time.Millisecond)
	_, _ = io.ReadAll(w.Result().Body)
}

func TestServeHTTP_IgnoresOtherEvents(t *testing.T) {
	s := &Server{WebhookSecret: []byte("secret")}
	body := []byte(`{}`)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", signBody(s.WebhookSecret, body))
	w := httptest.NewRecorder()

	s.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestVerifySig(t *testing.T) {
	secret := []byte("sekret")
	body := []byte(`{"hello":"world"}`)

	m := hmac.New(sha256.New, secret)
	m.Write(body)
	want := "sha256=" + hex.EncodeToString(m.Sum(nil))

	req, _ := http.NewRequest("POST", "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", want)

	s := &Server{WebhookSecret: secret}
	if !s.verifySig(req, body) {
		t.Fatalf("verifySig = false, want true")
	}

	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	if s.verifySig(req, body) {
		t.Fatalf("verifySig = true, want false")
	}
}

//
// ---------- Event routing “skip” tests ----------
//

func TestHandlePREvent_Skip_NotMerged(t *testing.T) {
	s := &Server{}
	ev := &github.PullRequestEvent{
		Action:       github.Ptr("labeled"),
		Installation: &github.Installation{ID: github.Ptr(int64(1))},
		Repo: &github.Repository{
			Owner: &github.User{Login: github.Ptr("owner")},
			Name:  github.Ptr("repo"),
		},
		PullRequest: &github.PullRequest{
			Number: github.Ptr(123),
			Merged: github.Ptr(false),
		},
	}
	s.handlePREvent(context.Background(), "test-delivery", ev) // just exercise path
}

func TestHandlePREvent_Skip_NoInstallation(t *testing.T) {
	s := &Server{}
	ev := &github.PullRequestEvent{
		Action: github.Ptr("closed"),
		Repo: &github.Repository{
			Owner: &github.User{Login: github.Ptr("owner")},
			Name:  github.Ptr("repo"),
		},
		PullRequest: &github.PullRequest{
			Number: github.Ptr(1),
			Merged: github.Ptr(true),
		},
	}
	s.handlePREvent(context.Background(), "test-delivery", ev)
}

//
// ---------- Core processMergedPRWith tests ----------
//

func TestProcessMergedPR_SuccessCreatesPR(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	pr := mergedPR(7, "Fix bug", "abc123456789", "cherry-pick to devops-release/0021")
	fpr := &fakePRFull{prGet: pr}
	fiss := &fakeIssuesFull{}
	fgit := &fakeGitFull{refs: map[string]bool{
		"refs/heads/devops-release/0021": true, // target exists
	}}
	frepos := &fakeReposFull{commit: repoCommitWithParents(1)} // not a merge
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	// Cherry-pick succeeds and returns a work branch
	s.CherryRunner = fakeCherry{workBranch: "autocherry/devops-release-0021/abc1234"}

	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 7, nil, "tok")

	if fpr.createdPR == nil {
		t.Fatalf("expected a new PR to be created")
	}
	if len(fiss.comments) == 0 {
		t.Fatalf("expected at least one comment")
	}
	got := fiss.comments[len(fiss.comments)-1].GetBody()
	if !strings.HasPrefix(got, "✅") {
		t.Fatalf("expected success comment, got %q", got)
	}
}

func TestProcessMergedPR_NoOpCherryPick(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	pr := mergedPR(8, "Tiny tweak", "def123456789", "cherry-pick to devops-release/0021")
	fpr := &fakePRFull{prGet: pr}
	fiss := &fakeIssuesFull{}
	fgit := &fakeGitFull{refs: map[string]bool{"refs/heads/devops-release/0021": true}}
	frepos := &fakeReposFull{commit: repoCommitWithParents(1)}
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	s.CherryRunner = fakeCherry{err: cherry.ErrNoopCherryPick}

	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 8, nil, "tok")

	if fpr.createdPR != nil {
		t.Fatalf("did not expect a PR to be created")
	}
	if len(fiss.comments) == 0 {
		t.Fatalf("expected a comment")
	}
	got := fiss.comments[0].GetBody()
	if !strings.HasPrefix(got, "ℹ️") {
		t.Fatalf("expected info comment, got %q", got)
	}
}

func TestProcessMergedPR_Idempotent_WorkBranchExistsWithOpenPR(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	pr := mergedPR(9, "Fix", "aaa1111222", "cherry-pick to devops-release/0021")
	fpr := &fakePRFull{
		prGet: pr,
		list:  []*github.PullRequest{{HTMLURL: github.Ptr("https://example.com/existing")}},
	}
	fiss := &fakeIssuesFull{}
	fgit := &fakeGitFull{refs: map[string]bool{
		"refs/heads/devops-release/0021":                    true,
		"refs/heads/autocherry/devops-release-0021/aaa1111": true,
	}}
	frepos := &fakeReposFull{commit: repoCommitWithParents(1)}
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	// Runner should not be called here; still safe to set.
	s.CherryRunner = fakeCherry{workBranch: "autocherry/devops-release-0021/aaa1111"}

	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 9, nil, "tok")

	if fpr.createdPR != nil {
		t.Fatalf("did not expect new PR")
	}
	if len(fiss.comments) == 0 {
		t.Fatalf("expected a comment")
	}
	got := fiss.comments[0].GetBody()
	if !strings.HasPrefix(got, "ℹ️") {
		t.Fatalf("expected info comment about already open, got %q", got)
	}
}

func TestProcessMergedPR_Idempotent_WorkBranchExistsNoOpenPR(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	pr := mergedPR(12, "Fix idempotent", "ddd4444555", "cherry-pick to devops-release/0021")
	fpr := &fakePRFull{prGet: pr, list: nil} // no open PRs
	fiss := &fakeIssuesFull{}
	fgit := &fakeGitFull{refs: map[string]bool{
		"refs/heads/devops-release/0021":                    true,
		"refs/heads/autocherry/devops-release-0021/ddd4444": true, // work branch already exists
	}}
	frepos := &fakeReposFull{commit: repoCommitWithParents(1)}
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 12, nil, "tok")

	if fpr.createdPR != nil {
		t.Fatalf("did not expect new PR")
	}
	if len(fiss.comments) == 0 || !strings.HasPrefix(fiss.comments[0].GetBody(), "ℹ️") {
		t.Fatalf("expected info comment about skipping duplicate")
	}
}

func TestProcessMergedPR_TargetMissing(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	pr := mergedPR(10, "Fix", "bbb2222333", "cherry-pick to devops-release/9999")
	fpr := &fakePRFull{prGet: pr}
	fiss := &fakeIssuesFull{}
	fgit := &fakeGitFull{refs: map[string]bool{}} // target does NOT exist
	frepos := &fakeReposFull{commit: repoCommitWithParents(1)}
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	s.CherryRunner = fakeCherry{workBranch: "will-not-be-used"}

	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 10, nil, "tok")

	if fpr.createdPR != nil {
		t.Fatalf("did not expect a PR to be created")
	}
	if len(fiss.comments) == 0 {
		t.Fatalf("expected a comment")
	}
	got := fiss.comments[0].GetBody()
	if !strings.HasPrefix(got, "⚠️") {
		t.Fatalf("expected warning about target missing, got %q", got)
	}
}

func TestProcessMergedPR_MergeCommit_UsesMainlinePath(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	pr := mergedPR(11, "Merge-y fix", "cafef00d1234567", "cherry-pick to devops-release/0021")
	fpr := &fakePRFull{prGet: pr}
	fiss := &fakeIssuesFull{}
	fgit := &fakeGitFull{refs: map[string]bool{"refs/heads/devops-release/0021": true}}
	// parents=2 -> merge commit branch exercised
	frepos := &fakeReposFull{commit: repoCommitWithParents(2)}
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	s.CherryRunner = fakeCherry{workBranch: "autocherry/devops-release-0021/cafef00"}

	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 11, nil, "tok")

	if fpr.createdPR == nil {
		t.Fatalf("expected a new PR to be created")
	}
	if len(fiss.comments) == 0 || !strings.HasPrefix(fiss.comments[len(fiss.comments)-1].GetBody(), "✅") {
		t.Fatalf("expected success comment")
	}
}

func TestProcessMergedPR_FallbackToListCommits(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	// MergeCommitSHA empty, must fall back to ListCommits (last commit)
	pr := mergedPR(13, "Fallback SHA", "", "cherry-pick to devops-release/0021")
	fpr := &fakePRFull{
		prGet: pr,
		commits: []*github.RepositoryCommit{
			{SHA: github.Ptr("deadbeefcafef00d")},
			{SHA: github.Ptr("feedfacec0ffee00")}, // last one used
		},
	}
	fiss := &fakeIssuesFull{}
	fgit := &fakeGitFull{refs: map[string]bool{"refs/heads/devops-release/0021": true}}
	frepos := &fakeReposFull{commit: repoCommitWithParents(1)}
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	s.CherryRunner = fakeCherry{workBranch: "autocherry/devops-release-0021/feedfac"}

	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 13, nil, "tok")

	if fpr.createdPR == nil {
		t.Fatalf("expected PR to be created")
	}
}

func TestProcessMergedPR_CantDetermineSHA(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	// MergeCommitSHA empty, ListCommits returns empty slice -> comment a warning
	pr := mergedPR(14, "No SHA", "", "cherry-pick to devops-release/0021")
	fpr := &fakePRFull{prGet: pr, commits: []*github.RepositoryCommit{}}
	fiss := &fakeIssuesFull{}
	fgit := &fakeGitFull{refs: map[string]bool{"refs/heads/devops-release/0021": true}}
	frepos := &fakeReposFull{commit: repoCommitWithParents(1)}
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 14, nil, "tok")

	if len(fiss.comments) == 0 || !strings.Contains(fiss.comments[0].GetBody(), "Could not determine merged commit SHA") {
		t.Fatalf("expected warning about missing SHA")
	}
}

func TestProcessMergedPR_CreatePROpenError(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	pr := mergedPR(15, "Create PR error", "c001d00d1234567", "cherry-pick to devops-release/0021")
	fpr := &fakePRFull{prGet: pr, createErr: errors.New("boom")}
	fiss := &fakeIssuesFull{}
	fgit := &fakeGitFull{refs: map[string]bool{"refs/heads/devops-release/0021": true}}
	frepos := &fakeReposFull{commit: repoCommitWithParents(1)}
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	s.CherryRunner = fakeCherry{workBranch: "autocherry/devops-release-0021/c001d00"}

	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 15, nil, "tok")

	if fpr.createdPR != nil {
		t.Fatalf("did not expect PR to be created")
	}
	if len(fiss.comments) == 0 || !strings.HasPrefix(fiss.comments[0].GetBody(), "⚠️") {
		t.Fatalf("expected warning about failing to open PR")
	}
}

func TestProcessMergedPR_TargetsOverrideUsed(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	// PR labels do NOT include our targets; we pass overrides.
	pr := mergedPR(16, "Override targets", "0a0b0c0d0e0f", "cherry-pick to foo")
	fpr := &fakePRFull{prGet: pr}
	fiss := &fakeIssuesFull{}
	fgit := &fakeGitFull{refs: map[string]bool{
		"refs/heads/devops-release/0021": true, // exists
		// devops-release/9999 missing
	}}
	frepos := &fakeReposFull{commit: repoCommitWithParents(1)}
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	s.CherryRunner = fakeCherry{workBranch: "autocherry/devops-release-0021/0a0b0c0"}

	overrides := []string{"devops-release/0021", "devops-release/9999"}
	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 16, overrides, "tok")

	// Expect: one success comment for 0021 and one warning for 9999
	if len(fiss.comments) < 2 {
		t.Fatalf("expected 2+ comments, got %d", len(fiss.comments))
	}
	var ok, warn bool
	for _, c := range fiss.comments {
		b := c.GetBody()
		if strings.HasPrefix(b, "✅") {
			ok = true
		}
		if strings.HasPrefix(b, "⚠️") && strings.Contains(b, "devops-release/9999") {
			warn = true
		}
	}
	if !ok || !warn {
		t.Fatalf("expected one success and one warning comment (override path)")
	}
}

//
// ---------- cherryRunner() seam tests ----------
//

func TestCherryRunner_DefaultVsInjected(t *testing.T) {
	// Default (nil) -> realCherryRunner with configured actor
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}
	cr := s.cherryRunner()
	rc, ok := cr.(realCherryRunner)
	if !ok {
		t.Fatalf("expected realCherryRunner")
	}
	if rc.actor.Name != "bot" || rc.actor.Email != "bot@noreply" {
		t.Fatalf("actor not propagated")
	}

	// Injected
	inj := fakeCherry{workBranch: "x"}
	s2 := &Server{CherryRunner: inj}
	if _, ok := s2.cherryRunner().(fakeCherry); !ok {
		t.Fatalf("expected injected fakeCherry")
	}
}

//
// ---------- ensureLabel / enforceLabelRetention ----------
//

func TestEnsureLabel_ListError(t *testing.T) {
	fiss := &fakeIssuesFull{listErr: errors.New("list blew up")}
	gh := fakeGH{repos: &fakeReposFull{}, iss: fiss, pr: &fakePRFull{}, git: &fakeGitFull{}}
	s := &Server{}

	err := s.ensureLabel(context.Background(), gh, "o", "r", "cherry-pick to devops-release/0028")
	if err == nil || !strings.Contains(err.Error(), "list blew up") {
		t.Fatalf("expected list error to bubble up, got: %v", err)
	}
}

func TestEnforceLabelRetention_KeepZero_NoOp(t *testing.T) {
	// keep <= 0 should be a no-op (no deletes)
	fiss := &fakeIssuesFull{
		labels: []*github.Label{
			{Name: github.Ptr("cherry-pick to devops-release/0021")},
			{Name: github.Ptr("cherry-pick to devops-release/0022")},
		},
	}
	gh := fakeGH{repos: &fakeReposFull{}, iss: fiss, pr: &fakePRFull{}, git: &fakeGitFull{}}
	s := &Server{}

	if err := s.enforceLabelRetention(context.Background(), gh, "o", "r", 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fiss.deleted) != 0 {
		t.Fatalf("expected no deletions when keep=0, got %v", fiss.deleted)
	}
}

func TestEnsureLabel_CreatesWhenMissing(t *testing.T) {
	fiss := &fakeIssuesFull{
		labels: []*github.Label{
			{Name: github.Ptr("cherry-pick to devops-release/0027")},
		},
	}
	gh := fakeGH{repos: &fakeReposFull{}, iss: fiss, pr: &fakePRFull{}, git: &fakeGitFull{}}
	s := &Server{}

	err := s.ensureLabel(context.Background(), gh, "o", "r", "cherry-pick to devops-release/0028")
	if err != nil {
		t.Fatalf("ensureLabel error: %v", err)
	}
	if len(fiss.created) != 1 || fiss.created[0].GetName() != "cherry-pick to devops-release/0028" {
		t.Fatalf("label not created as expected: %+v", fiss.created)
	}
}

func TestEnsureLabel_NoOpWhenExists(t *testing.T) {
	fiss := &fakeIssuesFull{
		labels: []*github.Label{
			{Name: github.Ptr("cherry-pick to devops-release/0028")},
		},
	}
	gh := fakeGH{repos: &fakeReposFull{}, iss: fiss, pr: &fakePRFull{}, git: &fakeGitFull{}}
	s := &Server{}

	if err := s.ensureLabel(context.Background(), gh, "o", "r", "cherry-pick to devops-release/0028"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fiss.created) != 0 {
		t.Fatalf("should not create when exists")
	}
}

func TestEnforceLabelRetention_DeletesOldOnesPerTeam(t *testing.T) {
	fiss := &fakeIssuesFull{
		labels: []*github.Label{
			{Name: github.Ptr("cherry-pick to devops-release/0021")},
			{Name: github.Ptr("cherry-pick to devops-release/0022")},
			{Name: github.Ptr("cherry-pick to devops-release/0023")},
			{Name: github.Ptr("cherry-pick to devops-release/0024")},
			{Name: github.Ptr("cherry-pick to devops-release/0025")},
			{Name: github.Ptr("cherry-pick to devops-release/0026")},
			{Name: github.Ptr("cherry-pick to devops-release/0027")},
			{Name: github.Ptr("cherry-pick to devops-release/0028")},

			{Name: github.Ptr("cherry-pick to neo-release/0021")},
			{Name: github.Ptr("cherry-pick to neo-release/0022")},
			{Name: github.Ptr("cherry-pick to neo-release/0023")},
			{Name: github.Ptr("cherry-pick to neo-release/0024")},
			{Name: github.Ptr("cherry-pick to neo-release/0025")},
			{Name: github.Ptr("cherry-pick to neo-release/0026")},

			{Name: github.Ptr("cherry-pick to core-release/0024")},
			{Name: github.Ptr("cherry-pick to core-release/0025")},
			{Name: github.Ptr("cherry-pick to core-release/0026")},
			{Name: github.Ptr("cherry-pick to core-release/0027")},
			{Name: github.Ptr("cherry-pick to core-release/0028")},
		},
	}
	gh := fakeGH{repos: &fakeReposFull{}, iss: fiss, pr: &fakePRFull{}, git: &fakeGitFull{}}
	s := &Server{}

	if err := s.enforceLabelRetention(context.Background(), gh, "o", "r", 5); err != nil {
		t.Fatalf("enforceLabelRetention error: %v", err)
	}

	wantDeletes := []string{
		"cherry-pick to devops-release/0021",
		"cherry-pick to devops-release/0022",
		"cherry-pick to devops-release/0023",
		"cherry-pick to neo-release/0021",
	}
	sort.Strings(fiss.deleted)
	sort.Strings(wantDeletes)
	if strings.Join(fiss.deleted, ",") != strings.Join(wantDeletes, ",") {
		t.Fatalf("deleted = %v, want %v", fiss.deleted, wantDeletes)
	}
}

//
// ---------- removeLabelFromOpenPRs ----------
//

func TestRemoveLabelFromOpenPRs(t *testing.T) {
	fiss := &fakeIssuesFull{
		listByRepo: []*github.Issue{
			{
				Number: github.Ptr(10),
				State:  github.Ptr("open"),
				Labels: []*github.Label{{Name: github.Ptr("cherry-pick to devops-release/0030")}},
			},
			{
				Number: github.Ptr(11),
				State:  github.Ptr("closed"), // ignored
				Labels: []*github.Label{{Name: github.Ptr("cherry-pick to devops-release/0030")}},
			},
		},
	}
	gh := fakeGH{iss: fiss, repos: &fakeReposFull{}, pr: &fakePRFull{}, git: &fakeGitFull{}}
	s := &Server{}

	if err := s.removeLabelFromOpenPRs(context.Background(), gh, "o", "r", "cherry-pick to devops-release/0030"); err != nil {
		t.Fatalf("removeLabelFromOpenPRs error: %v", err)
	}
	if len(fiss.removed) != 1 || fiss.removed[0].Num != 10 {
		t.Fatalf("expected to remove label from PR #10 only; got %+v", fiss.removed)
	}
}

//
// ---------- processUnlabeled path ----------
//

func TestProcessUnlabeled_ClosesAutoCherryPRAndDeletesBranch(t *testing.T) {
	mainPR := 100
	target := "devops-release/0042"
	workBranch := "autocherry/devops-release-0042/c0ffee1"

	// One open PR from the work branch to the target
	prList := &fakePRFull{
		list: []*github.PullRequest{
			{
				Number:  github.Ptr(200),
				State:   github.Ptr("open"),
				Base:    &github.PullRequestBranch{Ref: github.Ptr(target)},
				Head:    &github.PullRequestBranch{Ref: github.Ptr(workBranch)},
				HTMLURL: github.Ptr("https://example.com/auto"),
			},
		},
	}
	// Same instance will also record edits (closing)
	git := &fakeGitFull{}
	iss := &fakeIssuesFull{}
	repos := &fakeReposFull{}
	gh := fakeGH{pr: prList, iss: iss, git: git, repos: repos}

	s := &Server{}
	if err := s.processUnlabeled(context.Background(), gh, "o", "r", mainPR, target, workBranch); err != nil {
		t.Fatalf("processUnlabeled error: %v", err)
	}

	// PR edited (closed) and branch deleted
	if len(prList.edited) == 0 || prList.edited[0].GetState() != "closed" {
		t.Fatalf("expected PR to be closed; got %+v", prList.edited)
	}
	if len(git.deletedRefs) == 0 || git.deletedRefs[0] != "refs/heads/"+workBranch {
		t.Fatalf("expected work branch ref to be deleted; got %v", git.deletedRefs)
	}
}

//
// ---------- Tiny helpers ----------
//

func Test_parseInt(t *testing.T) {
	if parseInt("0027") != 27 {
		t.Fatalf("parseInt 0027 != 27")
	}
	if parseInt("oops") != -1 {
		t.Fatalf("parseInt invalid should be -1")
	}
}

func Test_isNotFound(t *testing.T) {
	if !isNotFound(&github.ErrorResponse{Response: &http.Response{StatusCode: 404}}) {
		t.Fatalf("expected true for 404")
	}
	if isNotFound(&github.ErrorResponse{Response: &http.Response{StatusCode: 500}}) {
		t.Fatalf("expected false for 500")
	}
}

// ---------- (Optional) compile-time checks for ghshim.go ----------
var (
	_ PullRequestsAPI = (*github.PullRequestsService)(nil)
	_ IssuesAPI       = (*github.IssuesService)(nil)
	_ GitAPI          = (*github.GitService)(nil)
	_ RepositoriesAPI = (*github.RepositoriesService)(nil)
)
