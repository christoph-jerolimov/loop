package engine

import (
	"strings"
	"testing"

	"github.com/christoph-jerolimov/loop/internal/state"
)

func TestFlakyCIIsRerunInsteadOfFixed(t *testing.T) {
	lc := newLifecycle(t, nil)
	lc.drive(t, state.PhaseMonitor)
	r := lc.run
	lc.gh.mu.Lock()
	lc.gh.flaky = true
	lc.gh.checks = []map[string]any{{"id": 1, "name": "test", "status": "completed", "conclusion": "failure", "html_url": "https://gh/o/r/actions/runs/9/job/1", "output": map[string]any{"summary": "boom"}}}
	lc.gh.mu.Unlock()
	lc.drive(t, state.PhaseMonitor)
	if lc.gh.reruns != 1 || r.FixRounds != 0 || len(r.Sessions) != 1 {
		t.Fatalf("expected one re-run and no session: reruns=%d rounds=%d sessions=%d", lc.gh.reruns, r.FixRounds, len(r.Sessions))
	}
	if !strings.Contains(lc.out.String(), "re-running the failed jobs once before a fix round") {
		t.Errorf("log:\n%s", lc.out.String())
	}
	// The re-run turned the check green: the PR goes on without a fix round.
	lc.drive(t, state.PhaseMonitor)
	if r.FixRounds != 0 || lc.gh.reruns != 1 || lc.gh.readyCall != 1 {
		t.Errorf("green after re-run must mark the draft ready without a fix round: rounds=%d reruns=%d ready=%d", r.FixRounds, lc.gh.reruns, lc.gh.readyCall)
	}
}

func TestExternalChecksAreNotRerun(t *testing.T) {
	lc := newLifecycle(t, nil)
	lc.drive(t, state.PhaseMonitor)
	r := lc.run
	lc.gh.mu.Lock()
	// A check from another app has no Actions run behind it.
	lc.gh.checks = []map[string]any{{"id": 2, "name": "sonar", "status": "completed", "conclusion": "failure", "details_url": "https://sonar.example/project/1", "output": map[string]any{"summary": "smells"}}}
	lc.gh.mu.Unlock()
	lc.drive(t, state.PhaseMonitor)
	if lc.gh.reruns != 0 || r.FixRounds != 1 {
		t.Errorf("nothing to re-run, so the fix round starts at once: reruns=%d rounds=%d", lc.gh.reruns, r.FixRounds)
	}
}
