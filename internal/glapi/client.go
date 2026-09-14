// Package glapi is a small GitLab REST API v4 client covering what loop
// needs: issues, notes, labels, merge requests, discussions, approvals,
// pipelines, commit statuses and merging.
package glapi

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
	"strconv"
	"strings"
	"time"

	"github.com/christoph-jerolimov/loop/internal/httpx"
)

// Client talks to one project of one GitLab instance.
type Client struct {
	// BaseURL is the instance, e.g. https://gitlab.com.
	BaseURL string
	// Project is the path with namespace, e.g. group/sub/project.
	Project string
	Token   string
	HTTP    *http.Client
	// Retry governs retries and rate-limit waits; zero means httpx.Default.
	Retry httpx.Policy
}

// New creates a client for the project using the token from GITLAB_TOKEN.
func New(baseURL, project string) (*Client, error) {
	if !strings.Contains(project, "/") {
		return nil, fmt.Errorf("gitlab project must be group/project, got %q", project)
	}
	tok, err := Token()
	if err != nil {
		return nil, err
	}
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Project: project, Token: tok, HTTP: &http.Client{Timeout: 60 * time.Second}, Retry: httpx.Default}, nil
}

// Token resolves the GitLab token.
func Token() (string, error) {
	for _, k := range []string{"GITLAB_TOKEN", "GL_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v, nil
		}
	}
	return "", errors.New("no GitLab token: set GITLAB_TOKEN")
}

// Error is a non-2xx response.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string { return fmt.Sprintf("gitlab: HTTP %d: %s", e.Status, e.Body) }

// HTTPStatus and ResponseBody let callers inspect the error without
// importing this package.
func (e *Error) HTTPStatus() int      { return e.Status }
func (e *Error) ResponseBody() string { return e.Body }

func (c *Client) policy() httpx.Policy {
	if c.Retry.Attempts == 0 {
		return httpx.Default
	}
	return c.Retry
}

func (c *Client) api() string { return c.BaseURL + "/api/v4" }

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		payload = b
	}
	resp, err := c.policy().Do(ctx, c.HTTP, func() (*http.Request, error) {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.api()+path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("PRIVATE-TOKEN", c.Token)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out != nil && len(data) > 0 {
		if s, ok := out.(*string); ok {
			*s = string(data)
			return nil
		}
		return json.Unmarshal(data, out)
	}
	return nil
}

// projectPath builds /projects/:id<format> for this or another project.
func (c *Client) projectPath(format string, a ...any) string {
	return c.pathFor(c.Project, format, a...)
}

func (c *Client) pathFor(project, format string, a ...any) string {
	return fmt.Sprintf("/projects/%s"+format, append([]any{url.PathEscape(project)}, a...)...)
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

func list[T any](c *Client, ctx context.Context, path string, q url.Values) ([]T, error) {
	var out []T
	err := c.paged(ctx, path, q, func(raw json.RawMessage) error {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		out = append(out, v)
		return nil
	})
	return out, err
}

// User is a GitLab account.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	// PublicEmail is set on other users' profiles when they chose to show one.
	PublicEmail string `json:"public_email"`
}

// Myself returns the account behind the token.
func (c *Client) Myself(ctx context.Context) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// FindUser resolves a username to an account; nil when unknown.
func (c *Client) FindUser(ctx context.Context, username string) (*User, error) {
	var users []User
	if err := c.do(ctx, http.MethodGet, "/users?username="+url.QueryEscape(username), nil, &users); err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, nil
	}
	return &users[0], nil
}

// Access levels.
const (
	AccessGuest      = 10
	AccessReporter   = 20
	AccessDeveloper  = 30
	AccessMaintainer = 40
	AccessOwner      = 50
)

// Project is the subset of project fields loop uses.
type Project struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	Visibility        string `json:"visibility"`
	WebURL            string `json:"web_url"`
	Permissions       struct {
		ProjectAccess *struct {
			AccessLevel int `json:"access_level"`
		} `json:"project_access"`
		GroupAccess *struct {
			AccessLevel int `json:"access_level"`
		} `json:"group_access"`
	} `json:"permissions"`
}

// AccessLevel is the token owner's highest access level on the project.
func (p *Project) AccessLevel() int {
	lvl := 0
	if p.Permissions.ProjectAccess != nil {
		lvl = p.Permissions.ProjectAccess.AccessLevel
	}
	if p.Permissions.GroupAccess != nil && p.Permissions.GroupAccess.AccessLevel > lvl {
		lvl = p.Permissions.GroupAccess.AccessLevel
	}
	return lvl
}

