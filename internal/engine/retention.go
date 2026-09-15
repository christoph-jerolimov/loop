package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/state"
	"github.com/christoph-jerolimov/loop/internal/term"
)

// retireEvery is how often a watch loop applies the retention rule.
const retireEvery = 10 * time.Minute

// Retire removes the checkouts of finished runs (done, failed, blocked)
// that last changed more than age ago and returns the runs it touched.
// Run folders with run.yaml, logs and prompts stay: only the worktree or
// clone goes, which is what takes the space. A resumed run checks its
// branch out again (see Resume). age <= 0 removes nothing.
func (e *Engine) Retire(ctx context.Context, age time.Duration) ([]*state.Run, error) {
	if age <= 0 {
		return nil, nil
	}
	runs, err := e.Store.List()
	if err != nil {
		return nil, err
	}
	cutoff := e.now().Add(-age)
	var retired []*state.Run
	for _, r := range runs {
		if r.Phase.Active() || !r.Updated.Before(cutoff) || !r.HasWorkdir() {
			continue
		}
		if err := e.RemoveWorkdir(ctx, r); err != nil {
			e.failf(r, "retention: remove workdir %s: %v", r.Workdir, err)
			continue
		}
		r.Log("workdir %s removed: the run ended more than %s ago (retention.workdirs); loop resume checks the branch out again", r.Workdir, item.FormatInterval(age))
		if err := e.Store.Save(r); err != nil {
			return retired, err
		}
		retired = append(retired, r)
	}
	return retired, nil
}

// retire applies the configured retention from the watch loop and says
// what it removed.
func (e *Engine) retire(ctx context.Context) {
	age := e.Cfg.Retention.WorkdirAge()
	retired, err := e.Retire(ctx, age)
	if err != nil {
		fmt.Fprintln(e.Out, e.Paint.Paint(fmt.Sprintf("retention: %v", err), term.Red))
		return
	}
	if len(retired) > 0 {
		fmt.Fprintln(e.Out, e.Paint.Paint(fmt.Sprintf("retention: removed the workdir(s) of %d run(s) that ended more than %s ago; their logs and sessions are kept", len(retired), item.FormatInterval(age)), term.Dim))
	}
}
