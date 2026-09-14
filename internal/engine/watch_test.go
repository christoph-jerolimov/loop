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
	"github.com/christoph-jerolimov/loop/internal/httpx"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// manualMerge configures the run so that the PR counts as merged by
// someone else on the first poll: the whole lifecycle then completes in
// one drive without reviews or checks.
func manualMerge(cfg *config.Config, lc *lifecycle) {
	cfg.Workflow.Merge = config.MergeManual
	lc.gh.mu.Lock()
	lc.gh.merged = true
	lc.gh.mu.Unlock()
}

func TestWatchDrivesActiveRunsUntilIdle(t *testing.T) {
	lc := newLifecycleWith(t, manualMerge)
	err := lc.eng.Watch(context.Background(), WatchOptions{ExitWhenIdle: true, Tick: time.Millisecond})
	if err != nil {
		t.Fatalf("watch: %v\n%s", err, lc.out.String())
	}
	r, err := lc.eng.Store.Load(lc.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Phase != state.PhaseDone {
		t.Fatalf("phase = %s (%s)\n%s", r.Phase, r.Error, lc.out.String())
	}
	if _, err := os.Stat(r.Workdir); !os.IsNotExist(err) {
		t.Error("workdir must be cleaned up")
	}
}

func TestWatchPicksReadyItemsWithinConcurrency(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		os.WriteFile(filepath.Join(lc.proj, "backlog", "search.md"), []byte("---\ntitle: Add search\n---\nIndex things.\n"), 0o644)
		os.WriteFile(filepath.Join(lc.proj, "backlog", "later.md"), []byte("---\ntitle: Later\n---\nDepends on: search.md\n"), 0o644)
	})
	err := lc.eng.Watch(context.Background(), WatchOptions{PickNew: true, ExitWhenIdle: true, Tick: time.Millisecond})
	if err != nil {
		t.Fatalf("watch: %v\n%s", err, lc.out.String())
	}
	runs, err := lc.eng.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	phases := map[string]state.Phase{}
	for _, r := range runs {
		phases[r.ItemID] = r.Phase
	}
	// auth was started by the fixture; search is picked once auth is done;
	// later depends on search, so it is picked once search is closed.
	for _, id := range []string{"backlog:auth", "backlog:search", "backlog:later"} {
		if phases[id] != state.PhaseDone {
			t.Errorf("%s = %q, want done\n%s", id, phases[id], lc.out.String())
		}
	}
	if len(runs) != 3 {
		t.Errorf("expected exactly one run per item, got %d", len(runs))
	}
}

func TestWatchRestrictedToOneRunLeavesOthersAlone(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		os.WriteFile(filepath.Join(lc.proj, "backlog", "search.md"), []byte("---\ntitle: Add search\n---\nIndex things.\n"), 0o644)
	})
	ctx := context.Background()
	search, err := lc.eng.Sources.Resolve(ctx, "search.md")
	if err != nil {
		t.Fatal(err)
	}
	other, err := lc.eng.Start(ctx, search, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := lc.eng.Watch(ctx, WatchOptions{Only: []string{lc.run.ID}, ExitWhenIdle: true, Tick: time.Millisecond}); err != nil {
		t.Fatalf("watch: %v\n%s", err, lc.out.String())
	}
	got, _ := lc.eng.Store.Load(lc.run.ID)
	untouched, _ := lc.eng.Store.Load(other.ID)
	if got.Phase != state.PhaseDone || untouched.Phase != state.PhaseQueued {
		t.Errorf("only-run = %s, other = %s\n%s", got.Phase, untouched.Phase, lc.out.String())
	}
}

func TestWatchReportsRunsParkedAtGates(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		cfg.Workflow.Gates = []string{config.GateBeforePR}
	})
	if err := lc.eng.Watch(context.Background(), WatchOptions{ExitWhenIdle: true, Tick: time.Millisecond}); err != nil {
		t.Fatalf("watch: %v\n%s", err, lc.out.String())
	}
	r, _ := lc.eng.Store.Load(lc.run.ID)
	if r.Gate != config.GateBeforePR || r.PR != nil || !r.Phase.Active() {
		t.Errorf("run should park before the PR: gate=%q phase=%s pr=%v", r.Gate, r.Phase, r.PR)
	}
	if !strings.Contains(lc.out.String(), "1 run(s) waiting at a gate; approve with: loop approve") {
		t.Errorf("parked runs must be reported:\n%s", lc.out.String())
	}
}

