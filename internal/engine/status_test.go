package engine

import (
	"strings"
	"testing"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/state"
)

func TestStatusOf(t *testing.T) {
	lc := newLifecycle(t, nil)
	e := lc.eng
	cases := []struct {
		run        state.Run
		state, has string
	}{
		{state.Run{Phase: state.PhaseMonitor}, "pending", "monitoring; fix rounds 0/3"},
		{state.Run{Phase: state.PhaseMonitor, Gate: "before-merge", FixRounds: 1}, "pending", "waiting at gate before-merge; fix rounds 1/3"},
		{state.Run{Phase: state.PhaseFix, FixRounds: 1, PendingFix: state.FixReview}, "pending", "fix round 2/3 (review)"},
		{state.Run{Phase: state.PhaseFix, PendingFix: state.FixConflict}, "pending", "conflict round 1/1"},
		{state.Run{Phase: state.PhaseMerge}, "pending", "merging"},
		{state.Run{Phase: state.PhaseClose}, "pending", "closing the ticket"},
		{state.Run{Phase: state.PhaseDone}, "success", "merged and ticket closed"},
		{state.Run{Phase: state.PhaseBlocked, Error: "ann pushed\nmore"}, "error", "blocked: ann pushed"},
		{state.Run{Phase: state.PhaseFailed, Error: "boom"}, "failure", "failed: boom"},
	}
	for _, c := range cases {
		st, desc := e.statusOf(&c.run)
		if st != c.state || !strings.Contains(desc, c.has) {
			t.Errorf("%s: got %s %q, want %s containing %q", c.run.Phase, st, desc, c.state, c.has)
		}
	}
}

func TestBlockedRunPostsAnErrorStatus(t *testing.T) {
	lc := newLifecycle(t, nil)
	lc.drive(t, state.PhaseMonitor)
	humanPush(t, lc, "ann")
	lc.drive(t, state.PhaseBlocked)
	last := lc.gh.statuses[len(lc.gh.statuses)-1]
	if !strings.HasPrefix(last, "error: blocked: ann pushed to ") {
		t.Errorf("last status = %q", last)
	}
}

func TestPRStatusCanBeTurnedOff(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		f := false
		cfg.Workflow.PRStatus = &f
	})
	driveUntil(t, lc, lc.run, state.PhaseDone)
	if len(lc.gh.statuses) != 0 {
		t.Errorf("no status expected, got %v", lc.gh.statuses)
	}
}
