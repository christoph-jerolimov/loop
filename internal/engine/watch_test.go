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