func TestWatchTellsWhatItDoesAndWhenItIsDone(t *testing.T) {
	lc := newLifecycleWith(t, manualMerge)
	if err := lc.eng.Watch(context.Background(), WatchOptions{ExitWhenIdle: true, Tick: time.Millisecond}); err != nil {
		t.Fatalf("watch: %v\n%s", err, lc.out.String())
	}
	out := lc.out.String()
	for _, want := range []string{
		"watching active runs, polling PRs every 1ms; not starting new items (use --pick for that); returning when nothing is left to do\n",
		"working on 1 run(s)\n",
		"nothing left to do\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("watch output lacks %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "nothing left to do\n") {
		t.Errorf("the last line says why the watch returned:\n%s", out)
	}
}

func TestWatchExplainsWhyItWaitsOnlyOnce(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		cfg.Workflow.Gates = []string{config.GateBeforePR}
	})
	lc.drive(t, state.PhaseVerify) // parks at the gate
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = lc.eng.Watch(ctx, WatchOptions{Tick: time.Millisecond})
	out := lc.out.String()
	if !strings.Contains(out, "; checking every 1ms until interrupted\n") {
		t.Errorf("a watch without ExitWhenIdle says it keeps checking:\n%s", out)
	}
	idle := "nothing to do: 1 run(s) waiting at a gate (loop approve <run>); checking again every 1ms\n"
	if n := strings.Count(out, idle); n != 1 {
		t.Errorf("the idle line must be printed once, not on every tick: got %d\n%s", n, out)
	}
}

func TestWatchReportsPRsWaitingForTheirNextPoll(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		cfg.Workflow.Merge = config.MergeManual
		cfg.Workflow.PollInterval = config.Duration(time.Hour)
	})
	lc.drive(t, state.PhaseMonitor) // opens the PR and schedules the next poll in an hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = lc.eng.Watch(ctx, WatchOptions{Only: []string{lc.run.ID}, Tick: time.Millisecond})
	out := lc.out.String()
	want := "nothing to do: 1 PR(s) waiting for their next poll at " + lc.run.NextPoll.Format("15:04:05") + "; checking again every 1ms\n"
	if n := strings.Count(out, want); n != 1 {
		t.Errorf("want %q once, got %d:\n%s", want, n, out)
	}
	if strings.Contains(out, "watching active runs") {
		t.Errorf("driving one run (loop run) has no watch intro:\n%s", out)
	}
}

func TestWatchExplainsWhyNothingIsPicked(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		// Long enough for the scheduler to look at the backlog while auth
		// still waits for its first poll, so it is counted as running.
		cfg.Workflow.PollInterval = config.Duration(50 * time.Millisecond)
		os.WriteFile(filepath.Join(lc.proj, "backlog", "later.md"), []byte("---\ntitle: Later\n---\nDepends on: missing.md\n"), 0o644)
		os.WriteFile(filepath.Join(lc.proj, "backlog", "claimed.md"), []byte("---\ntitle: Claimed\nstatus: in-progress\n---\nSomeone is on it.\n"), 0o644)
	})
	if err := lc.eng.Watch(context.Background(), WatchOptions{PickNew: true, ExitWhenIdle: true, Tick: time.Millisecond}); err != nil {
		t.Fatalf("watch: %v\n%s", err, lc.out.String())
	}
	out := lc.out.String()
	for _, want := range []string{
		"; starting ready items, up to 1 at a time (workflow.concurrency); returning when nothing is left to do\n",
		"nothing to pick: 3 open item(s), 1 already running, 1 marked in progress, 1 blocked by open dependencies (see: loop list)\n",
		"nothing to pick: 2 open item(s), 1 marked in progress, 1 blocked by open dependencies (see: loop list)\n",
		"nothing left to do\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("watch output lacks %q:\n%s", want, out)
		}
	}
}

