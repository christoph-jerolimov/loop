// Package ghapi is a small GitHub REST/GraphQL client covering what loop
// needs: issues, labels, comments, pull requests, reviews, checks, merge.
package ghapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/christoph-jerolimov/loop/internal/httpx"
)

// Client talks to one repository.
type Client struct {
	Owner, Repo string
	Token       string
	BaseURL     string
	HTTP        *http.Client
	// Retry governs retries and rate-limit waits; zero means httpx.Default.
	Retry httpx.Policy
}

// New creates a client for owner/repo using the token from GITHUB_TOKEN,
// GH_TOKEN or "gh auth token".
func New(ownerRepo string) (*Client, error) {
	owner, repo, ok := strings.Cut(ownerRepo, "/")
	if !ok {
		return nil, fmt.Errorf("github repo must be owner/name, got %q", ownerRepo)
	}
	tok, err := Token()
	if err != nil {
		return nil, err
	}
	base := os.Getenv("GITHUB_API_URL")
	if base == "" {
		base = "https://api.github.com"
	}
	return &Client{Owner: owner, Repo: repo, Token: tok, BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 60 * time.Second}, Retry: httpx.Default}, nil
}

// Token resolves the GitHub token.
func Token() (string, error) {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v, nil
		}
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil && len(bytes.TrimSpace(out)) > 0 {
		return string(bytes.TrimSpace(out)), nil
	}
	return "", errors.New("no GitHub token: set GITHUB_TOKEN or log in with gh")
}

// Error is a non-2xx response.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string { return fmt.Sprintf("github: HTTP %d: %s", e.Status, e.Body) }

func (c *Client) policy() httpx.Policy {
	if c.Retry.Attempts == 0 {
		return httpx.Default
	}
	return c.Retry
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		payload = b
	}
	u := path
	if !strings.HasPrefix(path, "http") {
		u = c.BaseURL + path
	}
	resp, err := c.policy().Do(ctx, c.HTTP, func() (*http.Request, error) {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) repoPath(format string, a ...any) string {
	return fmt.Sprintf("/repos/%s/%s"+format, append([]any{c.Owner, c.Repo}, a...)...)
}

// paged fetches every page of a list endpoint.
func (c *Client) paged(ctx context.Context, path string, q url.Values, each func(json.RawMessage) error) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", "100")
	for page := 1; page <= 50; page++ {
		q.Set("page", fmt.Sprint(page))
		var items []json.RawMessage
		if err := c.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &items); err != nil {
			return err
		}
		for _, it := range items {
			if err := each(it); err != nil {
				return err
			}
		}
		if len(items) < 100 {
			return nil
		}
	}
	return nil
}

// User is a GitHub account.
type User struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// Label is an issue label.
type Label struct {
	Name string `json:"name"`
}

// Issue is an issue or the issue half of a pull request.
type Issue struct {
	Number      int       `json:"number"`
	NodeID      string    `json:"node_id"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	StateReason string    `json:"state_reason"`
	HTMLURL     string    `json:"html_url"`
	User        User      `json:"user"`
	Labels      []Label   `json:"labels"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request,omitempty"`
}

// Comment is an issue comment.
type Comment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	// AuthorAssociation is GitHub's relationship of the author to the
	// repository: OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR, NONE, ...
	AuthorAssociation string `json:"author_association"`
}

// ListIssues returns open issues (not PRs) carrying all labels, oldest first.
func (c *Client) ListIssues(ctx context.Context, labels []string) ([]Issue, error) {
	q := url.Values{"state": {"open"}, "sort": {"created"}, "direction": {"asc"}}
	if len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	var out []Issue
	err := c.paged(ctx, c.repoPath("/issues"), q, func(raw json.RawMessage) error {
		var is Issue
		if err := json.Unmarshal(raw, &is); err != nil {
			return err
		}
		if is.PullRequest == nil {
			out = append(out, is)
		}
		return nil
	})
	return out, err
}

// GetIssue fetches one issue.
func (c *Client) GetIssue(ctx context.Context, n int) (*Issue, error) {
	var is Issue
	if err := c.do(ctx, http.MethodGet, c.repoPath("/issues/%d", n), nil, &is); err != nil {
		return nil, err
	}
	return &is, nil
}

// ListIssueComments returns all comments on an issue or PR, oldest first.
func (c *Client) ListIssueComments(ctx context.Context, n int) ([]Comment, error) {
	var out []Comment
	err := c.paged(ctx, c.repoPath("/issues/%d/comments", n), nil, func(raw json.RawMessage) error {
		var cm Comment
		if err := json.Unmarshal(raw, &cm); err != nil {
			return err
		}
		out = append(out, cm)
		return nil
	})
	return out, err
}

