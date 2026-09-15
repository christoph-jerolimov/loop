package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/state"
	"github.com/christoph-jerolimov/loop/internal/term"
)

// WatchOptions controls the scheduler loop.
type WatchOptions struct {
	// Only restricts driving to these run ids (empty = all active runs).
	Only []string
	// PickNew starts ready backlog items while capacity allows.
	PickNew bool
	// Force passes --force to picked items (ignore deps and claims).
	Force bool
	// Source restricts picking to one source name or type.
	Source string
	// ExitWhenIdle returns once nothing is active and nothing can be picked.
	ExitWhenIdle bool
	// Tick is the scheduler wake-up interval.
	Tick time.Duration
}

// Watch drives active runs until ctx ends or (with ExitWhenIdle) until
// nothing is left to do.
//
// It tells on the terminal what it is doing: one line at the start with
// the mode it runs in, then a status line whenever what it works on, waits
// for or cannot pick changes. Identical states are not repeated, so an
// idle watch stays quiet after saying why it waits.
func (e *Engine) Watch(ctx context.Context, o WatchOptions) error {
	if o.Tick == 0 {
		o.Tick = 5 * time.Second
	}
	var mu sync.Mutex
	driving := map[string]bool{}
	var wg sync.WaitGroup
	only := map[string]bool{}
	for _, id := range o.Only {
		only[id] = true
	}
	drive := func(r *state.Run) {
		mu.Lock()
		if driving[r.ID] {
			mu.Unlock()
			return
		}
		driving[r.ID] = true
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { mu.Lock(); delete(driving, r.ID); mu.Unlock() }()
			if err := e.Drive(ctx, r); err != nil && ctx.Err() == nil {
				fmt.Fprintf(e.Out, "%s %s\n", e.Paint.Paint("["+shortID(r)+"]", term.Dim), e.Paint.Paint(fmt.Sprintf("error: %v", err), term.Red))
			}
		}()
	}
	busy := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(driving)
	}
	if len(only) == 0 {
		fmt.Fprintln(e.Out, e.watchIntro(o))
	}
	var lastPick, lastStatus string
	var lastRetire time.Time
	for {
		if len(only) == 0 && time.Since(lastRetire) >= retireEvery {
			// Old checkouts go before new ones are made, and then every
			// ten minutes, so a worker left alone does not fill the disk.
			lastRetire = time.Now()
			e.retire(ctx)
		}
		runs, err := e.Store.Active()
		if err != nil {
			return err
		}
		e.resumeFromPR(ctx, only)
		var tick watchTick
		active, parked := 0, 0
		for _, r := range runs {
			if len(only) > 0 && !only[r.ID] {
				continue
			}
			active++
			if r.Gate != "" && r.GateApproved != r.Gate {
				parked++
				if !e.checkPRCommands(ctx, r) {
					_ = e.Store.Save(r)
					tick.parked++
					continue
				}
				_ = e.Store.Save(r)
			}
			polls := r.Phase == state.PhaseMonitor || r.Phase == state.PhaseClose
			if polls {
				tick.polling++
				if !r.NextPoll.IsZero() && time.Now().Before(r.NextPoll) {
					if tick.nextPoll.IsZero() || r.NextPoll.Before(tick.nextPoll) {
						tick.nextPoll = r.NextPoll
					}
					continue
				}
			} else {
				tick.working++
			}
			drive(r)
		}
		picked := 0
		if o.PickNew && (busy() < e.Cfg.Workflow.Concurrency || len(runs) == 0) {
			res, err := e.pick(ctx, o, runs)
			if err != nil {
				fmt.Fprintln(e.Out, e.Paint.Paint(fmt.Sprintf("pick: %v", err), term.Red))
			}
			picked = res.started
			tell(e, &lastPick, res.note(e.Cfg.Workflow.Concurrency), term.Yellow)
		}
		idle := o.ExitWhenIdle && active == parked && picked == 0 && busy() == 0 &&
			(!o.PickNew || !e.anythingReady(ctx, o))
		if idle {
			wg.Wait()
			switch {
			case parked > 0:
				fmt.Fprintln(e.Out, e.Paint.Paint(fmt.Sprintf("%d run(s) waiting at a gate; approve with: loop approve <run>", parked), term.Yellow))
			case len(only) == 0:
				fmt.Fprintln(e.Out, e.Paint.Paint("nothing left to do", term.Dim))
			}
			return nil
		}
		if active > 0 || len(only) == 0 {
			style := term.Dim
			if tick.working > 0 {
				style = term.Cyan
			}
			tell(e, &lastStatus, tick.String(o.Tick), style)
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case <-time.After(o.Tick):
		}
	}
}

