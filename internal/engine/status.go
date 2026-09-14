package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/christoph-jerolimov/loop/internal/state"
)

// statusContext is the name of the commit status loop posts on PR heads.
const statusContext = "loop"

// reportStatus posts the run's phase and outcome as a commit status on
// the PR head, once per change, so reviewers see on the host what loop is
// doing without running loop status. Failures are logged, never fatal.
func (e *Engine) reportStatus(ctx context.Context, r *state.Run) {
	if e.Cfg.Workflow.PRStatus == nil || !*e.Cfg.Workflow.PRStatus || r.PR == nil || r.PR.HeadSHA == "" {
		return
	}
	st, desc := e.statusOf(r)
	key := r.PR.HeadSHA + " " + st + " " + desc
	if key == r.LastStatus {
		return
	}
	h, err := e.Host()
	if err != nil {
		return
	}
	if err := h.SetStatus(ctx, r.PR.HeadSHA, statusContext, st, desc, r.Item.URL); err != nil {
		e.logf(r, "pr status: %v", err)
		return
	}
	r.LastStatus = key
}

// statusOf maps the run to a commit status state and a short description.
func (e *Engine) statusOf(r *state.Run) (string, string) {
	rounds := fmt.Sprintf("fix rounds %d/%d", r.FixRounds, e.Cfg.Workflow.FixRounds)
	switch r.Phase {
	case state.PhaseDone:
		return "success", "merged and ticket closed; " + rounds
	case state.PhaseFailed:
		return "failure", "failed: " + firstLine(r.Error)
	case state.PhaseBlocked:
		return "error", "blocked: " + firstLine(r.Error)
	case state.PhaseFix:
		// The round about to run: fix() counts it when it starts.
		if r.PendingFix == state.FixConflict {
			return "pending", fmt.Sprintf("conflict round %d/%d", r.ConflictRounds+1, e.Cfg.Workflow.ConflictAttempts)
		}
		return "pending", fmt.Sprintf("fix round %d/%d (%s)", r.FixRounds+1, e.Cfg.Workflow.FixRounds, r.PendingFix)
	case state.PhaseMerge:
		return "pending", "merging; " + rounds
	case state.PhaseClose, state.PhaseCleanup:
		return "pending", "merged; closing the ticket; " + rounds
	}
	desc := "monitoring; " + rounds
	if r.Gate != "" {
		desc = "waiting at gate " + r.Gate + "; " + rounds
	}
	return "pending", strings.TrimSpace(desc)
}