// CreateComment posts a comment on an issue or PR.
func (c *Client) CreateComment(ctx context.Context, n int, body string) error {
	return c.do(ctx, http.MethodPost, c.repoPath("/issues/%d/comments", n), map[string]string{"body": body}, nil)
}

// AddLabels adds labels to an issue.
func (c *Client) AddLabels(ctx context.Context, n int, labels []string) error {
	return c.do(ctx, http.MethodPost, c.repoPath("/issues/%d/labels", n), map[string][]string{"labels": labels}, nil)
}

// RemoveLabel removes one label; a missing label is not an error.
func (c *Client) RemoveLabel(ctx context.Context, n int, label string) error {
	err := c.do(ctx, http.MethodDelete, c.repoPath("/issues/%d/labels/%s", n, url.PathEscape(label)), nil, nil)
	var ge *Error
	if errors.As(err, &ge) && ge.Status == 404 {
		return nil
	}
	return err
}

// CloseIssue closes an issue as completed.
func (c *Client) CloseIssue(ctx context.Context, n int) error {
	return c.do(ctx, http.MethodPatch, c.repoPath("/issues/%d", n), map[string]string{"state": "closed", "state_reason": "completed"}, nil)
}

// PullRequest is the subset of PR fields loop uses.
type PullRequest struct {
	Number         int       `json:"number"`
	NodeID         string    `json:"node_id"`
	Title          string    `json:"title"`
	Body           string    `json:"body"`
	State          string    `json:"state"`
	Draft          bool      `json:"draft"`
	Merged         bool      `json:"merged"`
	Mergeable      *bool     `json:"mergeable"`
	MergeableState string    `json:"mergeable_state"`
	HTMLURL        string    `json:"html_url"`
	UpdatedAt      time.Time `json:"updated_at"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	AutoMerge *struct {
		MergeMethod string `json:"merge_method"`
	} `json:"auto_merge"`
}

// CreatePullRequest opens a PR from head into base.
func (c *Client) CreatePullRequest(ctx context.Context, title, body, head, base string, draft bool) (*PullRequest, error) {
	var pr PullRequest
	in := map[string]any{"title": title, "body": body, "head": head, "base": base, "draft": draft}
	if err := c.do(ctx, http.MethodPost, c.repoPath("/pulls"), in, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// FindPullRequestByHead returns the open PR for a head, if any. head is
// "owner:branch"; a bare branch name means a branch of this repository.
func (c *Client) FindPullRequestByHead(ctx context.Context, head string) (*PullRequest, error) {
	if !strings.Contains(head, ":") {
		head = c.Owner + ":" + head
	}
	q := url.Values{"state": {"open"}, "head": {head}}
	var prs []PullRequest
	if err := c.do(ctx, http.MethodGet, c.repoPath("/pulls")+"?"+q.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

// Commit is one commit of a pull request with the GitHub accounts behind it.
type Commit struct {
	SHA       string `json:"sha"`
	Author    *User  `json:"author"`
	Committer *User  `json:"committer"`
	Commit    struct {
		Author struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"commit"`
}

// ListPullRequestCommits returns the commits of a PR, oldest first.
func (c *Client) ListPullRequestCommits(ctx context.Context, n int) ([]Commit, error) {
	var out []Commit
	err := c.paged(ctx, c.repoPath("/pulls/%d/commits", n), nil, func(raw json.RawMessage) error {
		var cm Commit
		if err := json.Unmarshal(raw, &cm); err != nil {
			return err
		}
		out = append(out, cm)
		return nil
	})
	return out, err
}

