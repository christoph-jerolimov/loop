package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/host"
	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// PR commands a collaborator can post as a comment on the pull request.
const (
	cmdApprove = "/loop approve"
	cmdResume  = "/loop resume"
)

// commandIssue is the issue or PR whose comments carry commands for the
// run: the PR once it exists, before that the ticket itself when it is an
// issue of the repository on its host. A zero number means there is
// nothing to read.
func (e *Engine) commandIssue(r *state.Run) host.Ref {
	if r.PR != nil {
		return prRef(r)
	}
	if r.Item != nil && e.hostIssue(r.Item) {
		if n, err := strconv.Atoi(r.Item.NativeID); err == nil {
			return host.Ref{Number: n}
		}
	}
	return host.Ref{}
}

// hostIssue reports whether the item is an issue of the target repository
// itself, on the same host.
func (e *Engine) hostIssue(it *item.Item) bool {
	return it.SourceType == e.Cfg.Repo.Host() && strings.EqualFold(it.Extra["repo"], e.Cfg.Repo.Project())
}

// checkPRCommands looks for "/loop approve" and "/loop resume" comments on
// the run's PR (or, before a PR exists, on its GitHub issue) that were
// written after the run parked, by a collaborator with push access. It
// reacts to each command once and reports whether the run may continue.
func (e *Engine) checkPRCommands(ctx context.Context, r *state.Run) (proceed bool) {
	ref := e.commandIssue(r)
	if ref.Number == 0 || e.Cfg.Workflow.PRCommands == nil || !*e.Cfg.Workflow.PRCommands {
		return false
	}
	if time.Now().Before(r.NextPoll) {
		return false
	}
	r.NextPoll = time.Now().Add(e.Cfg.Workflow.PollInterval.D())
	h, err := e.Host()
	if err != nil {
		return false
	}
	comments, err := h.ListComments(ctx, ref)
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
		perm, perr := h.Permission(ctx, c.User)
		if perr != nil {
			e.logf(r, "permission of %s: %v", c.User.Login, perr)
			continue
		}
		if !host.CanPush(perm) {
			e.logf(r, "ignoring %q from %s (%s access)", cmd, c.User.Login, perm)
			_ = h.React(ctx, c, "confused")
			continue
		}
		switch {
		case cmd == cmdApprove && r.Gate != "":
			r.GateApproved = r.Gate
			e.logf(r, "gate %s approved by %s via PR comment", r.Gate, c.User.Login)
			_ = h.React(ctx, c, "+1")
			proceed = true
		case cmd == cmdResume && !r.Phase.Active():
			if err := e.Resume(r); err != nil {
				e.logf(r, "resume via PR comment: %v", err)
				continue
			}
			e.logf(r, "resumed by %s via PR comment", c.User.Login)
			_ = h.React(ctx, c, "+1")
			proceed = true
		default:
			e.logf(r, "ignoring %q from %s: nothing to %s in phase %s", cmd, c.User.Login, strings.TrimPrefix(cmd, "/loop "), r.Phase)
			_ = h.React(ctx, c, "confused")
		}
	}
	return proceed
}

// gateNote tells people on the PR, or on the ticket before a PR exists,
// how to release a gate.
func (e *Engine) gateNote(ctx context.Context, r *state.Run, gate string) {
	if r.PR != nil {
		h, err := e.Host()
		if err != nil {
			return
		}
		body := fmt.Sprintf("loop is waiting at gate `%s`. A collaborator with push access can continue it by commenting `%s`, or run `loop approve %s` where loop runs.\n\n%s", gate, cmdApprove, r.ID, loopMarker)
		if err := h.CreateComment(ctx, prRef(r), body); err != nil {
			e.logf(r, "gate note: %v", err)
		}
		return
	}
	if gate == config.GateBeforeCode && e.Cfg.Workflow.Plan {
		return // the plan comment already says how to continue
	}
	src := e.Sources.ByName(r.Item.Source)
	if src == nil {
		return
	}
	body := fmt.Sprintf("loop is waiting at gate `%s` for run `%s`. Continue it with `loop approve %s` where loop runs%s.", gate, r.ID, r.ID, e.approveHint(r))
	if err := src.Comment(ctx, r.Item, body); err != nil {
		e.logf(r, "gate note: %v", err)
	}
}