// tell prints line in the given styles when it differs from what was last
// printed through the same slot, so a state that does not change is
// reported once.
func tell(e *Engine, last *string, line string, styles ...term.Style) {
	if line == *last {
		return
	}
	*last = line
	if line != "" {
		fmt.Fprintln(e.Out, e.Paint.Paint(line, styles...))
	}
}

// watchIntro is the first line of a watch: what it drives, whether it
// picks, and how often it looks.
func (e *Engine) watchIntro(o WatchOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "watching active runs, polling PRs every %s", e.Cfg.Workflow.PollInterval.D())
	if o.PickNew {
		fmt.Fprintf(&b, "; starting ready items, up to %d at a time (workflow.concurrency)", e.Cfg.Workflow.Concurrency)
		if o.Source != "" {
			fmt.Fprintf(&b, ", from source %s", o.Source)
		}
	} else {
		b.WriteString("; not starting new items (use --pick for that)")
	}
	if o.ExitWhenIdle {
		b.WriteString("; returning when nothing is left to do")
	} else {
		fmt.Fprintf(&b, "; checking every %s until interrupted", o.Tick)
	}
	return b.String()
}

// watchTick counts what one scheduler tick found.
type watchTick struct {
	// working runs are in an agent phase (checkout, session, verify, PR,
	// fix, merge, cleanup) and produce their own output.
	working int
	// polling runs have a PR and wait for or are at their next poll.
	polling int
	// nextPoll is the earliest upcoming poll among the polling runs.
	nextPoll time.Time
	// parked runs wait at a gate.
	parked int
}

// String describes the tick: what loop works on, or why it has nothing to
// do and how long it waits before it looks again.
func (t watchTick) String(tick time.Duration) string {
	var parts []string
	if t.working > 0 {
		parts = append(parts, fmt.Sprintf("working on %d run(s)", t.working))
	}
	if t.polling > 0 {
		s := fmt.Sprintf("%d PR(s) waiting for their next poll", t.polling)
		if !t.nextPoll.IsZero() {
			s += " at " + t.nextPoll.Format("15:04:05")
		}
		parts = append(parts, s)
	}
	if t.parked > 0 {
		parts = append(parts, fmt.Sprintf("%d run(s) waiting at a gate (loop approve <run>)", t.parked))
	}
	if t.working > 0 {
		return strings.Join(parts, "; ")
	}
	if len(parts) == 0 {
		parts = append(parts, "no active runs")
	}
	return fmt.Sprintf("nothing to do: %s; checking again every %s", strings.Join(parts, ", "), tick)
}

// resumeFromPR gives blocked runs that have a PR a chance to be resumed
// by a "/loop resume" comment.
func (e *Engine) resumeFromPR(ctx context.Context, only map[string]bool) {
	all, err := e.Store.List()
	if err != nil {
		return
	}
	for _, r := range all {
		if r.Phase != state.PhaseBlocked || r.PR == nil || (len(only) > 0 && !only[r.ID]) {
			continue
		}
		if e.checkPRCommands(ctx, r) || r.PollFailures > 0 {
			_ = e.Store.Save(r)
		}
	}
}

// pickResult says what pick started and why it started nothing (more).
type pickResult struct {
	started int
	// budget is the budget error that stopped picking, if any.
	budget error
	// full is set when the concurrency limit was reached with open items
	// left unlooked at.
	full bool
	// open is the number of open items in the sources; the counters below
	// say why the ones not started were skipped.
	open, running, inProgress, blocked, notDue, unloadable int
}

