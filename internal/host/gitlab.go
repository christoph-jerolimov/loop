package host

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/glapi"
)

// gitLab adapts glapi.Client to the Host interface. Merge requests live in
// the target project even when they come from a fork, so one client for
// the repository serves everything; only creation, the search by branch
// and branch deletion need the fork's project path.
type gitLab struct {
	api  *glapi.Client
	fork string

	once  sync.Once
	me    *glapi.User
	meErr error
}

func newGitLab(r config.Repo, project string) (Host, error) {
	api, err := glapi.New(r.GitLabURL, project)
	if err != nil {
		return nil, err
	}
	h := &gitLab{api: api}
	if project == r.Project() {
		h.fork = r.Fork
	}
	return h, nil
}

func (h *gitLab) Name() string { return config.HostGitLab }

func (h *gitLab) myself(ctx context.Context) (*glapi.User, error) {
	h.once.Do(func() { h.me, h.meErr = h.api.Myself(ctx) })
	return h.me, h.meErr
}

func (h *gitLab) Viewer(ctx context.Context) (string, error) {
	u, err := h.myself(ctx)
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func (h *gitLab) GetRepository(ctx context.Context) (*Repository, error) {
	p, err := h.api.GetProject(ctx)
	if err != nil {
		return nil, err
	}
	lvl := p.AccessLevel()
	return &Repository{FullName: p.PathWithNamespace, DefaultBranch: p.DefaultBranch, Private: p.Visibility != "public", CanPush: lvl >= glapi.AccessDeveloper, Admin: lvl >= glapi.AccessMaintainer}, nil
}

func convertMR(mr *glapi.MergeRequest) *PullRequest {
	if mr == nil {
		return nil
	}
	pr := &PullRequest{Number: mr.IID, ID: fmt.Sprint(mr.ID), Title: mr.Title, State: "open", Draft: mr.Draft, Merged: mr.State == "merged",
		URL: mr.WebURL, HeadSHA: mr.SHA, HeadRef: mr.SourceBranch}
	if mr.State == "closed" || mr.State == "merged" {
		pr.State = "closed"
	}
	switch mr.DetailedMergeStatus {
	case "mergeable":
		pr.MergeableState = "clean"
	case "conflict":
		pr.MergeableState = "dirty"
	case "not_approved", "blocked_status", "discussions_not_resolved", "policies_denied", "external_status_checks", "need_rebase", "jira_association_missing", "requested_changes", "not_open", "approvals_syncing":
		pr.MergeableState = "blocked"
	}
	if mr.HasConflicts {
		pr.MergeableState = "dirty"
	}
	if mr.DetailedMergeStatus != "" && mr.DetailedMergeStatus != "unchecked" && mr.DetailedMergeStatus != "checking" && mr.DetailedMergeStatus != "preparing" {
		m := !mr.HasConflicts
		pr.Mergeable = &m
	}
	return pr
}

func (h *gitLab) FindPullRequestByHead(ctx context.Context, branch string) (*PullRequest, error) {
	mr, err := h.api.FindMergeRequest(ctx, branch, h.fork)
	if err != nil {
		return nil, err
	}
	return convertMR(mr), nil
}

func (h *gitLab) CreatePullRequest(ctx context.Context, title, body, branch, base string, draft bool) (*PullRequest, error) {
	mr, err := h.api.CreateMergeRequest(ctx, draftTitle(title, draft), body, branch, base, h.fork)
	if err != nil {
		return nil, err
	}
	return convertMR(mr), nil
}

func (h *gitLab) GetPullRequest(ctx context.Context, n int) (*PullRequest, error) {
	mr, err := h.api.GetMergeRequest(ctx, n)
	if err != nil {
		return nil, err
	}
	return convertMR(mr), nil
}

func (h *gitLab) AddLabels(ctx context.Context, n int, labels []string) error {
	return h.api.UpdateMergeRequest(ctx, n, map[string]any{"add_labels": strings.Join(labels, ",")})
}

func (h *gitLab) RequestReviewers(ctx context.Context, n int, users []string) error {
	if len(users) == 0 {
		return nil
	}
	var ids []int64
	for _, name := range users {
		u, err := h.api.FindUser(ctx, name)
		if err != nil {
			return err
		}
		if u == nil {
			return fmt.Errorf("gitlab: no user %q", name)
		}
		ids = append(ids, u.ID)
	}
	return h.api.UpdateMergeRequest(ctx, n, map[string]any{"reviewer_ids": ids})
}

func (h *gitLab) EnableAutoMerge(ctx context.Context, pr *PullRequest, method string) error {
	return h.api.Merge(ctx, pr.Number, method == "squash", true)
}

func (h *gitLab) MarkReadyForReview(ctx context.Context, pr *PullRequest) error {
	title := pr.Title
	for _, p := range []string{"Draft: ", "Draft:", "WIP: ", "[Draft] ", "(Draft) "} {
		title = strings.TrimPrefix(title, p)
	}
	return h.api.UpdateMergeRequest(ctx, pr.Number, map[string]any{"title": strings.TrimSpace(title)})
}

// ListPullRequestCommits maps commit authors to loop's own username when
// the name or email is loop's; GitLab does not link commits to accounts.
func (h *gitLab) ListPullRequestCommits(ctx context.Context, n int) ([]Commit, error) {
	commits, err := h.api.ListMergeRequestCommits(ctx, n)
	if err != nil {
		return nil, err
	}
	me, _ := h.myself(ctx)
	out := make([]Commit, 0, len(commits))
	for _, c := range commits {
		author, committer := c.AuthorName, c.CommitterName
		if me != nil && (c.AuthorEmail != "" && (c.AuthorEmail == me.Email || c.AuthorEmail == me.PublicEmail) || c.AuthorName == me.Name || c.AuthorName == me.Username) {
			author = me.Username
		}
		out = append(out, Commit{SHA: c.ID, Author: author, Committer: committer})
	}
	return out, nil
}

func (h *gitLab) MergePullRequest(ctx context.Context, n int, method string) error {
	return h.api.Merge(ctx, n, method == "squash", false)
}

func (h *gitLab) DeleteBranch(ctx context.Context, branch string) error {
	project := h.api.Project
	if h.fork != "" {
		project = h.fork
	}
	return h.api.DeleteBranch(ctx, project, branch)
}

// ListChecks maps pipeline jobs and external statuses to checks.
func (h *gitLab) ListChecks(ctx context.Context, sha string) ([]Check, error) {
	statuses, err := h.api.ListCommitStatuses(ctx, sha)
	if err != nil {
		return nil, err
	}
	out := make([]Check, 0, len(statuses))
	for _, s := range statuses {
		ck := Check{Name: s.Name, URL: s.TargetURL, Summary: s.Description, JobID: s.ID, RunID: s.PipelineID}
		switch s.Status {
		case "success":
			ck.Status, ck.Conclusion = "completed", "success"
		case "failed":
			ck.Status, ck.Conclusion = "completed", "failure"
			if s.AllowFailure {
				ck.Conclusion = "neutral"
			}
		case "canceled":
			ck.Status, ck.Conclusion = "completed", "cancelled"
		case "skipped", "manual":
			ck.Status, ck.Conclusion = "completed", "skipped"
		default: // created, pending, running, waiting_for_resource, preparing, scheduled
			ck.Status = "in_progress"
		}
		out = append(out, ck)
	}
	return out, nil
}

func (h *gitLab) JobLog(ctx context.Context, c Check) (string, error) {
	if c.JobID == 0 {
		return "", nil
	}
	return h.api.JobTrace(ctx, c.JobID)
}

func (h *gitLab) RerunFailed(ctx context.Context, runID int64) error {
	return h.api.RetryPipeline(ctx, runID)
}

func (h *gitLab) SetStatus(ctx context.Context, sha, name, state, description, targetURL string) error {
	switch state {
	case "failure", "error":
		state = "failed"
	case "success":
	default:
		state = "pending"
	}
	return h.api.SetCommitStatus(ctx, sha, name, state, description, targetURL)
}

// ListReviews builds reviews from approvals and reviewers who requested
// changes. GitLab records no time for either, so approvals are dated at
// the beginning of time (they carry no request for the agent) and change
// requests as new, which lets the next fix round pick them up once.
func (h *gitLab) ListReviews(ctx context.Context, n int) ([]Review, error) {
	approvers, err := h.api.Approvals(ctx, n)
	if err != nil {
		return nil, err
	}
	var out []Review
	for _, u := range approvers {
		out = append(out, Review{ID: u.ID, User: User{Login: u.Username, ID: u.ID}, State: "APPROVED"})
	}
	reviewers, err := h.api.Reviewers(ctx, n)
	if err != nil {
		return out, nil // the endpoint is newer than some instances; approvals are enough
	}
	for _, r := range reviewers {
		if r.State == "requested_changes" {
			out = append(out, Review{ID: -r.User.ID, User: User{Login: r.User.Username, ID: r.User.ID}, State: "CHANGES_REQUESTED", SubmittedAt: now()})
		}
	}
	return out, nil
}

func (h *gitLab) noteURL(kind glapi.Noteable, iid int, noteID int64) string {
	return fmt.Sprintf("%s#note_%d", h.api.BrowseURL(kind, iid), noteID)
}

// ListReviewComments returns the notes with a position in the diff, each
// tagged with its discussion.
func (h *gitLab) ListReviewComments(ctx context.Context, n int) ([]ReviewComment, error) {
	discussions, err := h.api.ListDiscussions(ctx, n)
	if err != nil {
		return nil, err
	}
	var out []ReviewComment
	for _, d := range discussions {
		for _, note := range d.Notes {
			if note.System || note.Position == nil {
				continue
			}
			out = append(out, ReviewComment{ID: note.ID, ThreadID: d.ID, User: User{Login: note.Author.Username, ID: note.Author.ID}, Body: note.Body,
				Path: note.Path(), Line: note.Line(), URL: h.noteURL(glapi.MergeRequests, n, note.ID), CreatedAt: note.CreatedAt, Resolved: note.Resolved})
		}
	}
	return out, nil
}

func (h *gitLab) ReplyToReviewComment(ctx context.Context, n int, c ReviewComment, body string) error {
	return h.api.ReplyToDiscussion(ctx, n, c.ThreadID, body)
}

func (h *gitLab) ResolveReviewThread(ctx context.Context, n int, c ReviewComment) error {
	if c.Resolved {
		return nil
	}
	return h.api.ResolveDiscussion(ctx, n, c.ThreadID)
}

func kindOf(ref Ref) glapi.Noteable {
	if ref.PR {
		return glapi.MergeRequests
	}
	return glapi.Issues
}

// ListComments returns the conversation notes: everything without a diff
// position.
func (h *gitLab) ListComments(ctx context.Context, ref Ref) ([]Comment, error) {
	notes, err := h.api.ListNotes(ctx, kindOf(ref), ref.Number)
	if err != nil {
		return nil, err
	}
	var out []Comment
	for _, note := range notes {
		if note.Position != nil {
			continue
		}
		out = append(out, Comment{ID: note.ID, Ref: ref, User: User{Login: note.Author.Username, ID: note.Author.ID}, Body: note.Body, URL: h.noteURL(kindOf(ref), ref.Number, note.ID), CreatedAt: note.CreatedAt})
	}
	return out, nil
}

func (h *gitLab) CreateComment(ctx context.Context, ref Ref, body string) error {
	return h.api.CreateNote(ctx, kindOf(ref), ref.Number, body)
}

func (h *gitLab) React(ctx context.Context, c Comment, content string) error {
	name := map[string]string{"+1": "thumbsup", "-1": "thumbsdown", "confused": "confused", "eyes": "eyes"}[content]
	if name == "" {
		name = content
	}
	return h.api.AwardEmoji(ctx, kindOf(c.Ref), c.Ref.Number, c.ID, name)
}

// Permission maps the user's access level to admin, write, read or none.
func (h *gitLab) Permission(ctx context.Context, u User) (string, error) {
	if u.ID == 0 {
		found, err := h.api.FindUser(ctx, u.Login)
		if err != nil {
			return "", err
		}
		if found == nil {
			return "none", nil
		}
		u.ID = found.ID
	}
	lvl, err := h.api.MemberAccessLevel(ctx, u.ID)
	if err != nil {
		return "", err
	}
	return AccessName(lvl), nil
}

// AccessName maps a GitLab access level to loop's permission names.
func AccessName(level int) string {
	switch {
	case level >= glapi.AccessMaintainer:
		return "admin"
	case level >= glapi.AccessDeveloper:
		return "write"
	case level >= glapi.AccessGuest:
		return "read"
	}
	return "none"
}