func TestWatchTellsWhenConcurrencyIsReached(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		os.WriteFile(filepath.Join(lc.proj, "backlog", "search.md"), []byte("---\ntitle: Add search\n---\nIndex things.\n"), 0o644)
		os.WriteFile(filepath.Join(lc.proj, "backlog", "export.md"), []byte("---\ntitle: Add export\n---\nExport things.\n"), 0o644)
	})
	if err := lc.eng.Watch(context.Background(), WatchOptions{PickNew: true, ExitWhenIdle: true, Tick: time.Millisecond}); err != nil {
		t.Fatalf("watch: %v\n%s", err, lc.out.String())
	}
	out := lc.out.String()
	if !strings.Contains(out, "workflow.concurrency (1) reached; not picking more items\n") {
		t.Errorf("picking one of two ready items must say why the other waits:\n%s", out)
	}
	runs, _ := lc.eng.Store.List()
	if len(runs) != 3 {
		t.Errorf("all three items are run eventually, got %d runs", len(runs))
	}
}

func TestPickResultNote(t *testing.T) {
	cases := []struct {
		res  pickResult
		want string
	}{
		{pickResult{budget: &BudgetError{"daily run budget reached (2 of 2 today)"}}, "budget: daily run budget reached (2 of 2 today); not picking new items"},
		{pickResult{started: 1, full: true}, "workflow.concurrency (2) reached; not picking more items"},
		{pickResult{started: 1, open: 3}, ""},
		{pickResult{}, "nothing to pick: no open items"},
		{pickResult{open: 2, running: 2}, "nothing to pick: every open item already has a run"},
		{pickResult{open: 3, running: 1, unloadable: 2}, "nothing to pick: 3 open item(s), 1 already running, 2 could not be loaded (see: loop list)"},
	}
	for _, c := range cases {
		if got := c.res.note(2); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.res, got, c.want)
		}
	}
}

func TestWatchTickString(t *testing.T) {
	next := time.Date(2026, 9, 14, 12, 30, 5, 0, time.Local)
	cases := []struct {
		tick watchTick
		want string
	}{
		{watchTick{}, "nothing to do: no active runs; checking again every 5s"},
		{watchTick{polling: 2, nextPoll: next}, "nothing to do: 2 PR(s) waiting for their next poll at 12:30:05; checking again every 5s"},
		{watchTick{polling: 1}, "nothing to do: 1 PR(s) waiting for their next poll; checking again every 5s"},
		{watchTick{parked: 1, polling: 1, nextPoll: next}, "nothing to do: 1 PR(s) waiting for their next poll at 12:30:05, 1 run(s) waiting at a gate (loop approve <run>); checking again every 5s"},
		{watchTick{working: 2, polling: 1, nextPoll: next, parked: 1}, "working on 2 run(s); 1 PR(s) waiting for their next poll at 12:30:05; 1 run(s) waiting at a gate (loop approve <run>)"},
	}
	for _, c := range cases {
		if got := c.tick.String(5 * time.Second); got != c.want {
			t.Errorf("%+v:\n got %q\nwant %q", c.tick, got, c.want)
		}
	}
}

func TestWatchStopsWhenContextEnds(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		cfg.Workflow.Gates = []string{config.GateBeforePR}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := lc.eng.Watch(ctx, WatchOptions{Tick: 5 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("watch without ExitWhenIdle must end with the context, got %v", err)
	}
}

func TestPollFailedBacksOffAndHonoursRateLimits(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, _ *lifecycle) {
		cfg.Workflow.PollInterval = config.Duration(time.Minute)
	})
	r := lc.run
	before := time.Now()
	lc.eng.pollFailed(r, errors.New("HTTP 502"))
	if r.PollFailures != 1 {
		t.Errorf("failures = %d", r.PollFailures)
	}
	if wait := r.NextPoll.Sub(before); wait < 2*time.Minute-time.Second || wait > 2*time.Minute+time.Second {
		t.Errorf("first failure should double the interval, got %s", wait.Round(time.Second))
	}
	for i := 0; i < 20; i++ {
		lc.eng.pollFailed(r, errors.New("HTTP 502"))
	}
	if wait := time.Until(r.NextPoll); wait > maxPollBackoff+time.Second {
		t.Errorf("backoff must be capped at %s, got %s", maxPollBackoff, wait.Round(time.Second))
	}
	reset := time.Now().Add(42 * time.Minute).Truncate(time.Second)
	lc.eng.pollFailed(r, &httpx.RateLimitError{Status: 403, ResetAt: reset})
	if !r.NextPoll.Equal(reset) || r.PollFailures != 22 {
		t.Errorf("rate limit: next poll %s, failures %d", r.NextPoll, r.PollFailures)
	}
	if out := lc.out.String(); !strings.Contains(out, "poll: HTTP 502 (failure 1, next poll in 2m0s)") || !strings.Contains(out, "rate limited, next poll at") {
		t.Errorf("log:\n%s", out)
	}
}