// GetPullRequest fetches a PR.
func (c *Client) GetPullRequest(ctx context.Context, n int) (*PullRequest, error) {
	var pr PullRequest
	if err := c.do(ctx, http.MethodGet, c.repoPath("/pulls/%d", n), nil, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// RequestReviewers asks users to review.
func (c *Client) RequestReviewers(ctx context.Context, n int, users []string) error {
	if len(users) == 0 {
		return nil
	}
	return c.do(ctx, http.MethodPost, c.repoPath("/pulls/%d/requested_reviewers", n), map[string][]string{"reviewers": users}, nil)
}

// Review is a submitted PR review.
type Review struct {
	ID          int64     `json:"id"`
	User        User      `json:"user"`
	Body        string    `json:"body"`
	State       string    `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	HTMLURL     string    `json:"html_url"`
	CommitID    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// ListReviews returns every review on a PR.
func (c *Client) ListReviews(ctx context.Context, n int) ([]Review, error) {
	var out []Review
	err := c.paged(ctx, c.repoPath("/pulls/%d/reviews", n), nil, func(raw json.RawMessage) error {
		var r Review
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

// ReviewComment is an inline code comment.
type ReviewComment struct {
	ID        int64     `json:"id"`
	User      User      `json:"user"`
	Body      string    `json:"body"`
	Path      string    `json:"path"`
	Line      int       `json:"line"`
	DiffHunk  string    `json:"diff_hunk"`
	HTMLURL   string    `json:"html_url"`
	CommitID  string    `json:"commit_id"`
	CreatedAt time.Time `json:"created_at"`
	InReplyTo int64     `json:"in_reply_to_id"`
}

// ListReviewComments returns inline comments on a PR.
func (c *Client) ListReviewComments(ctx context.Context, n int) ([]ReviewComment, error) {
	var out []ReviewComment
	err := c.paged(ctx, c.repoPath("/pulls/%d/comments", n), nil, func(raw json.RawMessage) error {
		var r ReviewComment
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

// ReplyToReviewComment answers an inline review comment in its thread.
func (c *Client) ReplyToReviewComment(ctx context.Context, pr int, commentID int64, body string) error {
	return c.do(ctx, http.MethodPost, c.repoPath("/pulls/%d/comments/%d/replies", pr, commentID), map[string]string{"body": body}, nil)
}

// ReviewThreads maps inline comment ids to the node id of their thread
// and whether the thread is resolved.
func (c *Client) ReviewThreads(ctx context.Context, pr int) (map[int64]ReviewThread, error) {
	var resp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int64 `json:"databaseId"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	q := `query($owner: String!, $name: String!, $pr: Int!) { repository(owner: $owner, name: $name) { pullRequest(number: $pr) { reviewThreads(first: 100) { nodes { id isResolved comments(first: 100) { nodes { databaseId } } } } } } }`
	if err := c.do(ctx, http.MethodPost, c.graphqlURL(), map[string]any{"query": q, "variables": map[string]any{"owner": c.Owner, "name": c.Repo, "pr": pr}}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("github graphql: %s", resp.Errors[0].Message)
	}
	out := map[int64]ReviewThread{}
	for _, t := range resp.Data.Repository.PullRequest.ReviewThreads.Nodes {
		for _, cm := range t.Comments.Nodes {
			out[cm.DatabaseID] = ReviewThread{ID: t.ID, Resolved: t.IsResolved}
		}
	}
	return out, nil
}

// ReviewThread is a review conversation on the PR.
type ReviewThread struct {
	ID       string
	Resolved bool
}

// ResolveReviewThread marks a thread resolved.
func (c *Client) ResolveReviewThread(ctx context.Context, threadID string) error {
	return c.graphql(ctx, `mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { id } } }`, map[string]any{"id": threadID})
}

func (c *Client) graphqlURL() string {
	if c.BaseURL == "https://api.github.com" {
		return "https://api.github.com/graphql"
	}
	return strings.TrimSuffix(c.BaseURL, "/api/v3") + "/graphql"
}

// CollaboratorPermission returns the permission level of a user on the
// repository: admin, maintain, write, triage, read or none.
func (c *Client) CollaboratorPermission(ctx context.Context, login string) (string, error) {
	var resp struct {
		Permission string `json:"permission"`
		RoleName   string `json:"role_name"`
	}
	err := c.do(ctx, http.MethodGet, c.repoPath("/collaborators/%s/permission", url.PathEscape(login)), nil, &resp)
	var ge *Error
	if errors.As(err, &ge) && ge.Status == 404 {
		return "none", nil
	}
	if err != nil {
		return "", err
	}
	if resp.RoleName != "" {
		return resp.RoleName, nil
	}
	return resp.Permission, nil
}

// CanPush reports whether a permission level allows pushing.
func CanPush(permission string) bool {
	switch permission {
	case "admin", "maintain", "write":
		return true
	}
	return false
}

// ReactToComment adds a reaction (+1, -1, eyes, confused, ...) to an issue
// or PR comment.
func (c *Client) ReactToComment(ctx context.Context, commentID int64, content string) error {
	return c.do(ctx, http.MethodPost, c.repoPath("/issues/comments/%d/reactions", commentID), map[string]string{"content": content}, nil)
}

// CheckRun is one check on a commit.
type CheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`     // queued, in_progress, completed
	Conclusion string `json:"conclusion"` // success, failure, neutral, cancelled, skipped, timed_out, action_required
	HTMLURL    string `json:"html_url"`
	DetailsURL string `json:"details_url"`
	Output     struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Text    string `json:"text"`
	} `json:"output"`
}

// ListCheckRuns returns all check runs for a commit.
func (c *Client) ListCheckRuns(ctx context.Context, sha string) ([]CheckRun, error) {
	var out []CheckRun
	q := url.Values{"per_page": {"100"}}
	for page := 1; page <= 10; page++ {
		q.Set("page", fmt.Sprint(page))
		var resp struct {
			Total     int        `json:"total_count"`
			CheckRuns []CheckRun `json:"check_runs"`
		}
		if err := c.do(ctx, http.MethodGet, c.repoPath("/commits/%s/check-runs", sha)+"?"+q.Encode(), nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.CheckRuns...)
		if len(out) >= resp.Total || len(resp.CheckRuns) == 0 {
			break
		}
	}
	return out, nil
}

// Status is one legacy commit status context.
type Status struct {
	Context     string `json:"context"`
	State       string `json:"state"` // error, failure, pending, success
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

// CombinedStatus returns the legacy statuses for a commit.
func (c *Client) CombinedStatus(ctx context.Context, sha string) (string, []Status, error) {
	var resp struct {
		State    string   `json:"state"`
		Statuses []Status `json:"statuses"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoPath("/commits/%s/status", sha), nil, &resp); err != nil {
		return "", nil, err
	}
	return resp.State, resp.Statuses, nil
}

