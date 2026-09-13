package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/state"
)

func TestRunCostBudgetParksBeforeTheNextSession(t *testing.T) {
	lc := newLifecycle(t, func(cfg *config.Config) {
		cfg.Budget.RunCost = 1 // the fake agent reports $1.25 per session
		cfg.Steps.Verify = []config.Step{{Name: "never", Run: "false"}}
	})
	_ = lc.eng.Drive(context.Background(), lc.run)
	r, _ := lc.eng.Store.Load(lc.run.ID)
	if r.Phase != state.PhaseBlocked || !strings.Contains(r.Error, "run cost budget reached ($1.25 of $1.00)") {
		t.Fatalf("phase=%s error=%q\n%s", r.Phase, r.Error, lc.out.String())
	}
	if len(r.Sessions) != 1 || r.Cost() != 1.25 {
		t.Errorf("only the first session may run: sessions=%d cost=%.2f", len(r.Sessions), r.Cost())
	}
	if raw, _ := os.ReadFile(filepath.Join(lc.proj, "backlog", "auth.md")); !strings.Contains(string(raw), "in-progress") {
		t.Error("a budget stop keeps the claim so the run can be resumed")
	}
	// Raising the budget and resuming continues with the verify fix session.
	lc.eng.Cfg.Budget.RunCost = 10
	if err := lc.eng.Resume(r); err != nil {
		t.Fatal(err)
	}
	_ = lc.eng.Drive(context.Background(), r)
	if len(r.Sessions) < 2 {
		t.Errorf("after raising the budget the next session must run, got %d sessions (%s: %s)", len(r.Sessions), r.Phase, r.Error)
	}
}

func TestRunTimeBudgetIsCheckedBeforeSessions(t *testing.T) {
	lc := newLifecycle(t, func(cfg *config.Config) { cfg.Budget.RunTime = config.Duration(time.Millisecond) })
	r := lc.run
	r.Sessions = append(r.Sessions, state.Session{Kind: "session", Started: time.Now().Add(-time.Minute), Ended: time.Now()})
	if err := lc.eng.checkRunBudget(r); err == nil || !strings.Contains(err.Error(), "run time budget reached") {
		t.Errorf("err = %v", err)
	}
	if err := lc.eng.checkRunBudget(&state.Run{}); err != nil {
		t.Errorf("a fresh run is within budget: %v", err)
	}
}

func TestDailyBudgetStopsPickingButKeepsDriving(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		cfg.Budget.DailyRuns = 1 // the fixture's run is today's one run
		os.WriteFile(filepath.Join(lc.proj, "backlog", "search.md"), []byte("---\ntitle: Add search\n---\nIndex things.\n"), 0o644)
	})
	if err := lc.eng.Watch(context.Background(), WatchOptions{PickNew: true, ExitWhenIdle: true, Tick: time.Millisecond}); err != nil {
		t.Fatalf("watch: %v\n%s", err, lc.out.String())
	}
	runs, _ := lc.eng.Store.List()
	if len(runs) != 1 || runs[0].Phase != state.PhaseDone {
		t.Errorf("the existing run is driven to done and no second run is started: %d runs", len(runs))
	}
	if n := strings.Count(lc.out.String(), "daily run budget reached (1 of 1 today); not picking new items"); n != 1 {
		t.Errorf("the budget note must appear exactly once, got %d\n%s", n, lc.out.String())
	}
	search, _ := lc.eng.Sources.Resolve(context.Background(), "search.md")
	if _, err := lc.eng.Start(context.Background(), search, true); err == nil || !isBudget(err) {
		t.Errorf("loop run must refuse too, even with --force: %v", err)
	}
	sp, _ := lc.eng.DailySpend(time.Now())
	if sp.Runs != 1 || sp.Cost != 1.25 {
		t.Errorf("today's spend = %+v", sp)
	}
}

func TestStats(t *testing.T) {
	lc := newLifecycleWith(t, manualMerge)
	if err := lc.eng.Watch(context.Background(), WatchOptions{ExitWhenIdle: true, Tick: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	st, err := lc.eng.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Runs != 1 || st.ByPhase["done"] != 1 || st.Merged != 1 || st.Sessions != 1 || st.Cost != 1.25 || st.CostMerge != 1.25 || st.Today.Runs != 1 {
		t.Errorf("stats = %+v", st)
	}
}
