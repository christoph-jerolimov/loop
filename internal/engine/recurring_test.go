package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// recurringAuth turns the fixture item into a weekly task.
func recurringAuth(cfg *config.Config, lc *lifecycle) {
	manualMerge(cfg, lc)
	os.WriteFile(filepath.Join(lc.proj, "backlog", "auth.md"), []byte("---\ntitle: Bump deps\nevery: 7d\n---\nUpdate the dependencies.\n"), 0o644)
}

func TestRecurringRunMergesWithoutClosingTheItem(t *testing.T) {
	lc := newLifecycleWith(t, recurringAuth)
	r := lc.run
	today := time.Now().Format("20060102")
	if !strings.Contains(lc.out.String(), "(recurring, every 7d)") {
		t.Errorf("start must say the item is recurring:\n%s", lc.out.String())
	}
	lc.drive(t, state.PhaseMonitor) // PR opened; the first poll finds it merged
	lc.drive(t, state.PhaseDone)
	if !strings.HasSuffix(r.Branch, "-"+today) {
		t.Errorf("branch %q must carry the occurrence date", r.Branch)
	}
	if body, _ := lc.gh.pr["body"].(string); strings.Contains(body, "Closes") {
		t.Errorf("a recurring item must not be linked with a closing keyword:\n%s", body)
	}
	if r.Outcome != state.OutcomeMerged {
		t.Errorf("outcome = %q, want merged", r.Outcome)
	}
	file := run(t, filepath.Join(lc.proj, "backlog"), "cat", "auth.md")
	if strings.Contains(file, "status: closed") || strings.Contains(file, "loop_run") || !strings.Contains(file, "status: open") {
		t.Errorf("the item must stay open with the claim released:\n%s", file)
	}
	if !strings.Contains(file, "Merged https://gh/o/r/pull/7") || !strings.Contains(file, "next occurrence is due") {
		t.Errorf("the ticket must learn about the merge and the next occurrence:\n%s", file)
	}
	if _, err := os.Stat(r.Workdir); !os.IsNotExist(err) {
		t.Error("workdir must be cleaned up")
	}
	if !strings.Contains(strings.Join(lc.gh.statuses, "\n"), "success: merged; fix rounds 0/3") {
		t.Errorf("the final status must not claim the ticket was closed:\n%s", strings.Join(lc.gh.statuses, "\n"))
	}

	// Not due again until the interval has passed since the run started.
	ctx := context.Background()
	it, err := lc.eng.Sources.Resolve(ctx, "auth.md")
	if err != nil {
		t.Fatal(err)
	}
	rd := lc.eng.Check(ctx, it)
	if !rd.Recurring || !rd.Ready || rd.Due || rd.Pickable() || rd.LastRun == nil || rd.LastRun.ID != r.ID {
		t.Errorf("readiness after the run = %+v", rd)
	}
	if want := r.Created.Add(7 * 24 * time.Hour); !rd.NextDue.Equal(want) {
		t.Errorf("next due = %v, want %v", rd.NextDue, want)
	}
	if got := rd.Status(it); !strings.HasPrefix(got, "due "+rd.NextDue.Local().Format("2006-01-02")) {
		t.Errorf("status = %q", got)
	}
	// An explicit start is the manual trigger and ignores the schedule.
	early, err := lc.eng.Start(ctx, it, false)
	if err != nil {
		t.Fatalf("an explicit start must not wait for the schedule: %v", err)
	}
	if early.ID == r.ID {
		t.Error("expected a second run")
	}
	// Once the interval has passed the item is due, on a new branch.
	lc.eng.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	rd = lc.eng.Check(ctx, it)
	if rd.ActiveRun == nil || rd.ActiveRun.ID != early.ID {
		t.Errorf("the queued run must occupy the item: %+v", rd)
	}
	early.Fail(errors.New("harness gone"))
	lc.eng.Store.Save(early)
	rd = lc.eng.Check(ctx, it)
	if !rd.Pickable() || rd.LastRun.ID != early.ID {
		t.Errorf("readiness after the interval = %+v", rd)
	}
	if b := lc.eng.BranchFor(it); strings.HasSuffix(b, "-"+today) {
		t.Errorf("the next occurrence must get its own date: %s", b)
	}
}

func TestRecurringRunWithoutChangesEndsDone(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		recurringAuth(cfg, lc)
		cfg.Agent.Env = map[string]string{"FAKE_NO_CHANGES": "1"}
	})
	r := lc.run
	lc.drive(t, state.PhaseDone)
	if r.Outcome != state.OutcomeNoChanges || r.PR != nil || r.Attempt != 1 {
		t.Errorf("run = outcome %q, pr %v, attempts %d; want no-changes without a PR after one attempt", r.Outcome, r.PR, r.Attempt)
	}
	if _, err := os.Stat(r.Workdir); !os.IsNotExist(err) {
		t.Error("workdir must be cleaned up")
	}
	file := run(t, filepath.Join(lc.proj, "backlog"), "cat", "auth.md")
	if !strings.Contains(file, "found nothing to change") || !strings.Contains(file, "status: open") || strings.Contains(file, "loop_run") {
		t.Errorf("the ticket must be released with a note:\n%s", file)
	}
	if !strings.Contains(lc.out.String(), "done (no changes)") {
		t.Errorf("output must say the run ended without changes:\n%s", lc.out.String())
	}
	if len(lc.gh.statuses) != 0 {
		t.Errorf("no PR, no status: %v", lc.gh.statuses)
	}
	st, err := lc.eng.Stats()
	if err != nil || st.NoChanges != 1 || st.Merged != 0 {
		t.Errorf("stats = %+v, %v", st, err)
	}
	// A previous no-changes occurrence is described to the next session.
	next, err := lc.eng.Start(context.Background(), r.Item, false)
	if err != nil {
		t.Fatal(err)
	}
	if p := lc.eng.previous(next); p == nil || p.RunID != r.ID || p.Result() != "no changes" {
		t.Errorf("previous = %+v", p)
	}
}