func TestFailedSessionAbandonsRunAndReleasesItem(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		broken := filepath.Join(lc.root, "broken-agent")
		os.WriteFile(broken, []byte("#!/bin/sh\necho 'model overloaded' >&2\nexit 3\n"), 0o755)
		cfg.Agent.Command = broken
		cfg.Agent.Attempts = 2
	})
	_ = lc.eng.Drive(context.Background(), lc.run)
	r, _ := lc.eng.Store.Load(lc.run.ID)
	if r.Phase != state.PhaseFailed || !strings.Contains(r.Error, "no usable result after 2 attempt(s)") || !strings.Contains(lc.out.String(), "model overloaded") {
		t.Fatalf("phase = %s, error = %q\n%s", r.Phase, r.Error, lc.out.String())
	}
	if r.Attempt != 2 || len(r.Sessions) != 2 {
		t.Errorf("both attempts should have run: attempt=%d sessions=%d", r.Attempt, len(r.Sessions))
	}
	raw, _ := os.ReadFile(filepath.Join(lc.proj, "backlog", "auth.md"))
	if strings.Contains(string(raw), "in-progress") || !strings.Contains(string(raw), "## Loop log") || !strings.Contains(string(raw), "failed: ") {
		t.Errorf("item must be released and told about the failure:\n%s", raw)
	}
	if lc.eng.Check(context.Background(), r.Item).ActiveRun != nil {
		t.Error("a failed run is not active")
	}
}

func TestResumeRestartsFromTheRightPhase(t *testing.T) {
	lc := newLifecycleWith(t, manualMerge)
	r := lc.run
	if err := lc.eng.Resume(r); err == nil || !strings.Contains(err.Error(), "nothing to resume") {
		t.Errorf("resuming an active run: %v", err)
	}
	r.Block("stuck")
	if err := lc.eng.Resume(r); err != nil || r.Phase != state.PhaseQueued || r.Error != "" {
		t.Errorf("no workdir: phase=%s err=%v", r.Phase, err)
	}
	r.Workdir = lc.root
	r.Attempt = 2
	r.Fail(errors.New("boom"))
	if err := lc.eng.Resume(r); err != nil || r.Phase != state.PhaseSession || r.Attempt != 0 {
		t.Errorf("with workdir: phase=%s attempt=%d err=%v", r.Phase, r.Attempt, err)
	}
	r.PR = &state.PR{Number: 7}
	r.NextPoll = time.Now().Add(time.Hour)
	r.PendingFix = state.FixReason("ci")
	r.Block("api down")
	if err := lc.eng.Resume(r); err != nil || r.Phase != state.PhaseMonitor || !r.NextPoll.IsZero() || r.PendingFix != "" {
		t.Errorf("with PR: phase=%s next=%v pending=%q err=%v", r.Phase, r.NextPoll, r.PendingFix, err)
	}
}

func TestRemoveWorkdirBlocksActiveRuns(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		cfg.Workflow.Gates = []string{config.GateBeforePR}
	})
	ctx := context.Background()
	_ = lc.eng.Drive(ctx, lc.run) // parks at the gate with a worktree
	r, _ := lc.eng.Store.Load(lc.run.ID)
	if r.Workdir == "" {
		t.Fatalf("no workdir\n%s", lc.out.String())
	}
	if err := lc.eng.RemoveWorkdir(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Workdir); !os.IsNotExist(err) {
		t.Error("workdir still exists")
	}
	again, _ := lc.eng.Store.Load(lc.run.ID)
	if again.Phase != state.PhaseBlocked || !strings.Contains(again.Error, "workdir removed") {
		t.Errorf("an active run whose workdir is removed must be blocked: %s %q", again.Phase, again.Error)
	}
	if err := lc.eng.RemoveWorkdir(ctx, &state.Run{}); err != nil {
		t.Errorf("no workdir is a no-op: %v", err)
	}
}
