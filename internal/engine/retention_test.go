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
	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/state"
)

func TestRetireRemovesOnlyOldFinishedWorkdirs(t *testing.T) {
	lc := newLifecycle(t, nil)
	ctx := context.Background()
	mkWorkdir := func(name string) string {
		dir := lc.eng.Cfg.StatePath("workdirs", name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
		return dir
	}
	finished := func(name string, phase state.Phase, withDir bool) *state.Run {
		t.Helper()
		r, err := lc.eng.Store.Create(&item.Item{ID: "backlog:" + name, NativeID: name, Source: "backlog", Title: name}, "claude")
		if err != nil {
			t.Fatal(err)
		}
		r.Phase = phase
		r.Branch = "loop/" + name
		if withDir {
			r.Workdir = mkWorkdir(name)
		}
		lc.eng.Store.Save(r)
		return r
	}
	done := finished("done", state.PhaseDone, true)
	failed := finished("failed", state.PhaseFailed, true)
	blocked := finished("blocked", state.PhaseBlocked, true)
	bare := finished("bare", state.PhaseDone, false)
	active := lc.run
	active.Workdir = mkWorkdir("active")
	lc.eng.Store.Save(active)

	// Fresh runs keep their checkout.
	if got, err := lc.eng.Retire(ctx, 7*24*time.Hour); err != nil || len(got) != 0 {
		t.Fatalf("nothing is old yet: %v %v", got, err)
	}
	// A week later the finished ones lose it, the active one and the run
	// without a checkout are untouched, and the run folders stay.
	lc.eng.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if got, err := lc.eng.Retire(ctx, 0); err != nil || got != nil {
		t.Errorf("age 0 must keep everything: %v %v", got, err)
	}
	got, err := lc.eng.Retire(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.ID] = true
	}
	if len(got) != 3 || !ids[done.ID] || !ids[failed.ID] || !ids[blocked.ID] {
		t.Errorf("retired %v, want the done, failed and blocked runs", ids)
	}
	for _, r := range []*state.Run{done, failed, blocked} {
		if _, err := os.Stat(r.Workdir); !os.IsNotExist(err) {
			t.Errorf("%s: workdir must be removed", r.ID)
		}
		if _, err := os.Stat(filepath.Join(r.Dir(), state.FileName)); err != nil {
			t.Errorf("%s: run folder must stay: %v", r.ID, err)
		}
		saved, _ := lc.eng.Store.Load(r.ID)
		if saved.Workdir != r.Workdir || saved.Phase != r.Phase {
			t.Errorf("%s: run.yaml must keep the phase and the old path: %+v", r.ID, saved)
		}
		log, _ := os.ReadFile(r.LogFile())
		if !strings.Contains(string(log), "removed: the run ended more than 1w ago (retention.workdirs)") {
			t.Errorf("%s: removal not logged:\n%s", r.ID, log)
		}
	}
	if _, err := os.Stat(active.Workdir); err != nil {
		t.Error("the active run keeps its workdir")
	}
	if bare.HasWorkdir() {
		t.Error("bare run has no workdir to lose")
	}
	// Nothing left to do on the next pass, and the watch output says what
	// was removed only when something was.
	if got, _ := lc.eng.Retire(ctx, 7*24*time.Hour); len(got) != 0 {
		t.Errorf("second pass must remove nothing, got %d", len(got))
	}
	lc.eng.retire(ctx)
	if strings.Contains(lc.out.String(), "retention:") {
		t.Errorf("quiet when nothing is removed:\n%s", lc.out.String())
	}
	finished("late", state.PhaseDone, true)
	lc.eng.Now = func() time.Time { return time.Now().Add(16 * 24 * time.Hour) }
	lc.eng.retire(ctx)
	if !strings.Contains(lc.out.String(), "retention: removed the workdir(s) of 1 run(s) that ended more than 1w ago; their logs and sessions are kept") {
		t.Errorf("watch output lacks the retention line:\n%s", lc.out.String())
	}
}

func TestResumeRestoresARemovedWorkdir(t *testing.T) {
	lc := newLifecycle(t, func(cfg *config.Config) { cfg.Workflow.Merge = config.MergeManual })
	r := lc.run
	lc.drive(t, state.PhaseMonitor) // branch pushed, PR open
	r.Block("fix rounds used up")
	lc.eng.Store.Save(r)
	if err := lc.eng.RemoveWorkdir(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.HasWorkdir() {
		t.Fatal("workdir must be gone")
	}
	if err := lc.eng.Resume(r); err != nil {
		t.Fatal(err)
	}
	if r.Phase != state.PhaseCheckout || r.Branch == "" {
		t.Fatalf("resume without a workdir must check the branch out again, got phase %s", r.Phase)
	}
	lc.drive(t, state.PhaseMonitor)
	if !r.HasWorkdir() {
		t.Fatalf("workdir not restored: %s\n%s", r.Workdir, lc.out.String())
	}
	if head := run(t, r.Workdir, "git", "rev-parse", "--abbrev-ref", "HEAD"); head != r.Branch {
		t.Errorf("restored checkout is on %q, want %q", head, r.Branch)
	}
	if _, err := os.Stat(filepath.Join(r.Workdir, "feature.txt")); err != nil {
		t.Error("the restored checkout must carry the agent's commits from the remote")
	}
	if b, err := os.ReadFile(filepath.Join(r.Dir(), "setup-ran")); err != nil || strings.TrimSpace(string(b)) != r.Branch {
		t.Errorf("setup steps must run again in the restored checkout: %v %q", err, b)
	}
	if !strings.Contains(lc.out.String(), "checking branch "+r.Branch+" out again") {
		t.Errorf("output must say the branch was restored:\n%s", lc.out.String())
	}

	// Without a PR there is nothing to restore: the run starts over.
	fresh, err := lc.eng.Store.Create(r.Item, "claude")
	if err != nil {
		t.Fatal(err)
	}
	fresh.Branch, fresh.Workdir = "loop/gone", filepath.Join(lc.root, "gone")
	fresh.Fail(errors.New("agent died"))
	lc.eng.Store.Save(fresh)
	if err := lc.eng.Resume(fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Phase != state.PhaseQueued || fresh.Branch != "" || fresh.Workdir != "" {
		t.Errorf("a failed run without PR and workdir starts over: phase=%s branch=%q workdir=%q", fresh.Phase, fresh.Branch, fresh.Workdir)
	}
}
