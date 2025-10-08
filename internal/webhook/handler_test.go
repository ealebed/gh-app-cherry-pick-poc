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
// In go-github/v75, Parents is []*github.Commit (not []*github.RepositoryCommit).
func repoCommitWithParents(n int) *github.RepositoryCommit {
	ps := make([]*github.Commit, n)
	for i := range ps {
		ps[i] = &github.Commit{}
	}
	return &github.RepositoryCommit{Parents: ps}
}

//
// ---------- Tiny fakes for GH + cherry runner ----------
//

type fakePR struct {
	prGet      *github.PullRequest
	commits    []*github.RepositoryCommit
	createdPR  *github.PullRequest
	createErr  error
	list       []*github.PullRequest
	listErr    error
	commitsErr error
}

func (f *fakePR) Get(ctx context.Context, owner, repo string, number int) (*github.PullRequest, *github.Response, error) {
	return f.prGet, nil, nil
}
func (f *fakePR) ListCommits(ctx context.Context, owner, repo string, number int, opt *github.ListOptions) ([]*github.RepositoryCommit, *github.Response, error) {
	if f.commits != nil {
		return f.commits, nil, f.commitsErr
	}
	// default: return one with the PR's merge SHA (may be empty)
	return []*github.RepositoryCommit{{SHA: f.prGet.MergeCommitSHA}}, nil, f.commitsErr
}
func (f *fakePR) List(ctx context.Context, owner, repo string, opts *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error) {
	return f.list, nil, f.listErr
}
func (f *fakePR) Create(ctx context.Context, owner, repo string, pr *github.NewPullRequest) (*github.PullRequest, *github.Response, error) {
	if f.createErr != nil {
		return nil, nil, f.createErr
	}
	f.createdPR = &github.PullRequest{
		HTMLURL: github.Ptr("https://example.com/newpr"),
	}
	return f.createdPR, nil, nil
}

type fakeIssues struct {
	comments []*github.IssueComment
}

func (f *fakeIssues) CreateComment(ctx context.Context, owner, repo string, number int, comment *github.IssueComment) (*github.IssueComment, *github.Response, error) {
	f.comments = append(f.comments, comment)
	return comment, nil, nil
}

type fakeGit struct {
	refs map[string]bool // existing refs, e.g. "refs/heads/devops-release/0021"
}

func (f *fakeGit) GetRef(ctx context.Context, owner, repo, ref string) (*github.Reference, *github.Response, error) {
	if f.refs[ref] {
		return &github.Reference{Ref: github.Ptr(ref)}, nil, nil
	}
	return nil, nil, &github.ErrorResponse{Response: &http.Response{StatusCode: 404}}
}

type fakeRepos struct {
	commit *github.RepositoryCommit
}

func (f *fakeRepos) GetCommit(ctx context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error) {
	if f.commit != nil {
		return f.commit, nil, nil
	}
	// default: not a merge
	return repoCommitWithParents(1), nil, nil
}

type fakeGH struct {
	pr    *fakePR
	iss   *fakeIssues
	git   *fakeGit
	repos *fakeRepos
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
	fpr := &fakePR{prGet: pr}
	fiss := &fakeIssues{}
	fgit := &fakeGit{refs: map[string]bool{
		"refs/heads/devops-release/0021": true, // target exists
	}}
	frepos := &fakeRepos{commit: repoCommitWithParents(1)} // not a merge
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
	fpr := &fakePR{prGet: pr}
	fiss := &fakeIssues{}
	fgit := &fakeGit{refs: map[string]bool{"refs/heads/devops-release/0021": true}}
	frepos := &fakeRepos{commit: repoCommitWithParents(1)}
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
	fpr := &fakePR{
		prGet: pr,
		list:  []*github.PullRequest{{HTMLURL: github.Ptr("https://example.com/existing")}},
	}
	fiss := &fakeIssues{}
	fgit := &fakeGit{refs: map[string]bool{
		"refs/heads/devops-release/0021":                    true,
		"refs/heads/autocherry/devops-release-0021/aaa1111": true,
	}}
	frepos := &fakeRepos{commit: repoCommitWithParents(1)}
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
	fpr := &fakePR{prGet: pr, list: nil} // no open PRs
	fiss := &fakeIssues{}
	fgit := &fakeGit{refs: map[string]bool{
		"refs/heads/devops-release/0021":                    true,
		"refs/heads/autocherry/devops-release-0021/ddd4444": true, // work branch already exists
	}}
	frepos := &fakeRepos{commit: repoCommitWithParents(1)}
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
	fpr := &fakePR{prGet: pr}
	fiss := &fakeIssues{}
	fgit := &fakeGit{refs: map[string]bool{}} // target does NOT exist
	frepos := &fakeRepos{commit: repoCommitWithParents(1)}
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
	fpr := &fakePR{prGet: pr}
	fiss := &fakeIssues{}
	fgit := &fakeGit{refs: map[string]bool{"refs/heads/devops-release/0021": true}}
	// parents=2 -> merge commit branch exercised
	frepos := &fakeRepos{commit: repoCommitWithParents(2)}
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
	fpr := &fakePR{
		prGet: pr,
		commits: []*github.RepositoryCommit{
			{SHA: github.Ptr("deadbeefcafef00d")},
			{SHA: github.Ptr("feedfacec0ffee00")}, // last one used
		},
	}
	fiss := &fakeIssues{}
	fgit := &fakeGit{refs: map[string]bool{"refs/heads/devops-release/0021": true}}
	frepos := &fakeRepos{commit: repoCommitWithParents(1)}
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
	fpr := &fakePR{prGet: pr, commits: []*github.RepositoryCommit{}}
	fiss := &fakeIssues{}
	fgit := &fakeGit{refs: map[string]bool{"refs/heads/devops-release/0021": true}}
	frepos := &fakeRepos{commit: repoCommitWithParents(1)}
	gh := fakeGH{pr: fpr, iss: fiss, git: fgit, repos: frepos}

	s.processMergedPRWith(context.Background(), "d", gh, "o", "r", 14, nil, "tok")

	if len(fiss.comments) == 0 || !strings.Contains(fiss.comments[0].GetBody(), "Could not determine merged commit SHA") {
		t.Fatalf("expected warning about missing SHA")
	}
}

func TestProcessMergedPR_CreatePROpenError(t *testing.T) {
	s := &Server{GitUserName: "bot", GitUserEmail: "bot@noreply"}

	pr := mergedPR(15, "Create PR error", "c001d00d1234567", "cherry-pick to devops-release/0021")
	fpr := &fakePR{prGet: pr, createErr: errors.New("boom")}
	fiss := &fakeIssues{}
	fgit := &fakeGit{refs: map[string]bool{"refs/heads/devops-release/0021": true}}
	frepos := &fakeRepos{commit: repoCommitWithParents(1)}
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
	fpr := &fakePR{prGet: pr}
	fiss := &fakeIssues{}
	fgit := &fakeGit{refs: map[string]bool{
		"refs/heads/devops-release/0021": true, // exists
		// devops-release/9999 missing
	}}
	frepos := &fakeRepos{commit: repoCommitWithParents(1)}
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

// ---------- (Optional) compile-time checks for ghshim.go ----------
// These ensure the real go-github types implement our narrow interfaces.
var (
	_ PullRequestsAPI = (*github.PullRequestsService)(nil)
	_ IssuesAPI       = (*github.IssuesService)(nil)
	_ GitAPI          = (*github.GitService)(nil)
	_ RepositoriesAPI = (*github.RepositoriesService)(nil)
)