// note explains why nothing (more) was picked, "" when there is nothing
// to explain.
func (p pickResult) note(concurrency int) string {
	switch {
	case p.budget != nil:
		return fmt.Sprintf("%v; not picking new items", p.budget)
	case p.full:
		return fmt.Sprintf("workflow.concurrency (%d) reached; not picking more items", concurrency)
	case p.started > 0:
		return ""
	case p.open == 0:
		return "nothing to pick: no open items"
	case p.open == p.running:
		return "nothing to pick: every open item already has a run"
	}
	var why []string
	for _, c := range []struct {
		n    int
		what string
	}{
		{p.running, "already running"},
		{p.inProgress, "marked in progress"},
		{p.blocked, "blocked by open dependencies"},
		{p.notDue, "recurring and not due yet"},
		{p.unloadable, "could not be loaded"},
	} {
		if c.n > 0 {
			why = append(why, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	return fmt.Sprintf("nothing to pick: %d open item(s), %s (see: loop list)", p.open, strings.Join(why, ", "))
}

// pick starts ready items up to the concurrency limit.
func (e *Engine) pick(ctx context.Context, o WatchOptions, active []*state.Run) (pickResult, error) {
	var res pickResult
	if err := e.checkStartBudget(); err != nil {
		res.budget = err
		return res, nil
	}
	items, errs := e.Sources.ListAll(ctx, o.Source)
	for _, err := range errs {
		fmt.Fprintln(e.Out, e.Paint.Paint(fmt.Sprintf("warning: %v", err), term.Yellow))
	}
	res.open = len(items)
	activeItems := map[string]bool{}
	heavy := 0
	for _, r := range active {
		activeItems[r.ItemID] = true
		if r.Phase != state.PhaseMonitor && r.Phase != state.PhaseClose {
			heavy++
		}
	}
	for i, it := range items {
		if heavy+res.started >= e.Cfg.Workflow.Concurrency {
			// Out of capacity: the rest is not looked at, but items that
			// already run are not the reason nothing more was picked.
			for _, rest := range items[i:] {
				if activeItems[rest.ID] {
					res.running++
				} else {
					res.full = true
				}
			}
			break
		}
		if activeItems[it.ID] {
			res.running++
			continue
		}
		full, err := e.Sources.Resolve(ctx, it.ID)
		if err != nil {
			res.unloadable++
			continue
		}
		rd := e.Check(ctx, full)
		switch {
		case rd.ActiveRun != nil:
			res.running++
			continue
		case !rd.Due:
			// The schedule is never forced: loop run <item> is the manual
			// trigger for a recurring item.
			res.notDue++
			continue
		}
		if !rd.Ready && !(o.Force && rd.ScheduleError == "") {
			switch {
			case rd.InProgress:
				res.inProgress++
			case rd.ScheduleError != "":
				res.unloadable++
			default:
				res.blocked++
			}
			continue
		}
		if _, err := e.Start(ctx, full, o.Force); err != nil {
			fmt.Fprintln(e.Out, e.Paint.Paint(fmt.Sprintf("skip %s: %v", it.ID, err), term.Red))
			res.unloadable++
			continue
		}
		res.started++
	}
	return res, nil
}

func (e *Engine) anythingReady(ctx context.Context, o WatchOptions) bool {
	if e.checkStartBudget() != nil {
		return false
	}
	items, _ := e.Sources.ListAll(ctx, o.Source)
	for _, it := range items {
		if rd := e.Check(ctx, it); rd.Pickable() {
			return true
		}
	}
	return false
}

// Ready lists items with their readiness, in pick-up order.
type ReadyItem struct {
	Item      *item.Item
	Readiness Readiness
}

// ListReady returns every open item with readiness info. Sources that
// cannot be read are reported as errors without hiding the others.
func (e *Engine) ListReady(ctx context.Context, sourceFilter string) ([]ReadyItem, []error) {
	items, errs := e.Sources.ListAll(ctx, sourceFilter)
	out := make([]ReadyItem, 0, len(items))
	for _, it := range items {
		out = append(out, ReadyItem{Item: it, Readiness: e.Check(ctx, it)})
	}
	return out, errs
}
