package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/gitx"
	"github.com/christoph-jerolimov/loop/internal/host"
	"github.com/christoph-jerolimov/loop/internal/httpx"
	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/prompt"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// loopMarker tags comments loop writes on the PR so they are never fed back.
const loopMarker = "<!-- loop -->"

func (e *Engine) openPR(ctx context.Context, r *state.Run) (bool, error) {
	r.SetPhase(state.PhasePR, "")
	h, err := e.Host()
	if err != nil {
		e.abandon(ctx, r, err)
		return false, err
	}
	if err := e.runSteps(ctx, r, e.Cfg.Steps.BeforePR, "before_pr"); err != nil {
		e.abandon(ctx, r, err)
		return false, err
	}
	e.logf(r, "pushing %s to %s", r.Branch, e.pushRemote())
	if err := gitx.Push(ctx, r.Workdir, e.pushRemote(), r.Branch); err != nil {
		e.abandon(ctx, r, err)
		return false, err
	}
	sha, _ := gitx.HeadSHA(ctx, r.Workdir)
	r.LastPushSHA = sha

	// Reuse an existing PR for the branch (e.g. after a crash).
	pr, err := h.FindPullRequestByHead(ctx, r.Branch)
	if err != nil {
		e.abandon(ctx, r, err)
		return false, err
	}
	if pr == nil {
		d := e.data(r)
		title, err := prompt.Render("pr-title", e.Cfg.PR.Title, d)
		if err != nil {
			e.abandon(ctx, r, err)
			return false, err
		}
		title = strings.TrimSpace(title)
		bodyTpl := e.Cfg.PR.Body
		bodyPath := ""
		if bodyTpl != "" {
			if _, serr := os.Stat(e.Cfg.Resolve(bodyTpl)); serr == nil {
				bodyPath, bodyTpl = e.Cfg.Resolve(bodyTpl), ""
			}
		}
		var body string
		if bodyTpl != "" {
			body, err = prompt.Render("pr-body", bodyTpl, d)
		} else {
			body, err = prompt.RenderFile(prompt.TplPRBody, bodyPath, d)
		}
		if err != nil {
			e.abandon(ctx, r, err)
			return false, err
		}
		if kw := e.closingKeyword(r); kw != "" {
			body += "\n" + kw + "\n"
		}
		body += "\n" + loopMarker + "\n"
		pr, err = h.CreatePullRequest(ctx, title, body, r.Branch, e.Cfg.Repo.Base, *e.Cfg.PR.Draft)
		if err != nil {
			e.abandon(ctx, r, fmt.Errorf("create PR: %w", err))
			return false, err
		}
		e.logf(r, "opened PR %s", pr.URL)
		if len(e.Cfg.PR.Labels) > 0 {
			_ = h.AddLabels(ctx, pr.Number, e.Cfg.PR.Labels)
		}
		if err := h.RequestReviewers(ctx, pr.Number, e.Cfg.PR.Reviewers); err != nil {
			e.logf(r, "request reviewers: %v", err)
		}
		if e.Cfg.Workflow.Merge == config.MergeAuto {
			if err := h.EnableAutoMerge(ctx, pr, e.Cfg.Workflow.MergeMethod); err != nil {
				e.logf(r, "enable auto-merge: %v (is auto-merge allowed in the repository settings?)", err)
			}
		}
	} else {
		e.logf(r, "reusing existing PR %s", pr.URL)
	}
	r.PR = &state.PR{Number: pr.Number, NodeID: pr.ID, URL: pr.URL, HeadSHA: pr.HeadSHA, Draft: pr.Draft}
	if src := e.Sources.ByName(r.Item.Source); src != nil {
		_ = src.Comment(ctx, r.Item, fmt.Sprintf("loop opened pull request %s (run `%s`).", pr.URL, r.ID))
	}
	r.SetPhase(state.PhaseMonitor, "")
	r.NextPoll = time.Now().Add(e.Cfg.Workflow.PollInterval.D())
	return true, nil
}

