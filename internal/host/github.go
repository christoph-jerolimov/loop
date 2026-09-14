package host

import (
	"context"
	"strings"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/ghapi"
)

// gitHub adapts ghapi.Client to the Host interface.
type gitHub struct {
	api *ghapi.Client
	// push is the client for the repository branches are pushed to: the
	// fork when configured, otherwise api.
	push *ghapi.Client
	// forkOwner prefixes PR heads from a fork ("owner:branch").
	forkOwner string
}

func newGitHub(r config.Repo, project string) (Host, error) {
	api, err := ghapi.New(project)
	if err != nil {
		return nil, err
	}
	h := &gitHub{api: api, push: api}
	if project == r.Project() && r.Fork != "" {
		if h.push, err = ghapi.New(r.Fork); err != nil {
			return nil, err
		}
		h.forkOwner = r.ForkOwner()
	}
	return h, nil
}

func (h *gitHub) Name() string { return config.HostGitHub }

func (h *gitHub) Viewer(ctx context.Context) (string, error) { return h.api.Viewer(ctx) }

func (h *gitHub) GetRepository(ctx context.Context) (*Repository, error) {
	r, err := h.api.GetRepository(ctx)
	if err != nil {
		return nil, err
	}
	return &Repository{FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private, CanPush: r.Permissions.Push, Admin: r.Permissions.Admin}, nil
}

// head is the PR head reference GitHub expects: "owner:branch" from a
// fork, the bare branch name otherwise.
func (h *gitHub) head(branch string) string {
	if h.forkOwner != "" {
		return h.forkOwner + ":" + branch
	}
	return branch
}

func convertPR(pr *ghapi.PullRequest) *PullRequest {
	if pr == nil {
		return nil
	}
	return &PullRequest{Number: pr.Number, ID: pr.NodeID, Title: pr.Title, State: pr.State, Draft: pr.Draft, Merged: pr.Merged,
		Mergeable: pr.Mergeable, MergeableState: pr.MergeableState, URL: pr.HTMLURL, HeadSHA: pr.Head.SHA, HeadRef: pr.Head.Ref}
}

func (h *gitHub) FindPullRequestByHead(ctx context.Context, branch string) (*PullRequest, error) {
	pr, err := h.api.FindPullRequestByHead(ctx, h.head(branch))
	if err != nil {
		return nil, err
	}
	return convertPR(pr), nil
}

func (h *gitHub) CreatePullRequest(ctx context.Context, title, body, branch, base string, draft bool) (*PullRequest, error) {
	pr, err := h.api.CreatePullRequest(ctx, title, body, h.head(branch), base, draft)
	if err != nil {
		return nil, err
	}
	return convertPR(pr), nil
}

func (h *gitHub) GetPullRequest(ctx context.Context, n int) (*PullRequest, error) {
	pr, err := h.api.GetPullRequest(ctx, n)
	if err != nil {
		return nil, err
	}
	return convertPR(pr), nil
}

func (h *gitHub) AddLabels(ctx context.Context, n int, labels []string) error {
	return h.api.AddLabels(ctx, n, labels)
}

func (h *gitHub) RequestReviewers(ctx context.Context, n int, users []string) error {
	return h.api.RequestReviewers(ctx, n, users)
}

func (h *gitHub) EnableAutoMerge(ctx context.Context, pr *PullRequest, method string) error {
	return h.api.EnableAutoMerge(ctx, pr.ID, method)
}

func (h *gitHub) MarkReadyForReview(ctx context.Context, pr *PullRequest) error {
	return h.api.MarkReadyForReview(ctx, pr.ID)
}

func (h *gitHub) ListPullRequestCommits(ctx context.Context, n int) ([]Commit, error) {
	commits, err := h.api.ListPullRequestCommits(ctx, n)
	if err != nil {
		return nil, err
	}
	out := make([]Commit, 0, len(commits))
	for _, c := range commits {
		cm := Commit{SHA: c.SHA}
		if c.Author != nil {
			cm.Author = c.Author.Login
		}
		if cm.Author == "" {
			cm.Author = c.Commit.Author.Name
		}
		if c.Committer != nil {
			cm.Committer = c.Committer.Login
		}
		out = append(out, cm)
	}
	return out, nil
}

func (h *gitHub) MergePullRequest(ctx context.Context, n int, method string) error {
	return h.api.MergePullRequest(ctx, n, method, "")
}

func (h *gitHub) DeleteBranch(ctx context.Context, branch string) error {
	return h.push.DeleteBranch(ctx, branch)
}