// GetProject fetches the project, including the token's permissions.
func (c *Client) GetProject(ctx context.Context) (*Project, error) {
	return c.getProject(ctx, c.Project)
}

func (c *Client) getProject(ctx context.Context, project string) (*Project, error) {
	var p Project
	if err := c.do(ctx, http.MethodGet, c.pathFor(project, ""), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// MemberAccessLevel returns the user's access level on the project,
// including inherited membership; 0 when the user is not a member.
func (c *Client) MemberAccessLevel(ctx context.Context, userID int64) (int, error) {
	var m struct {
		AccessLevel int `json:"access_level"`
	}
	err := c.do(ctx, http.MethodGet, c.projectPath("/members/all/%d", userID), nil, &m)
	var ge *Error
	if errors.As(err, &ge) && ge.Status == 404 {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return m.AccessLevel, nil
}

// Issue is a project issue.
type Issue struct {
	ID          int64     `json:"id"`
	IID         int       `json:"iid"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"` // opened, closed
	WebURL      string    `json:"web_url"`
	Labels      []string  `json:"labels"`
	CreatedAt   time.Time `json:"created_at"`
	Author      User      `json:"author"`
}

// ListIssues returns open issues carrying all labels, oldest first.
func (c *Client) ListIssues(ctx context.Context, labels []string) ([]Issue, error) {
	q := url.Values{"state": {"opened"}, "order_by": {"created_at"}, "sort": {"asc"}, "scope": {"all"}}
	if len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	return list[Issue](c, ctx, c.projectPath("/issues"), q)
}

// GetIssue fetches one issue by its iid.
func (c *Client) GetIssue(ctx context.Context, iid int) (*Issue, error) {
	var is Issue
	if err := c.do(ctx, http.MethodGet, c.projectPath("/issues/%d", iid), nil, &is); err != nil {
		return nil, err
	}
	return &is, nil
}

// Note is a comment on an issue or merge request.
type Note struct {
	ID         int64     `json:"id"`
	Body       string    `json:"body"`
	Author     User      `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
	System     bool      `json:"system"`
	Type       string    `json:"type"` // DiffNote, DiscussionNote or ""
	Resolvable bool      `json:"resolvable"`
	Resolved   bool      `json:"resolved"`
	Position   *struct {
		NewPath string `json:"new_path"`
		OldPath string `json:"old_path"`
		NewLine int    `json:"new_line"`
		OldLine int    `json:"old_line"`
	} `json:"position"`
}

// Path and Line locate an inline note in the diff.
func (n Note) Path() string {
	if n.Position == nil {
		return ""
	}
	if n.Position.NewPath != "" {
		return n.Position.NewPath
	}
	return n.Position.OldPath
}

func (n Note) Line() int {
	if n.Position == nil {
		return 0
	}
	if n.Position.NewLine != 0 {
		return n.Position.NewLine
	}
	return n.Position.OldLine
}

// Noteable is the kind of thing notes hang off.
type Noteable string

const (
	Issues        Noteable = "issues"
	MergeRequests Noteable = "merge_requests"
)

// ListNotes returns the human notes on an issue or merge request, oldest
// first.
func (c *Client) ListNotes(ctx context.Context, kind Noteable, iid int) ([]Note, error) {
	q := url.Values{"order_by": {"created_at"}, "sort": {"asc"}}
	notes, err := list[Note](c, ctx, c.projectPath("/%s/%d/notes", kind, iid), q)
	if err != nil {
		return nil, err
	}
	out := notes[:0]
	for _, n := range notes {
		if !n.System {
			out = append(out, n)
		}
	}
	return out, nil
}

// CreateNote posts a comment.
func (c *Client) CreateNote(ctx context.Context, kind Noteable, iid int, body string) error {
	return c.do(ctx, http.MethodPost, c.projectPath("/%s/%d/notes", kind, iid), map[string]string{"body": body}, nil)
}

// AwardEmoji reacts to a note with an emoji name (thumbsup, confused, ...).
func (c *Client) AwardEmoji(ctx context.Context, kind Noteable, iid int, noteID int64, name string) error {
	return c.do(ctx, http.MethodPost, c.projectPath("/%s/%d/notes/%d/award_emoji", kind, iid, noteID), map[string]string{"name": name}, nil)
}

// AddLabels adds labels to an issue.
func (c *Client) AddLabels(ctx context.Context, iid int, labels []string) error {
	return c.do(ctx, http.MethodPut, c.projectPath("/issues/%d", iid), map[string]string{"add_labels": strings.Join(labels, ",")}, nil)
}

// RemoveLabel removes one label; a missing label is not an error.
func (c *Client) RemoveLabel(ctx context.Context, iid int, label string) error {
	return c.do(ctx, http.MethodPut, c.projectPath("/issues/%d", iid), map[string]string{"remove_labels": label}, nil)
}

// CloseIssue closes an issue.
func (c *Client) CloseIssue(ctx context.Context, iid int) error {
	return c.do(ctx, http.MethodPut, c.projectPath("/issues/%d", iid), map[string]string{"state_event": "close"}, nil)
}

// MergeRequest is the subset of merge request fields loop uses.
type MergeRequest struct {
	ID                  int64  `json:"id"`
	IID                 int    `json:"iid"`
	Title               string `json:"title"`
	Description         string `json:"description"`
	State               string `json:"state"` // opened, closed, locked, merged
	Draft               bool   `json:"draft"`
	SHA                 string `json:"sha"`
	SourceBranch        string `json:"source_branch"`
	TargetBranch        string `json:"target_branch"`
	WebURL              string `json:"web_url"`
	HasConflicts        bool   `json:"has_conflicts"`
	DetailedMergeStatus string `json:"detailed_merge_status"`
}

// CreateMergeRequest opens a merge request from branch into base. When
// sourceProject is set (a fork), the request is created there and targets
// this project.
func (c *Client) CreateMergeRequest(ctx context.Context, title, body, branch, base string, sourceProject string) (*MergeRequest, error) {
	in := map[string]any{"source_branch": branch, "target_branch": base, "title": title, "description": body}
	path := c.projectPath("/merge_requests")
	if sourceProject != "" {
		p, err := c.getProject(ctx, c.Project)
		if err != nil {
			return nil, err
		}
		in["target_project_id"] = p.ID
		path = c.pathFor(sourceProject, "/merge_requests")
	}
	var mr MergeRequest
	if err := c.do(ctx, http.MethodPost, path, in, &mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

// FindMergeRequest returns the open merge request for a source branch,
// if any. sourceProject narrows the search to requests from a fork.
func (c *Client) FindMergeRequest(ctx context.Context, branch, sourceProject string) (*MergeRequest, error) {
	q := url.Values{"state": {"opened"}, "source_branch": {branch}}
	if sourceProject != "" {
		p, err := c.getProject(ctx, sourceProject)
		if err != nil {
			return nil, err
		}
		q.Set("source_project_id", strconv.FormatInt(p.ID, 10))
	}
	var mrs []MergeRequest
	if err := c.do(ctx, http.MethodGet, c.projectPath("/merge_requests")+"?"+q.Encode(), nil, &mrs); err != nil {
		return nil, err
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	return &mrs[0], nil
}

// GetMergeRequest fetches a merge request.
func (c *Client) GetMergeRequest(ctx context.Context, iid int) (*MergeRequest, error) {
	var mr MergeRequest
	if err := c.do(ctx, http.MethodGet, c.projectPath("/merge_requests/%d", iid), nil, &mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

// UpdateMergeRequest changes attributes: title, add_labels, reviewer_ids.
func (c *Client) UpdateMergeRequest(ctx context.Context, iid int, attrs map[string]any) error {
	return c.do(ctx, http.MethodPut, c.projectPath("/merge_requests/%d", iid), attrs, nil)
}

// Commit is one commit of a merge request.
type Commit struct {
	ID            string `json:"id"`
	AuthorName    string `json:"author_name"`
	AuthorEmail   string `json:"author_email"`
	CommitterName string `json:"committer_name"`
}

// ListMergeRequestCommits returns the commits, oldest first.
func (c *Client) ListMergeRequestCommits(ctx context.Context, iid int) ([]Commit, error) {
	commits, err := list[Commit](c, ctx, c.projectPath("/merge_requests/%d/commits", iid), nil)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}

// Approvals returns who approved the merge request.
func (c *Client) Approvals(ctx context.Context, iid int) ([]User, error) {
	var resp struct {
		ApprovedBy []struct {
			User User `json:"user"`
		} `json:"approved_by"`
	}
	if err := c.do(ctx, http.MethodGet, c.projectPath("/merge_requests/%d/approvals", iid), nil, &resp); err != nil {
		return nil, err
	}
	var out []User
	for _, a := range resp.ApprovedBy {
		out = append(out, a.User)
	}
	return out, nil
}

// Reviewer is a requested reviewer and their review state.
type Reviewer struct {
	User  User   `json:"user"`
	State string `json:"state"` // unreviewed, reviewed, requested_changes, approved
}

// Reviewers returns the reviewers of a merge request with their state.
func (c *Client) Reviewers(ctx context.Context, iid int) ([]Reviewer, error) {
	var out []Reviewer
	if err := c.do(ctx, http.MethodGet, c.projectPath("/merge_requests/%d/reviewers", iid), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Discussion is a thread of notes on a merge request.
type Discussion struct {
	ID             string `json:"id"`
	IndividualNote bool   `json:"individual_note"`
	Notes          []Note `json:"notes"`
}

// ListDiscussions returns every discussion of a merge request.
func (c *Client) ListDiscussions(ctx context.Context, iid int) ([]Discussion, error) {
	return list[Discussion](c, ctx, c.projectPath("/merge_requests/%d/discussions", iid), nil)
}

// ReplyToDiscussion adds a note to a discussion.
func (c *Client) ReplyToDiscussion(ctx context.Context, iid int, discussionID, body string) error {
	return c.do(ctx, http.MethodPost, c.projectPath("/merge_requests/%d/discussions/%s/notes", iid, discussionID), map[string]string{"body": body}, nil)
}

// ResolveDiscussion marks a discussion resolved.
func (c *Client) ResolveDiscussion(ctx context.Context, iid int, discussionID string) error {
	return c.do(ctx, http.MethodPut, c.projectPath("/merge_requests/%d/discussions/%s", iid, discussionID), map[string]bool{"resolved": true}, nil)
}

// CommitStatus is one CI job or external status on a commit.
type CommitStatus struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"` // created, pending, running, success, failed, canceled, skipped, manual
	TargetURL    string `json:"target_url"`
	Description  string `json:"description"`
	AllowFailure bool   `json:"allow_failure"`
	PipelineID   int64  `json:"pipeline_id"`
}

// ListCommitStatuses returns the latest status of every job and external
// check on a commit.
func (c *Client) ListCommitStatuses(ctx context.Context, sha string) ([]CommitStatus, error) {
	return list[CommitStatus](c, ctx, c.projectPath("/repository/commits/%s/statuses", sha), nil)
}

// SetCommitStatus posts an external status (pending, running, success,
// failed, canceled) named name on a commit.
func (c *Client) SetCommitStatus(ctx context.Context, sha, name, state, description, targetURL string) error {
	if len(description) > 255 {
		description = description[:252] + "..."
	}
	in := map[string]string{"state": state, "name": name, "description": description}
	if targetURL != "" {
		in["target_url"] = targetURL
	}
	return c.do(ctx, http.MethodPost, c.projectPath("/statuses/%s", sha), in, nil)
}

// JobTrace downloads the log of a CI job.
func (c *Client) JobTrace(ctx context.Context, jobID int64) (string, error) {
	var out string
	if err := c.do(ctx, http.MethodGet, c.projectPath("/jobs/%d/trace", jobID), nil, &out); err != nil {
		return "", err
	}
	return out, nil
}

// RetryPipeline re-runs the failed jobs of a pipeline.
func (c *Client) RetryPipeline(ctx context.Context, pipelineID int64) error {
	return c.do(ctx, http.MethodPost, c.projectPath("/pipelines/%d/retry", pipelineID), nil, nil)
}

// Merge merges the request; squash controls squashing. autoMerge merges
// once the pipeline succeeds instead of now.
func (c *Client) Merge(ctx context.Context, iid int, squash, autoMerge bool) error {
	in := map[string]any{"squash": squash, "should_remove_source_branch": false}
	if autoMerge {
		in["merge_when_pipeline_succeeds"] = true
	}
	return c.do(ctx, http.MethodPut, c.projectPath("/merge_requests/%d/merge", iid), in, nil)
}

// DeleteBranch removes a branch of the project; a missing one is not an
// error.
func (c *Client) DeleteBranch(ctx context.Context, project, branch string) error {
	err := c.do(ctx, http.MethodDelete, c.pathFor(project, "/repository/branches/%s", url.PathEscape(branch)), nil, nil)
	var ge *Error
	if errors.As(err, &ge) && ge.Status == 404 {
		return nil
	}
	return err
}

// BrowseURL is the web address of an issue or merge request.
func (c *Client) BrowseURL(kind Noteable, iid int) string {
	return fmt.Sprintf("%s/%s/-/%s/%d", c.BaseURL, c.Project, kind, iid)
}
