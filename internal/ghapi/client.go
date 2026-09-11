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
	"strings"
	"time"
)

// Client talks to one repository.
type Client struct {
	Owner, Repo string
	Token       string
	BaseURL     string
	HTTP        *http.Client
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
	return &Client{Owner: owner, Repo: repo, Token: tok, BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 60 * time.Second}}, nil
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

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	u := path
	if !strings.HasPrefix(path, "http") {
		u = c.BaseURL + path
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
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

// FindPullRequestByHead returns the open PR for a branch, if any.
func (c *Client) FindPullRequestByHead(ctx context.Context, branch string) (*PullRequest, error) {
	q := url.Values{"state": {"open"}, "head": {c.Owner + ":" + branch}}
	var prs []PullRequest
	if err := c.do(ctx, http.MethodGet, c.repoPath("/pulls")+"?"+q.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
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
	base := strings.TrimSuffix(c.BaseURL, "/api/v3")
	endpoint := base + "/graphql"
	if base == "https://api.github.com" {
		endpoint = "https://api.github.com/graphql"
	}
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
