package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/christoph-jerolimov/loop/internal/state"
)

// BudgetError says a session or a run start would exceed a configured
// budget. A run that hits one parks as blocked, never as failed: raising
// the budget in loop.yaml and resuming continues it.
type BudgetError struct{ Msg string }

func (e *BudgetError) Error() string { return "budget: " + e.Msg }

// Spend is what the project spent on one calendar day.
type Spend struct {
	Cost float64
	Runs int
}

// DailySpend sums the cost of every session started on the given day and
// counts the runs created that day, across all runs in the store.
func (e *Engine) DailySpend(day time.Time) (Spend, error) {
	runs, err := e.Store.List()
	if err != nil {
		return Spend{}, err
	}
	var sp Spend
	y, m, d := day.Date()
	for _, r := range runs {
		if ry, rm, rd := r.Created.Date(); ry == y && rm == m && rd == d {
			sp.Runs++
		}
		for _, s := range r.Sessions {
			if sy, sm, sd := s.Started.Date(); sy == y && sm == m && sd == d {
				sp.Cost += s.CostUSD
			}
		}
	}
	return sp, nil
}

// checkRunBudget is called before every session of a run.
func (e *Engine) checkRunBudget(r *state.Run) error {
	b := e.Cfg.Budget
	if b.RunCost > 0 && r.Cost() >= b.RunCost {
		return &BudgetError{fmt.Sprintf("run cost budget reached ($%.2f of $%.2f)", r.Cost(), b.RunCost)}
	}
	if b.RunTime > 0 && r.AgentTime() >= b.RunTime.D() {
		return &BudgetError{fmt.Sprintf("run time budget reached (%s of %s)", r.AgentTime().Round(time.Second), b.RunTime.D())}
	}
	if b.DailyCost > 0 {
		sp, err := e.DailySpend(time.Now())
		if err == nil && sp.Cost >= b.DailyCost {
			return &BudgetError{fmt.Sprintf("daily cost budget reached ($%.2f of $%.2f today)", sp.Cost, b.DailyCost)}
		}
	}
	return nil
}

// checkStartBudget is called before a new run is created.
func (e *Engine) checkStartBudget() error {
	b := e.Cfg.Budget
	if b.DailyRuns == 0 && b.DailyCost == 0 {
		return nil
	}
	sp, err := e.DailySpend(time.Now())
	if err != nil {
		return err
	}
	if b.DailyRuns > 0 && sp.Runs >= b.DailyRuns {
		return &BudgetError{fmt.Sprintf("daily run budget reached (%d of %d today)", sp.Runs, b.DailyRuns)}
	}
	if b.DailyCost > 0 && sp.Cost >= b.DailyCost {
		return &BudgetError{fmt.Sprintf("daily cost budget reached ($%.2f of $%.2f today)", sp.Cost, b.DailyCost)}
	}
	return nil
}

// isBudget reports whether err is a budget error.
func isBudget(err error) bool {
	var be *BudgetError
	return errors.As(err, &be)
}

// Stats summarises every run of the project.
type Stats struct {
	Runs      int            `json:"runs"`
	ByPhase   map[string]int `json:"by_phase"`
	Merged    int            `json:"merged"`
	Cost      float64        `json:"cost_usd"`
	CostMerge float64        `json:"cost_per_merged_pr_usd"`
	AgentTime time.Duration  `json:"agent_time"`
	Sessions  int            `json:"sessions"`
	FixRounds float64        `json:"mean_fix_rounds"`
	Duration  time.Duration  `json:"mean_duration_to_done"`
	Today     Spend          `json:"today"`
}

// Stats computes the project statistics from the run store.
func (e *Engine) Stats() (*Stats, error) {
	runs, err := e.Store.List()
	if err != nil {
		return nil, err
	}
	st := &Stats{ByPhase: map[string]int{}}
	var withPR, done int
	var rounds int
	var toDone time.Duration
	for _, r := range runs {
		st.Runs++
		st.ByPhase[string(r.Phase)]++
		st.Cost += r.Cost()
		st.AgentTime += r.AgentTime()
		st.Sessions += len(r.Sessions)
		if r.PR != nil {
			withPR++
			rounds += r.FixRounds
			if r.PR.Merged {
				st.Merged++
			}
		}
		if r.Phase == state.PhaseDone {
			done++
			toDone += r.Updated.Sub(r.Created)
		}
	}
	if withPR > 0 {
		st.FixRounds = float64(rounds) / float64(withPR)
	}
	if st.Merged > 0 {
		st.CostMerge = st.Cost / float64(st.Merged)
	}
	if done > 0 {
		st.Duration = (toDone / time.Duration(done)).Round(time.Second)
	}
	st.Today, _ = e.DailySpend(time.Now())
	return st, nil
}