// closingKeyword links the PR to an issue on the same host so merging
// closes it.
func (e *Engine) closingKeyword(r *state.Run) string {
	// A recurring item outlives every one of its PRs.
	if !*e.Cfg.PR.LinkIssue || r.Item.SourceType != e.Cfg.Repo.Host() || r.Item.Recurring() {
		return ""
	}
	repo := r.Item.Extra["repo"]
	if strings.EqualFold(repo, e.Cfg.Repo.Project()) {
		return "Closes #" + r.Item.NativeID
	}
	return "Closes " + repo + "#" + r.Item.NativeID
}

// ciState summarises checks on a commit.
type ciState struct {
	Pending bool
	Failed  []prompt.Check
	// ciRuns are the CI runs (workflow runs, pipelines) behind the failed
	// checks, the ones loop can ask to re-run.
	ciRuns []int64
}

func (e *Engine) ci(ctx context.Context, h host.Host, sha string) (ciState, error) {
	var st ciState
	checks, err := h.ListChecks(ctx, sha)
	if err != nil {
		return st, err
	}
	required := map[string]bool{}
	for _, n := range e.Cfg.Workflow.RequiredChecks {
		required[n] = true
	}
	for _, c := range checks {
		if c.Name == statusContext || len(required) > 0 && !required[c.Name] {
			continue
		}
		if c.Status != "completed" {
			st.Pending = true
			continue
		}
		if c.Failed() {
			st.Failed = append(st.Failed, prompt.Check{Name: c.Name, Conclusion: c.Conclusion, URL: c.URL, Summary: c.Summary, Text: c.Text, Log: e.jobLog(ctx, h, c)})
			if c.RunID != 0 && !containsID(st.ciRuns, c.RunID) {
				st.ciRuns = append(st.ciRuns, c.RunID)
			}
		}
	}
	return st, nil
}

// jobLog fetches the tail of a failed CI job log. Checks that are not CI
// jobs, disabled log fetching and download errors yield "".
func (e *Engine) jobLog(ctx context.Context, h host.Host, c host.Check) string {
	n := *e.Cfg.Workflow.CILogLines
	if n <= 0 || c.JobID == 0 {
		return ""
	}
	log, err := h.JobLog(ctx, c)
	if err != nil {
		return ""
	}
	return host.TailLog(log, n)
}

// approved reports whether the latest review of every reviewer is an
// approval and at least one exists.
func (e *Engine) approved(reviews []host.Review) bool {
	latest := map[string]host.Review{}
	for _, rv := range reviews {
		if rv.User.Login == e.self || rv.State == "COMMENTED" || rv.State == "PENDING" {
			continue
		}
		if cur, ok := latest[rv.User.Login]; !ok || rv.SubmittedAt.After(cur.SubmittedAt) {
			latest[rv.User.Login] = rv
		}
	}
	any := false
	for _, rv := range latest {
		switch rv.State {
		case "CHANGES_REQUESTED":
			return false
		case "APPROVED":
			any = true
		}
	}
	return any
}

type feedback struct {
	Reviews        []prompt.Review
	ReviewComments []prompt.ReviewComment
	Comments       []host.Comment
	// inline are the review comments as the host returned them, for the
	// replies after the round.
	inline     []host.ReviewComment
	reviewIDs  []int64
	commentIDs []int64
}

func containsID(ids []int64, id int64) bool { return contains(ids, id) }

