package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/christoph-jerolimov/loop/internal/ghapi"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// PR commands a collaborator can post as a comment on the pull request.
const (
	cmdApprove = "/loop approve"
	cmdResume  = "/loop resume"
)

// checkPRCommands looks for "/loop approve" and "/loop resume" comments on
// the run's PR that were written after the run parked, by a collaborator
// with push access. It reacts to each command once and reports whether
// the run may continue. Runs without a PR have nothing to look at.
func (e *Engine) checkPRCommands(ctx context.Context, r *state.Run) (proceed bool) {
	if r.PR == nil || e.Cfg.Workflow.PRCommands == nil || !*e.Cfg.Workflow.PRCommands {
		return false
	}
	if time.Now().Before(r.NextPoll) {
		return false
	}
	r.NextPoll = time.Now().Add(e.Cfg.Workflow.PollInterval.D())
	gh, err := e.GitHub()
	if err != nil {
		return false
	}
	comments, err := gh.ListIssueComments(ctx, r.PR.Number)
	if err != nil {
		e.pollFailed(r, err)
		return false
	}
	r.PollFailures = 0
	since := r.Updated
	if len(r.Events) > 0 {
		since = r.Events[len(r.Events)-1].Time
	}
	for _, c := range comments {
		cmd := strings.ToLower(strings.TrimSpace(strings.SplitN(c.Body, "\n", 2)[0]))
		if !strings.HasPrefix(cmd, "/loop ") || contains(r.HandledComments, c.ID) || c.CreatedAt.Before(since.Add(-time.Minute)) {
			continue
		}
		r.HandledComments = append(r.HandledComments, c.ID)
		perm, perr := gh.CollaboratorPermission(ctx, c.User.Login)
		if perr != nil {
			e.logf(r, "permission of %s: %v", c.User.Login, perr)
			continue
		}
		if !ghapi.CanPush(perm) {
			e.logf(r, "ignoring %q from %s (%s access)", cmd, c.User.Login, perm)
			_ = gh.ReactToComment(ctx, c.ID, "confused")
			continue
		}
		switch {
		case cmd == cmdApprove && r.Gate != "":
			r.GateApproved = r.Gate
			e.logf(r, "gate %s approved by %s via PR comment", r.Gate, c.User.Login)
			_ = gh.ReactToComment(ctx, c.ID, "+1")
			proceed = true
		case cmd == cmdResume && !r.Phase.Active():
			if err := e.Resume(r); err != nil {
				e.logf(r, "resume via PR comment: %v", err)
				continue
			}
			e.logf(r, "resumed by %s via PR comment", c.User.Login)
			_ = gh.ReactToComment(ctx, c.ID, "+1")
			proceed = true
		default:
			e.logf(r, "ignoring %q from %s: nothing to %s in phase %s", cmd, c.User.Login, strings.TrimPrefix(cmd, "/loop "), r.Phase)
			_ = gh.ReactToComment(ctx, c.ID, "confused")
		}
	}
	return proceed
}

// gateNote tells reviewers on the PR how to release a gate.
func (e *Engine) gateNote(ctx context.Context, r *state.Run, gate string) {
	if r.PR == nil {
		return
	}
	gh, err := e.GitHub()
	if err != nil {
		return
	}
	body := fmt.Sprintf("loop is waiting at gate `%s`. A collaborator with push access can continue it by commenting `%s`, or run `loop approve %s` where loop runs.\n\n%s", gate, cmdApprove, r.ID, loopMarker)
	if err := gh.CreateComment(ctx, r.PR.Number, body); err != nil {
		e.logf(r, "gate note: %v", err)
	}
}
