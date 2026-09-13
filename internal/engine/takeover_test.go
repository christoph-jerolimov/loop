package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// humanPush commits to the run branch as another person and tells the fake
// GitHub who the commits on the PR belong to.
func humanPush(t *testing.T, lc *lifecycle, login string) string {
	t.Helper()
	dir := filepath.Join(lc.root, "human-"+login)
	run(t, lc.root, "git", "clone", "-q", "-b", lc.run.Branch, lc.remote, dir)
	run(t, dir, "git", "config", "user.email", login+"@t")
	run(t, dir, "git", "config", "user.name", login)
	os.WriteFile(filepath.Join(dir, "notes-"+login+".txt"), []byte("by "+login+"\n"), 0o644)
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "commit", "-qm", "tweak by "+login)
	run(t, dir, "git", "push", "-q", "origin", lc.run.Branch)
	sha := run(t, dir, "git", "rev-parse", "HEAD")
	lc.gh.mu.Lock()
	lc.gh.commits = append(lc.gh.commits,
		map[string]any{"sha": lc.run.LastPushSHA, "author": map[string]any{"login": "loop-bot"}},
		map[string]any{"sha": sha, "author": map[string]any{"login": login}})
	lc.gh.mu.Unlock()
	return sha
}

func TestHumanPushParksTheRunUntilResumed(t *testing.T) {
	lc := newLifecycle(t, nil)
	lc.drive(t, state.PhaseMonitor)
	r := lc.run
	loopHead := r.LastPushSHA
	sha := humanPush(t, lc, "ann")

	lc.drive(t, state.PhaseBlocked)
	if !strings.Contains(r.Error, "ann pushed to "+r.Branch) {
		t.Errorf("error = %q", r.Error)
	}
	note := lc.gh.icomments[len(lc.gh.icomments)-1]["body"].(string)
	if !strings.Contains(note, "ann pushed to "+r.Branch) || !strings.Contains(note, "/loop resume") {
		t.Errorf("PR note = %q", note)
	}
	if got := run(t, r.Workdir, "git", "rev-parse", "HEAD"); got != sha {
		t.Errorf("worktree must be fast-forwarded to the human's commit: %s != %s", got, sha)
	}
	if r.LastPushSHA != sha || r.LastPushSHA == loopHead {
		t.Errorf("the new head must become loop's baseline: %s", r.LastPushSHA)
	}

	// After a human resumes the run, loop continues from the new head and
	// does not park again for the same commits.
	if err := lc.eng.Resume(r); err != nil {
		t.Fatal(err)
	}
	lc.drive(t, state.PhaseMonitor)
	if r.Phase != state.PhaseMonitor || r.Error != "" {
		t.Errorf("after resume: phase=%s error=%q", r.Phase, r.Error)
	}
}

func TestHumanPushCanBeFollowed(t *testing.T) {
	lc := newLifecycle(t, func(cfg *config.Config) {
		cfg.Workflow.OnHumanPush = config.HumanPushContinue
		f := false
		cfg.Workflow.CIRerun = &f
	})
	lc.drive(t, state.PhaseMonitor)
	r := lc.run
	sha := humanPush(t, lc, "bob")
	lc.drive(t, state.PhaseMonitor)
	if r.Phase != state.PhaseMonitor || r.LastPushSHA != sha {
		t.Errorf("phase=%s last push=%s want %s", r.Phase, r.LastPushSHA, sha)
	}
	if !strings.Contains(lc.out.String(), "bob pushed to "+r.Branch+"; continuing on top") {
		t.Errorf("log:\n%s", lc.out.String())
	}
	if got := run(t, r.Workdir, "git", "rev-parse", "HEAD"); got != sha {
		t.Errorf("worktree not fast-forwarded: %s != %s", got, sha)
	}
	// A later fix round builds on the human's commit rather than clashing with it.
	lc.gh.mu.Lock()
	lc.gh.checks = []map[string]any{{"id": 1, "name": "test", "status": "completed", "conclusion": "failure", "html_url": "https://gh/o/r/actions/runs/9/job/1", "output": map[string]any{"summary": "boom"}}}
	lc.gh.mu.Unlock()
	lc.drive(t, state.PhaseMonitor)
	if r.FixRounds != 1 {
		t.Fatalf("expected a CI fix round on top of the human's commit, rounds=%d\n%s", r.FixRounds, lc.out.String())
	}
	if got := run(t, r.Workdir, "git", "log", "--format=%s", "-3"); !strings.Contains(got, "tweak by bob") {
		t.Errorf("the fix must sit on top of the human's commit:\n%s", got)
	}
}

func TestOwnPushesFromElsewhereAreNotTakeovers(t *testing.T) {
	lc := newLifecycle(t, nil)
	lc.drive(t, state.PhaseMonitor)
	r := lc.run
	sha := humanPush(t, lc, "loop-bot") // another loop process pushed as loop itself
	lc.drive(t, state.PhaseMonitor)
	if r.Phase != state.PhaseMonitor || r.LastPushSHA != sha || strings.Contains(lc.out.String(), "pushed to") {
		t.Errorf("phase=%s last push=%s\n%s", r.Phase, r.LastPushSHA, lc.out.String())
	}
}