func contains(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// collectFeedback gathers unhandled reviews and comments since the last push.
func (e *Engine) collectFeedback(ctx context.Context, h host.Host, r *state.Run, since time.Time) (*feedback, error) {
	fb := &feedback{}
	reviews, err := h.ListReviews(ctx, r.PR.Number)
	if err != nil {
		return nil, err
	}
	for _, rv := range reviews {
		if rv.User.Login == e.self || contains(r.HandledReviews, rv.ID) || rv.SubmittedAt.Before(since) {
			continue
		}
		if rv.State != "CHANGES_REQUESTED" && strings.TrimSpace(rv.Body) == "" {
			continue
		}
		if rv.State == "APPROVED" && strings.TrimSpace(rv.Body) == "" {
			continue
		}
		fb.reviewIDs = append(fb.reviewIDs, rv.ID)
		if rv.State == "APPROVED" {
			continue // approvals with a body are not requests
		}
		fb.Reviews = append(fb.Reviews, prompt.Review{Author: rv.User.Login, State: rv.State, Body: rv.Body, URL: rv.URL})
	}
	rcs, err := h.ListReviewComments(ctx, r.PR.Number)
	if err != nil {
		return nil, err
	}
	for _, c := range rcs {
		if c.User.Login == e.self || contains(r.HandledComments, c.ID) || c.CreatedAt.Before(since) {
			continue
		}
		fb.commentIDs = append(fb.commentIDs, c.ID)
		fb.inline = append(fb.inline, c)
		fb.ReviewComments = append(fb.ReviewComments, prompt.ReviewComment{ID: c.ID, Author: c.User.Login, Path: c.Path, Line: c.Line, Body: c.Body, DiffHunk: c.DiffHunk, URL: c.URL})
	}
	ics, err := h.ListComments(ctx, prRef(r))
	if err != nil {
		return nil, err
	}
	for _, c := range ics {
		if c.User.Login == e.self || strings.Contains(c.Body, loopMarker) || contains(r.HandledComments, c.ID) || c.CreatedAt.Before(since) {
			continue
		}
		fb.commentIDs = append(fb.commentIDs, c.ID)
		fb.Comments = append(fb.Comments, c)
	}
	return fb, nil
}

func (fb *feedback) empty() bool {
	return len(fb.Reviews) == 0 && len(fb.ReviewComments) == 0 && len(fb.Comments) == 0
}

// monitor polls the PR once and decides what to do next.
func (e *Engine) monitor(ctx context.Context, r *state.Run) (bool, error) {
	if time.Now().Before(r.NextPoll) {
		return true, nil
	}
	r.NextPoll = time.Now().Add(e.Cfg.Workflow.PollInterval.D())
	h, err := e.Host()
	if err != nil {
		return true, err
	}
	pr, err := h.GetPullRequest(ctx, r.PR.Number)
	if err != nil {
		e.pollFailed(r, err)
		return true, nil
	}
	r.PollFailures = 0
	r.PR.HeadSHA, r.PR.Draft, r.PR.Merged = pr.HeadSHA, pr.Draft, pr.Merged
	if pr.Merged {
		e.okf(r, "PR merged")
		r.SetPhase(state.PhaseClose, "merged")
		return false, nil
	}
	if pr.State == "closed" {
		r.PR.Closed = true
		e.release(ctx, r)
		e.block(ctx, r, "PR was closed without merging")
		return false, nil
	}

	// 1. A human pushed to the branch since loop's last push.
	if r.LastPushSHA != "" && pr.HeadSHA != r.LastPushSHA {
		who := e.foreignPushers(ctx, h, r, r.LastPushSHA)
		r.LastPushSHA = pr.HeadSHA // the new head is loop's baseline from here on
		if len(who) > 0 {
			ffErr := gitx.FastForward(ctx, r.Workdir, e.pushRemote(), r.Branch)
			if e.Cfg.Workflow.OnHumanPush == config.HumanPushPause {
				msg := fmt.Sprintf("%s pushed to %s, so loop stops driving this PR rather than build on top of their work", strings.Join(who, ", "), r.Branch)
				if ffErr != nil {
					msg += fmt.Sprintf(" (the worktree could not be fast-forwarded: %v)", ffErr)
				}
				return e.blockOrWait(ctx, r, "%s", msg)
			}
			if ffErr != nil {
				return e.blockOrWait(ctx, r, "%s pushed to %s and the worktree could not be fast-forwarded: %v", strings.Join(who, ", "), r.Branch, ffErr)
			}
			e.logf(r, "%s pushed to %s; continuing on top of their commits (on_human_push: continue)", strings.Join(who, ", "), r.Branch)
		}
	}

	// 2. Conflicts.
	if pr.Mergeable != nil && !*pr.Mergeable && (pr.MergeableState == "dirty" || pr.MergeableState == "") {
		if r.ConflictRounds >= e.Cfg.Workflow.ConflictAttempts {
			return e.blockOrWait(ctx, r, "merge conflict not resolved after %d attempt(s)", r.ConflictRounds)
		}
		r.PendingFix = state.FixConflict
		return e.toFix(r)
	}

	// 3. Reviews and comments (anything since the run started that was not handled yet).
	fb, err := e.collectFeedback(ctx, h, r, r.Created)
	if err != nil {
		e.pollFailed(r, err)
		return true, nil
	}
	if !fb.empty() {
		if r.FixRounds >= e.Cfg.Workflow.FixRounds {
			return e.blockOrWait(ctx, r, "new review feedback but fix rounds (%d) are used up", e.Cfg.Workflow.FixRounds)
		}
		r.PendingFix = state.FixReview
		return e.toFix(r)
	}

	// 4. CI.
	st, err := e.ci(ctx, h, pr.HeadSHA)
	if err != nil {
		e.pollFailed(r, err)
		return true, nil
	}
	if len(st.Failed) > 0 {
		if r.LastCIFixSHA == pr.HeadSHA {
			return true, nil // already tried this head; wait for humans or new pushes
		}
		if *e.Cfg.Workflow.CIRerun && r.CIRerunSHA != pr.HeadSHA && len(st.ciRuns) > 0 {
			// A flaky job should not cost an agent session: re-run the
			// failed jobs once and look again on the next poll.
			r.CIRerunSHA = pr.HeadSHA
			for _, id := range st.ciRuns {
				if err := h.RerunFailed(ctx, id); err != nil {
					e.logf(r, "re-run failed jobs of CI run %d: %v", id, err)
				}
			}
			e.logf(r, "CI red on %s: re-running the failed jobs once before a fix round", pr.HeadSHA[:min(7, len(pr.HeadSHA))])
			return true, nil
		}
		if r.FixRounds >= e.Cfg.Workflow.FixRounds {
			return e.blockOrWait(ctx, r, "CI red but fix rounds (%d) are used up", e.Cfg.Workflow.FixRounds)
		}
		r.PendingFix = state.FixCI
		return e.toFix(r)
	}
	if st.Pending {
		return true, nil
	}
	// Green from here on.
	if pr.Draft {
		e.logf(r, "CI green, marking PR ready for review")
		if err := h.MarkReadyForReview(ctx, pr); err != nil {
			e.logf(r, "mark ready: %v", err)
		} else {
			r.PR.Draft = false
		}
	}
	switch e.Cfg.Workflow.Merge {
	case config.MergeWhenGreen:
	case config.MergeWhenGreenApprove:
		reviews, err := h.ListReviews(ctx, r.PR.Number)
		if err != nil || !e.approved(reviews) {
			return true, nil
		}
	default:
		return true, nil // manual or the host's auto-merge: keep watching
	}
	if pr.Mergeable != nil && !*pr.Mergeable {
		return true, nil
	}
	r.SetPhase(state.PhaseMerge, "")
	return false, nil
}

// foreignPushers lists the logins (or author names) behind the commits on
// the PR after loop's last push that were not made by loop's own account.
// When that push is not among the commits any more (a force push), every
// commit counts.
func (e *Engine) foreignPushers(ctx context.Context, h host.Host, r *state.Run, since string) []string {
	commits, err := h.ListPullRequestCommits(ctx, r.PR.Number)
	if err != nil {
		e.logf(r, "list PR commits: %v", err)
		return nil
	}
	start := 0
	for i, c := range commits {
		if c.SHA == since {
			start = i + 1
		}
	}
	seen := map[string]bool{}
	var who []string
	for _, c := range commits[start:] {
		login := c.Author
		if login == "" {
			login = c.Committer
		}
		if login == "" || login == e.self || login == "web-flow" || seen[login] {
			continue
		}
		seen[login] = true
		who = append(who, login)
	}
	return who
}

// maxPollBackoff caps how far consecutive failures stretch the next poll.
const maxPollBackoff = 15 * time.Minute

// pollFailed schedules the next poll after an API error: at the rate
// limit's reset time when the server said so, otherwise with exponential
// backoff on the poll interval.
func (e *Engine) pollFailed(r *state.Run, err error) {
	r.PollFailures++
	var rl *httpx.RateLimitError
	if errors.As(err, &rl) {
		r.NextPoll = rl.ResetAt
		e.logf(r, "poll: rate limited, next poll at %s", rl.ResetAt.Format("15:04:05"))
		return
	}
	wait := e.Cfg.Workflow.PollInterval.D() << uint(min(r.PollFailures, 8))
	if wait > maxPollBackoff {
		wait = maxPollBackoff
	}
	r.NextPoll = time.Now().Add(wait)
	e.logf(r, "poll: %v (failure %d, next poll in %s)", err, r.PollFailures, wait.Round(time.Second))
}

func (e *Engine) toFix(r *state.Run) (bool, error) {
	if !e.gate(r, config.GateBeforeFix) {
		return true, nil
	}
	r.SetPhase(state.PhaseFix, string(r.PendingFix))
	return false, nil
}

// blockOrWait leaves a note on the PR once and parks the run as blocked.
func (e *Engine) blockOrWait(ctx context.Context, r *state.Run, format string, a ...any) (bool, error) {
	msg := fmt.Sprintf(format, a...)
	if h, err := e.Host(); err == nil {
		_ = h.CreateComment(ctx, prRef(r), fmt.Sprintf("loop stopped driving this PR: %s. After handling it, a collaborator with push access can comment `%s`, or run `loop resume %s` where loop runs.\n\n%s", msg, cmdResume, r.ID, loopMarker))
	}
	e.block(ctx, r, msg)
	return false, nil
}

// fix runs one fix round for the pending reason, then pushes.
func (e *Engine) fix(ctx context.Context, r *state.Run) (bool, error) {
	h, err := e.Host()
	if err != nil {
		return false, err
	}
	reason := r.PendingFix
	r.PendingFix = ""
	var text string
	switch reason {
	case state.FixConflict:
		r.ConflictRounds++
		e.logf(r, "merging origin/%s into %s", e.Cfg.Repo.Base, r.Branch)
		conflicts, merr := gitx.MergeBase(ctx, r.Workdir, e.Cfg.Repo.Base)
		if merr != nil {
			gitx.AbortMerge(ctx, r.Workdir)
			return e.blockOrWait(ctx, r, "merge failed: %v", merr)
		}
		if len(conflicts) > 0 {
			d := e.data(r)
			d.Conflicts = conflicts
			text, err = prompt.RenderFile(prompt.TplConflict, e.Cfg.Resolve(e.Cfg.Prompts.Conflict), d)
			if err != nil {
				return false, err
			}
			if _, aerr := e.runAgent(ctx, r, "conflict", text, "", 0); aerr != nil {
				gitx.AbortMerge(ctx, r.Workdir)
				return e.blockOrWait(ctx, r, "conflict session failed: %v", aerr)
			}
			if gitx.MergeInProgress(r.Workdir) {
				if cerr := gitx.CommitAll(ctx, r.Workdir, "Merge origin/"+e.Cfg.Repo.Base+" into "+r.Branch); cerr != nil {
					gitx.AbortMerge(ctx, r.Workdir)
					return e.blockOrWait(ctx, r, "merge could not be completed: %v", cerr)
				}
			}
		}
	case state.FixReview, state.FixCI:
		r.FixRounds++
		d := e.data(r)
		d.Round = r.FixRounds
		fb, ferr := e.collectFeedback(ctx, h, r, r.Created)
		if ferr != nil {
			return false, ferr
		}
		for _, c := range fb.Comments {
			d.PRComments = append(d.PRComments, itemComment(c))
		}
		d.Reviews, d.ReviewComments = fb.Reviews, fb.ReviewComments
		tpl, path := prompt.TplReview, e.Cfg.Prompts.Review
		var repliesFile string
		if reason == state.FixReview && len(fb.ReviewComments) > 0 {
			repliesFile = filepath.Join(r.Dir(), fmt.Sprintf("replies-round-%02d.json", r.FixRounds))
			d.RepliesFile = repliesFile
		}
		if reason == state.FixCI {
			st, cerr := e.ci(ctx, h, r.PR.HeadSHA)
			if cerr != nil {
				return false, cerr
			}
			d.Checks = st.Failed
			tpl, path = prompt.TplCI, e.Cfg.Prompts.CI
			r.LastCIFixSHA = r.PR.HeadSHA
		}
		text, err = prompt.RenderFile(tpl, e.Cfg.Resolve(path), d)
		if err != nil {
			return false, err
		}
		r.HandledReviews = append(r.HandledReviews, fb.reviewIDs...)
		r.HandledComments = append(r.HandledComments, fb.commentIDs...)
		extra := map[string]string{}
		if repliesFile != "" {
			extra["LOOP_REPLIES_FILE"] = repliesFile
		}
		if _, aerr := e.runAgent(ctx, r, string(reason), text, "", 0, extra); aerr != nil {
			return e.blockOrWait(ctx, r, "%s fix session failed: %v", reason, aerr)
		}
		defer e.answerReviewers(ctx, h, r, fb.inline, repliesFile)
		if cerr := e.commitLeftovers(ctx, r, "loop: address "+string(reason)+" feedback"); cerr != nil {
			return e.blockOrWait(ctx, r, "%v", cerr)
		}
	default:
		r.SetPhase(state.PhaseMonitor, "nothing to fix")
		return false, nil
	}
	// Whatever the round changed must pass the same checks as the initial
	// session before it is pushed.
	if err := e.runVerify(ctx, r); err != nil {
		return e.blockOrWait(ctx, r, "%s round did not pass verification: %v", reason, err)
	}
	sha, _ := gitx.HeadSHA(ctx, r.Workdir)
	if sha != r.LastPushSHA {
		e.logf(r, "pushing %s round %d", reason, r.FixRounds)
		if err := gitx.Push(ctx, r.Workdir, e.pushRemote(), r.Branch); err != nil {
			return e.blockOrWait(ctx, r, "push failed: %v", err)
		}
		r.LastPushSHA = sha
		if reason != state.FixConflict {
			_ = h.CreateComment(ctx, prRef(r), fmt.Sprintf("Addressed %s feedback in %s (round %d).\n\n%s", reason, sha[:7], r.FixRounds, loopMarker))
		}
	} else {
		e.logf(r, "%s round produced no new commit", reason)
	}
	r.SetPhase(state.PhaseMonitor, "")
	r.NextPoll = time.Now().Add(e.Cfg.Workflow.PollInterval.D())
	return true, nil
}

// maxMergeAttempts bounds how often a merge rejected by the host is
// retried before the run parks with a note.
const maxMergeAttempts = 3

func (e *Engine) merge(ctx context.Context, r *state.Run) (bool, error) {
	r.SetPhase(state.PhaseMerge, "")
	if !e.gate(r, config.GateBeforeMerge) {
		return true, nil
	}
	h, err := e.Host()
	if err != nil {
		return false, err
	}
	// Branch protection can hold a green, approved PR: required reviewers
	// loop cannot satisfy, required checks that never report, or a
	// "require branches to be up to date" rule. The host reports that as
	// a blocked mergeable state; there is nothing loop can do about it, so
	// park the run with one note instead of retrying every poll.
	if pr, perr := h.GetPullRequest(ctx, r.PR.Number); perr == nil && pr.MergeableState == "blocked" {
		return e.blockOrWait(ctx, r, "merge blocked by branch protection (mergeable_state=blocked): it needs reviews, checks or an update loop cannot provide")
	}
	e.logf(r, "merging PR #%d (%s)", r.PR.Number, e.Cfg.Workflow.MergeMethod)
	if err := h.MergePullRequest(ctx, r.PR.Number, e.Cfg.Workflow.MergeMethod); err != nil {
		if code := host.Status(err); code == 405 || code == 406 || code == 409 {
			r.MergeAttempts++
			if r.MergeAttempts >= maxMergeAttempts {
				return e.blockOrWait(ctx, r, "%s rejected the merge %d times (%s)", h.Name(), r.MergeAttempts, firstLine(host.Body(err)))
			}
			e.logf(r, "not mergeable right now (%s); will retry (%d/%d)", firstLine(host.Body(err)), r.MergeAttempts, maxMergeAttempts)
			r.SetPhase(state.PhaseMonitor, "merge rejected")
			r.NextPoll = time.Now().Add(e.Cfg.Workflow.PollInterval.D())
			return true, nil
		}
		return e.blockOrWait(ctx, r, "merge failed: %v", err)
	}
	r.PR.Merged = true
	if err := e.runSteps(ctx, r, e.Cfg.Steps.Merged, "merged"); err != nil {
		e.failf(r, "merged step failed: %v", err)
	}
	r.SetPhase(state.PhaseClose, "")
	return false, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// closeItem closes the ticket after the merge and waits until the source
// reports it closed.
func (e *Engine) closeItem(ctx context.Context, r *state.Run) (bool, error) {
	r.SetPhase(state.PhaseClose, "")
	src := e.Sources.ByName(r.Item.Source)
	if src == nil {
		r.SetPhase(state.PhaseCleanup, "source gone")
		return false, nil
	}
	if r.Item.Recurring() {
		// The item stays open for the next occurrence: only the claim is
		// released, and the ticket learns when that is.
		_ = src.Comment(ctx, r.Item, fmt.Sprintf("Merged %s (loop run `%s`).%s", r.PR.URL, r.ID, e.nextDueNote(r)))
		e.release(ctx, r)
		r.SetPhase(state.PhaseCleanup, "recurring item stays open")
		return false, nil
	}
	cur, err := src.Get(ctx, r.Item.NativeID)
	if err != nil {
		e.logf(r, "reload item: %v", err)
		r.NextPoll = time.Now().Add(e.Cfg.Workflow.PollInterval.D())
		return true, nil
	}
	if !cur.Closed && *e.Cfg.Workflow.CloseIssueOnMerge {
		msg := fmt.Sprintf("Done: merged %s (loop run `%s`).", r.PR.URL, r.ID)
		if err := src.Close(ctx, r.Item, msg); err != nil {
			e.logf(r, "close item: %v", err)
		} else {
			cur.Closed = true
		}
	}
	if !cur.Closed {
		if time.Now().Before(r.NextPoll) {
			return true, nil
		}
		e.logf(r, "PR merged; waiting for %s to be closed", r.Item.ID)
		r.NextPoll = time.Now().Add(e.Cfg.Workflow.PollInterval.D())
		return true, nil
	}
	r.SetPhase(state.PhaseCleanup, "item closed")
	return false, nil
}

func (e *Engine) cleanup(ctx context.Context, r *state.Run) error {
	r.SetPhase(state.PhaseCleanup, "")
	if *e.Cfg.Workflow.Cleanup {
		if err := e.runSteps(ctx, r, e.Cfg.Steps.Cleanup, "cleanup"); err != nil {
			e.failf(r, "cleanup step failed: %v", err)
		}
		e.logf(r, "removing workdir %s", r.Workdir)
		if e.Cfg.Repo.Workdir == "worktree" {
			_ = gitx.RemoveWorktree(ctx, e.baseRepo(), r.Workdir)
			gitx.DeleteLocalBranch(ctx, e.baseRepo(), r.Branch)
		} else {
			_ = os.RemoveAll(r.Workdir)
		}
		if *e.Cfg.Workflow.DeleteBranch && r.PR != nil && r.PR.Merged {
			if h, err := e.Host(); err == nil {
				_ = h.DeleteBranch(ctx, r.Branch)
			}
		}
	}
	if r.Outcome == "" && r.PR != nil && r.PR.Merged {
		r.Outcome = state.OutcomeMerged
	}
	r.SetPhase(state.PhaseDone, string(r.Outcome))
	if r.Outcome == state.OutcomeNoChanges {
		e.okf(r, "done (no changes)")
	} else {
		e.okf(r, "done")
	}
	return nil
}

// release drops the item claim.
func (e *Engine) release(ctx context.Context, r *state.Run) {
	if src := e.Sources.ByName(r.Item.Source); src != nil {
		_ = src.Release(ctx, r.Item)
	}
}

// Resume moves a blocked or failed run back into monitoring (when it has
// a PR) or to the session phase.
func (e *Engine) Resume(r *state.Run) error {
	switch r.Phase {
	case state.PhaseBlocked, state.PhaseFailed:
	default:
		return fmt.Errorf("run %s is %s, nothing to resume", r.ID, r.Phase)
	}
	r.Error = ""
	r.PendingFix = ""
	if r.PR != nil {
		r.SetPhase(state.PhaseMonitor, "resumed")
		r.NextPoll = time.Time{}
	} else if r.Workdir != "" {
		r.Attempt = 0
		r.SetPhase(state.PhaseSession, "resumed")
	} else {
		r.SetPhase(state.PhaseQueued, "resumed")
	}
	return e.Store.Save(r)
}

func itemComment(c host.Comment) item.Comment {
	return item.Comment{Author: c.User.Login, Body: c.Body, Created: c.CreatedAt, URL: c.URL}
}

// reviewReply is one entry of the replies file a review session writes.
type reviewReply struct {
	ID       int64  `json:"id"`
	Reply    string `json:"reply"`
	Resolved bool   `json:"resolved"`
}

// answerReviewers replies in every inline thread the round was given and
// resolves the ones the agent marked done. Comments the agent did not
// report on get a neutral note pointing at the pushed commit, so no thread
// is left without an answer. Failures are logged, never fatal.
func (e *Engine) answerReviewers(ctx context.Context, h host.Host, r *state.Run, comments []host.ReviewComment, repliesFile string) {
	if len(comments) == 0 {
		return
	}
	replies := map[int64]reviewReply{}
	if repliesFile != "" {
		if b, err := os.ReadFile(repliesFile); err == nil {
			var list []reviewReply
			if jerr := json.Unmarshal(b, &list); jerr != nil {
				e.logf(r, "replies file is not valid JSON, answering threads with a generic note: %v", jerr)
			}
			for _, rp := range list {
				replies[rp.ID] = rp
			}
		}
	}
	short := r.LastPushSHA
	if len(short) > 7 {
		short = short[:7]
	}
	for _, c := range comments {
		rp, ok := replies[c.ID]
		body := rp.Reply
		if !ok || strings.TrimSpace(body) == "" {
			body = fmt.Sprintf("Looked at this in fix round %d; see %s. The session left no specific note for this thread.", r.FixRounds, short)
			rp.Resolved = false
		} else if short != "" {
			body += fmt.Sprintf(" (round %d, %s)", r.FixRounds, short)
		}
		if err := h.ReplyToReviewComment(ctx, r.PR.Number, c, body+"\n\n"+loopMarker); err != nil {
			e.logf(r, "reply to comment %d: %v", c.ID, err)
			continue
		}
		if rp.Resolved {
			if err := h.ResolveReviewThread(ctx, r.PR.Number, c); err != nil {
				e.logf(r, "resolve thread of comment %d: %v", c.ID, err)
			}
		}
	}
	e.logf(r, "answered %d review thread(s)", len(comments))
}

// RemoveWorkdir deletes the run's checkout without touching the remote.
func (e *Engine) RemoveWorkdir(ctx context.Context, r *state.Run) error {
	if r.Workdir == "" {
		return nil
	}
	if e.Cfg.Repo.Workdir == "worktree" {
		if err := gitx.RemoveWorktree(ctx, e.baseRepo(), r.Workdir); err != nil {
			return err
		}
		gitx.DeleteLocalBranch(ctx, e.baseRepo(), r.Branch)
	} else if err := os.RemoveAll(r.Workdir); err != nil {
		return err
	}
	if r.Phase.Active() {
		r.Block("workdir removed by loop clean")
		return e.Store.Save(r)
	}
	return nil
}
