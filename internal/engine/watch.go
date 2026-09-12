package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/state"
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
				fmt.Fprintf(e.Out, "[%s] error: %v\n", shortID(r), err)
			}
		}()
	}
	for {
		runs, err := e.Store.Active()
		if err != nil {
			return err
		}
		active, parked := 0, 0
		for _, r := range runs {
			if len(only) > 0 && !only[r.ID] {
				continue
			}
			active++
			if r.Gate != "" && r.GateApproved != r.Gate {
				parked++
				continue
			}
			if !r.NextPoll.IsZero() && time.Now().Before(r.NextPoll) && (r.Phase == state.PhaseMonitor || r.Phase == state.PhaseClose) {
				continue
			}
			drive(r)
		}
		picked := 0
		if o.PickNew {
			mu.Lock()
			busy := len(driving)
			mu.Unlock()
			if busy < e.Cfg.Workflow.Concurrency || len(runs) == 0 {
				n, err := e.pick(ctx, o, runs)
				if err != nil {
					fmt.Fprintf(e.Out, "pick: %v\n", err)
				}
				picked = n
			}
		}
		if o.ExitWhenIdle && active == parked && picked == 0 {
			mu.Lock()
			busy := len(driving)
			mu.Unlock()
			if busy == 0 {
				if !o.PickNew || !e.anythingReady(ctx, o) {
					wg.Wait()
					if parked > 0 {
						fmt.Fprintf(e.Out, "%d run(s) waiting at a gate; approve with: loop approve <run>\n", parked)
					}
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case <-time.After(o.Tick):
		}
	}
}

// pick starts ready items up to the concurrency limit.
func (e *Engine) pick(ctx context.Context, o WatchOptions, active []*state.Run) (int, error) {
	items, errs := e.Sources.ListAll(ctx, o.Source)
	for _, err := range errs {
		fmt.Fprintf(e.Out, "warning: %v\n", err)
	}
	activeItems := map[string]bool{}
	heavy := 0
	for _, r := range active {
		activeItems[r.ItemID] = true
		if r.Phase != state.PhaseMonitor && r.Phase != state.PhaseClose {
			heavy++
		}
	}
	started := 0
	for _, it := range items {
		if heavy+started >= e.Cfg.Workflow.Concurrency {
			break
		}
		if activeItems[it.ID] {
			continue
		}
		full, err := e.Sources.Resolve(ctx, it.ID)
		if err != nil {
			continue
		}
		rd := e.Check(ctx, full)
		if !rd.Ready && !(o.Force && rd.ActiveRun == nil) {
			continue
		}
		if _, err := e.Start(ctx, full, o.Force); err != nil {
			fmt.Fprintf(e.Out, "skip %s: %v\n", it.ID, err)
			continue
		}
		started++
	}
	return started, nil
}

func (e *Engine) anythingReady(ctx context.Context, o WatchOptions) bool {
	items, _ := e.Sources.ListAll(ctx, o.Source)
	for _, it := range items {
		if rd := e.Check(ctx, it); rd.Ready {
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