func TestOneShotRunWithoutChangesStillFails(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		cfg.Agent.Env = map[string]string{"FAKE_NO_CHANGES": "1"}
		cfg.Agent.Attempts = 1
	})
	if err := lc.eng.Drive(context.Background(), lc.run); err == nil || !strings.Contains(err.Error(), "no usable result") {
		t.Errorf("a one-shot item without commits is a failed run, got %v", err)
	}
	if lc.run.Phase != state.PhaseFailed || lc.run.Outcome != "" {
		t.Errorf("phase = %s, outcome = %q", lc.run.Phase, lc.run.Outcome)
	}
}

func TestWatchPicksRecurringItemsOnlyWhenDue(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		os.WriteFile(filepath.Join(lc.proj, "backlog", "weekly.md"), []byte("---\ntitle: Weekly\nevery: 1w\n---\nCheck things.\n"), 0o644)
		os.WriteFile(filepath.Join(lc.proj, "backlog", "after.md"), []byte("---\ntitle: After\n---\nDepends on: weekly.md\n"), 0o644)
	})
	watch := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := lc.eng.Watch(ctx, WatchOptions{PickNew: true, ExitWhenIdle: true, Tick: time.Millisecond}); err != nil {
			t.Fatalf("watch: %v\n%s", err, lc.out.String())
		}
	}
	count := func(itemID string) int {
		t.Helper()
		runs, _ := lc.eng.Store.List()
		n := 0
		for _, r := range runs {
			if r.ItemID == itemID {
				n++
				if r.Phase != state.PhaseDone {
					t.Errorf("%s: phase %s (%s)", r.ID, r.Phase, r.Error)
				}
			}
		}
		return n
	}
	watch()
	if count("backlog:weekly") != 1 || count("backlog:after") != 0 {
		t.Fatalf("the weekly item runs once and nothing can depend on it\n%s", lc.out.String())
	}
	out := lc.out.String()
	if !strings.Contains(out, "1 blocked by open dependencies, 1 recurring and not due yet") {
		t.Errorf("watch must say why the weekly item is not picked again:\n%s", out)
	}
	ctx := context.Background()
	after, _ := lc.eng.Sources.Resolve(ctx, "after.md")
	if rd := lc.eng.Check(ctx, after); len(rd.DepErrors) != 1 || !strings.Contains(rd.DepErrors[0], "recurring item") {
		t.Errorf("a dependency on a recurring item must be reported: %+v", rd)
	}
	// Same day: nothing new. A week later: the weekly item runs again.
	watch()
	if count("backlog:weekly") != 1 {
		t.Error("the weekly item must not run twice on the same day")
	}
	lc.eng.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	watch()
	if count("backlog:weekly") != 2 {
		t.Errorf("the weekly item must run again once due\n%s", lc.out.String())
	}
}

func TestRecurringItemWaitsForParkedPR(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		recurringAuth(cfg, lc)
		lc.gh.mu.Lock()
		lc.gh.merged = false
		lc.gh.mu.Unlock()
	})
	lc.drive(t, state.PhaseMonitor) // PR opened
	lc.run.Block("fix rounds used up")
	lc.eng.Store.Save(lc.run)
	lc.eng.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	ctx := context.Background()
	it, _ := lc.eng.Sources.Resolve(ctx, "auth.md")
	rd := lc.eng.Check(ctx, it)
	if rd.Ready || rd.ActiveRun == nil || rd.ActiveRun.ID != lc.run.ID {
		t.Errorf("a parked run with an open PR must occupy the item: %+v", rd)
	}
	if got := rd.Status(it); !strings.HasPrefix(got, "blocked with open PR") {
		t.Errorf("status = %q", got)
	}
	if _, err := lc.eng.Start(ctx, it, true); err == nil || !strings.Contains(err.Error(), "open PR") {
		t.Errorf("start must refuse while the PR is open, got %v", err)
	}
	lc.run.PR.Closed = true
	lc.eng.Store.Save(lc.run)
	if rd := lc.eng.Check(ctx, it); rd.ActiveRun != nil || !rd.Due {
		t.Errorf("a closed PR frees the item: %+v", rd)
	}
}

func TestReadinessStatus(t *testing.T) {
	next := time.Date(2026, 9, 22, 9, 30, 0, 0, time.Local)
	cases := []struct {
		rd   Readiness
		want string
	}{
		{Readiness{Ready: true, Due: true}, "ready"},
		{Readiness{ActiveRun: &state.Run{Phase: state.PhaseSession}}, "running (session)"},
		{Readiness{ActiveRun: &state.Run{ID: "r9", Phase: state.PhaseBlocked, PR: &state.PR{Number: 1}}}, "blocked with open PR (r9)"},
		{Readiness{InProgress: true}, "in progress"},
		{Readiness{OpenDeps: []string{"a.md", "#3"}}, "blocked by a.md, #3"},
		{Readiness{Recurring: true, ScheduleError: "invalid interval \"soon\""}, "invalid schedule: invalid interval \"soon\""},
		{Readiness{Ready: true, Recurring: true, NextDue: next}, "due 2026-09-22 09:30"},
	}
	for _, c := range cases {
		if got := c.rd.Status(nil); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.rd, got, c.want)
		}
	}
}