// ListChecks merges check runs and legacy commit statuses into one list.
func (h *gitHub) ListChecks(ctx context.Context, sha string) ([]Check, error) {
	runs, err := h.api.ListCheckRuns(ctx, sha)
	if err != nil {
		return nil, err
	}
	var out []Check
	for _, c := range runs {
		url := c.HTMLURL
		if url == "" {
			url = c.DetailsURL
		}
		out = append(out, Check{Name: c.Name, Status: c.Status, Conclusion: c.Conclusion, URL: url, Summary: c.Output.Summary, Text: c.Output.Text, JobID: c.JobID(), RunID: c.WorkflowRunID()})
	}
	_, statuses, err := h.api.CombinedStatus(ctx, sha)
	if err != nil {
		return nil, err
	}
	for _, s := range statuses {
		ck := Check{Name: s.Context, URL: s.TargetURL, Summary: s.Description}
		switch s.State {
		case "success":
			ck.Status, ck.Conclusion = "completed", "success"
		case "failure", "error":
			ck.Status, ck.Conclusion = "completed", "failure"
		default:
			ck.Status = "in_progress"
		}
		out = append(out, ck)
	}
	return out, nil
}

func (h *gitHub) JobLog(ctx context.Context, c Check) (string, error) {
	if c.JobID == 0 {
		return "", nil
	}
	return h.api.JobLogs(ctx, c.JobID)
}

func (h *gitHub) RerunFailed(ctx context.Context, runID int64) error {
	return h.api.RerunFailedJobs(ctx, runID)
}

func (h *gitHub) SetStatus(ctx context.Context, sha, name, state, description, targetURL string) error {
	return h.api.SetStatus(ctx, sha, name, state, description, targetURL)
}

func (h *gitHub) ListReviews(ctx context.Context, n int) ([]Review, error) {
	reviews, err := h.api.ListReviews(ctx, n)
	if err != nil {
		return nil, err
	}
	out := make([]Review, 0, len(reviews))
	for _, r := range reviews {
		out = append(out, Review{ID: r.ID, User: User{Login: r.User.Login}, Body: r.Body, State: r.State, URL: r.HTMLURL, SubmittedAt: r.SubmittedAt})
	}
	return out, nil
}

func (h *gitHub) ListReviewComments(ctx context.Context, n int) ([]ReviewComment, error) {
	comments, err := h.api.ListReviewComments(ctx, n)
	if err != nil {
		return nil, err
	}
	out := make([]ReviewComment, 0, len(comments))
	for _, c := range comments {
		out = append(out, ReviewComment{ID: c.ID, User: User{Login: c.User.Login}, Body: c.Body, Path: c.Path, Line: c.Line, DiffHunk: c.DiffHunk, URL: c.HTMLURL, CreatedAt: c.CreatedAt})
	}
	return out, nil
}

func (h *gitHub) ReplyToReviewComment(ctx context.Context, n int, c ReviewComment, body string) error {
	return h.api.ReplyToReviewComment(ctx, n, c.ID, body)
}

// ResolveReviewThread looks the thread of the comment up (GitHub only
// exposes threads through GraphQL) and resolves it unless it already is.
func (h *gitHub) ResolveReviewThread(ctx context.Context, n int, c ReviewComment) error {
	threads, err := h.api.ReviewThreads(ctx, n)
	if err != nil {
		return err
	}
	t, ok := threads[c.ID]
	if !ok || t.Resolved {
		return nil
	}
	return h.api.ResolveReviewThread(ctx, t.ID)
}

func (h *gitHub) ListComments(ctx context.Context, ref Ref) ([]Comment, error) {
	comments, err := h.api.ListIssueComments(ctx, ref.Number)
	if err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(comments))
	for _, c := range comments {
		out = append(out, Comment{ID: c.ID, Ref: ref, User: User{Login: c.User.Login}, Body: c.Body, URL: c.HTMLURL, CreatedAt: c.CreatedAt})
	}
	return out, nil
}

func (h *gitHub) CreateComment(ctx context.Context, ref Ref, body string) error {
	return h.api.CreateComment(ctx, ref.Number, body)
}

func (h *gitHub) React(ctx context.Context, c Comment, content string) error {
	return h.api.ReactToComment(ctx, c.ID, content)
}

// Permission normalises GitHub's role names to admin, write, read or none.
func (h *gitHub) Permission(ctx context.Context, u User) (string, error) {
	p, err := h.api.CollaboratorPermission(ctx, u.Login)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(p) {
	case "admin", "maintain":
		return "admin", nil
	case "write":
		return "write", nil
	case "read", "triage":
		return "read", nil
	}
	return "none", nil
}
