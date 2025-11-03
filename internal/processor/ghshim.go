package processor

import (
	"context"
	"net/http"

	github "github.com/google/go-github/v75/github"
)

// Narrow interfaces for the subset of go-github we use.

type PullRequestsAPI interface {
	Get(ctx context.Context, owner, repo string, number int) (*github.PullRequest, *github.Response, error)
	List(ctx context.Context, owner, repo string, opts *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error)
	ListCommits(ctx context.Context, owner, repo string, number int, opt *github.ListOptions) ([]*github.RepositoryCommit, *github.Response, error)
	Create(ctx context.Context, owner, repo string, pr *github.NewPullRequest) (*github.PullRequest, *github.Response, error)
	Edit(ctx context.Context, owner, repo string, number int, pr *github.PullRequest) (*github.PullRequest, *github.Response, error)
}

type IssuesAPI interface {
	CreateComment(ctx context.Context, owner, repo string, number int, comment *github.IssueComment) (*github.IssueComment, *github.Response, error)
	ListByRepo(ctx context.Context, owner, repo string, opt *github.IssueListByRepoOptions) ([]*github.Issue, *github.Response, error)
	RemoveLabelForIssue(ctx context.Context, owner, repo string, number int, label string) (*github.Response, error)

	ListLabels(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Label, *github.Response, error)
	CreateLabel(ctx context.Context, owner, repo string, label *github.Label) (*github.Label, *github.Response, error)
	DeleteLabel(ctx context.Context, owner, repo, name string) (*github.Response, error)

	// NEW: needed so we can attach "orig-author:<login>" to the cherry-pick PR.
	AddLabelsToIssue(ctx context.Context, owner, repo string, number int, labels []string) ([]*github.Label, *github.Response, error)
}

type GitAPI interface {
	GetRef(ctx context.Context, owner, repo, ref string) (*github.Reference, *github.Response, error)
	DeleteRef(ctx context.Context, owner, repo, ref string) (*github.Response, error)
}

type RepositoriesAPI interface {
	GetCommit(ctx context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error)
}

type GH interface {
	PR() PullRequestsAPI
	Issues() IssuesAPI
	Git() GitAPI
	Repos() RepositoriesAPI
}

// real wrapper used in production
type realGH struct{ c *github.Client }

func (r realGH) PR() PullRequestsAPI    { return r.c.PullRequests }
func (r realGH) Issues() IssuesAPI      { return r.c.Issues }
func (r realGH) Git() GitAPI            { return r.c.Git }
func (r realGH) Repos() RepositoriesAPI { return r.c.Repositories }

// Optional compile-time assertions
var (
	_ PullRequestsAPI = (*github.PullRequestsService)(nil)
	_ IssuesAPI       = (*github.IssuesService)(nil)
	_ GitAPI          = (*github.GitService)(nil)
	_ RepositoriesAPI = (*github.RepositoriesService)(nil)
	_ *http.Client    // keep import
)