// MergePullRequest merges with the given method (merge, squash, rebase).
func (c *Client) MergePullRequest(ctx context.Context, n int, method, title string) error {
	in := map[string]string{"merge_method": method}
	if title != "" {
		in["commit_title"] = title
	}
	return c.do(ctx, http.MethodPut, c.repoPath("/pulls/%d/merge", n), in, nil)
}

// DeleteBranch removes a remote branch; a missing branch is not an error.
func (c *Client) DeleteBranch(ctx context.Context, branch string) error {
	err := c.do(ctx, http.MethodDelete, c.repoPath("/git/refs/heads/%s", branch), nil, nil)
	var ge *Error
	if errors.As(err, &ge) && (ge.Status == 404 || ge.Status == 422) {
		return nil
	}
	return err
}

// graphql runs one GraphQL mutation or query.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any) error {
	endpoint := c.graphqlURL()
	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.do(ctx, http.MethodPost, endpoint, map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		return fmt.Errorf("github graphql: %s", resp.Errors[0].Message)
	}
	return nil
}

// MarkReadyForReview converts a draft PR into a regular PR.
func (c *Client) MarkReadyForReview(ctx context.Context, nodeID string) error {
	return c.graphql(ctx, `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { clientMutationId } }`, map[string]any{"id": nodeID})
}

// EnableAutoMerge turns on GitHub's auto-merge for the PR.
func (c *Client) EnableAutoMerge(ctx context.Context, nodeID, method string) error {
	return c.graphql(ctx, `mutation($id: ID!, $m: PullRequestMergeMethod!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: $m}) { clientMutationId } }`,
		map[string]any{"id": nodeID, "m": strings.ToUpper(method)})
}

// Viewer returns the login of the token owner.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

// Repository is the subset of repository fields loop uses.
type Repository struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Permissions   struct {
		Push  bool `json:"push"`
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

// GetRepository fetches the repository, including the token's permissions.
func (c *Client) GetRepository(ctx context.Context) (*Repository, error) {
	var r Repository
	if err := c.do(ctx, http.MethodGet, c.repoPath(""), nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// getText performs a GET that returns a non-JSON body (log downloads).
// GitHub answers with a redirect to a signed URL; the client follows it.
func (c *Client) getText(ctx context.Context, path string) (string, error) {
	resp, err := c.policy().Do(ctx, c.HTTP, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return "", &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	return string(data), nil
}

// JobLogs downloads the log of one GitHub Actions job.
func (c *Client) JobLogs(ctx context.Context, jobID int64) (string, error) {
	return c.getText(ctx, c.repoPath("/actions/jobs/%d/logs", jobID))
}

var jobURLRe = regexp.MustCompile(`/actions/runs/\d+/jobs?/(\d+)`)

// JobID extracts the Actions job id from a check run. Only checks created
// by GitHub Actions have one; for other apps it returns 0.
func (c CheckRun) JobID() int64 {
	for _, u := range []string{c.HTMLURL, c.DetailsURL} {
		if m := jobURLRe.FindStringSubmatch(u); m != nil {
			var id int64
			if _, err := fmt.Sscan(m[1], &id); err == nil {
				return id
			}
		}
	}
	return 0
}

var (
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	timestampRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z\s?`)
)

// TailLog returns the last n lines of an Actions log with the leading
// timestamps and ANSI colour codes removed.
func TailLog(log string, n int) string {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		l = timestampRe.ReplaceAllString(l, "")
		lines[i] = strings.TrimRight(ansiRe.ReplaceAllString(l, ""), " \r")
	}
	return strings.Join(lines, "\n")
}
