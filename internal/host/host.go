// Package host abstracts the code host behind a repository: pull requests
// (merge requests on GitLab), reviews, checks, comments and merging. The
// engine talks to this interface only; the GitHub and GitLab adapters
// translate to their APIs.
package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/christoph-jerolimov/loop/internal/config"
)

// Host is one code host for one repository.
type Host interface {
	// Name is "github" or "gitlab".
	Name() string
	// Viewer is the login of the account behind the token.
	Viewer(ctx context.Context) (string, error)
	GetRepository(ctx context.Context) (*Repository, error)

	// FindPullRequestByHead returns the open PR for a run branch, if any.
	FindPullRequestByHead(ctx context.Context, branch string) (*PullRequest, error)
	CreatePullRequest(ctx context.Context, title, body, branch, base string, draft bool) (*PullRequest, error)
	GetPullRequest(ctx context.Context, n int) (*PullRequest, error)
	AddLabels(ctx context.Context, n int, labels []string) error
	RequestReviewers(ctx context.Context, n int, users []string) error
	EnableAutoMerge(ctx context.Context, pr *PullRequest, method string) error
	MarkReadyForReview(ctx context.Context, pr *PullRequest) error
	// ListPullRequestCommits returns the commits of the PR, oldest first.
	ListPullRequestCommits(ctx context.Context, n int) ([]Commit, error)
	// MergePullRequest merges with squash, merge or rebase.
	MergePullRequest(ctx context.Context, n int, method string) error
	// DeleteBranch removes the branch from the repository it was pushed
	// to; a missing branch is not an error.
	DeleteBranch(ctx context.Context, branch string) error

	// ListChecks returns every check on a commit: CI jobs and statuses.
	ListChecks(ctx context.Context, sha string) ([]Check, error)
	// JobLog downloads the log of a failed CI job; "" when there is none.
	JobLog(ctx context.Context, c Check) (string, error)
	// RerunFailed re-runs the failed jobs of a CI run.
	RerunFailed(ctx context.Context, runID int64) error
	// SetStatus posts a commit status (pending, success, failure, error).
	SetStatus(ctx context.Context, sha, name, state, description, targetURL string) error

	ListReviews(ctx context.Context, n int) ([]Review, error)
	ListReviewComments(ctx context.Context, n int) ([]ReviewComment, error)
	ReplyToReviewComment(ctx context.Context, n int, c ReviewComment, body string) error
	ResolveReviewThread(ctx context.Context, n int, c ReviewComment) error

	// ListComments returns the conversation comments of an issue or PR,
	// oldest first, without system notes.
	ListComments(ctx context.Context, ref Ref) ([]Comment, error)
	CreateComment(ctx context.Context, ref Ref, body string) error
	// React adds a reaction ("+1" or "confused") to a comment.
	React(ctx context.Context, c Comment, content string) error
	// Permission is the user's access to the repository: admin, write,
	// read or none.
	Permission(ctx context.Context, u User) (string, error)
}

// Ref addresses an issue or a pull request by number.
type Ref struct {
	Number int
	PR     bool
}

// User is an account on the host.
type User struct {
	Login string
	ID    int64
}

// Repository is what loop doctor wants to know.
type Repository struct {
	FullName      string
	DefaultBranch string
	Private       bool
	CanPush       bool
	Admin         bool
}

// PullRequest is the subset of PR fields loop uses.
type PullRequest struct {
	Number int
	// ID is the host's own identifier: GitHub's node id, GitLab's global id.
	ID     string
	Title  string
	State  string // open, closed
	Draft  bool
	Merged bool
	// Mergeable is nil while the host is still computing it.
	Mergeable *bool
	// MergeableState is clean, dirty (conflicts), blocked (rules the host
	// enforces) or "" (unknown).
	MergeableState string
	URL            string
	HeadSHA        string
	HeadRef        string
}

// Commit is one commit of a pull request. Author and Committer are logins
// when the host knows them, otherwise the name from the commit.
type Commit struct {
	SHA       string
	Author    string
	Committer string
}

// Review is one submitted review.
type Review struct {
	ID          int64
	User        User
	Body        string
	State       string // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	URL         string
	SubmittedAt time.Time
}

// ReviewComment is an inline comment on the diff.
type ReviewComment struct {
	ID int64
	// ThreadID is the discussion the comment belongs to, when the host
	// exposes it up front.
	ThreadID  string
	User      User
	Body      string
	Path      string
	Line      int
	DiffHunk  string
	URL       string
	CreatedAt time.Time
	Resolved  bool
}

// Comment is a conversation comment on an issue or PR.
type Comment struct {
	ID        int64
	Ref       Ref
	User      User
	Body      string
	URL       string
	CreatedAt time.Time
}

// Check is one CI job or status on a commit.
type Check struct {
	Name string
	// Status is "completed" once the check has a conclusion.
	Status string
	// Conclusion is success, failure, neutral, cancelled, skipped,
	// timed_out, action_required or startup_failure.
	Conclusion string
	URL        string
	Summary    string
	Text       string
	// JobID and RunID identify the CI job and its run (workflow run or
	// pipeline) for logs and re-runs; 0 when the check is not a CI job.
	JobID, RunID int64
}

// Failed reports whether the check ended badly.
func (c Check) Failed() bool {
	switch c.Conclusion {
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure":
		return true
	}
	return false
}

// CanPush reports whether a permission level allows pushing.
func CanPush(permission string) bool {
	switch permission {
	case "admin", "maintain", "write":
		return true
	}
	return false
}

// statusError is implemented by the API clients' error types.
type statusError interface {
	error
	HTTPStatus() int
}

// Status returns the HTTP status behind an API error, 0 for other errors.
func Status(err error) int {
	var se statusError
	if errors.As(err, &se) {
		return se.HTTPStatus()
	}
	return 0
}

// Body returns the response body behind an API error, or its message.
func Body(err error) string {
	type bodyError interface{ ResponseBody() string }
	var be bodyError
	if errors.As(err, &be) {
		return be.ResponseBody()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// New creates the host for the repository in loop.yaml.
func New(r config.Repo) (Host, error) {
	return NewProject(r, r.Project())
}

// NewProject creates a host client for another project of the same host,
// such as the fork.
func NewProject(r config.Repo, project string) (Host, error) {
	switch r.Host() {
	case config.HostGitLab:
		return newGitLab(r, project)
	case config.HostGitHub:
		return newGitHub(r, project)
	}
	return nil, fmt.Errorf("unknown host %q", r.Host())
}

// Draft prefixes a title the way GitLab marks drafts.
func draftTitle(title string, draft bool) string {
	if draft && !strings.HasPrefix(title, "Draft: ") {
		return "Draft: " + title
	}
	return title
}

// now is a variable so tests can pin it.
var now = time.Now
