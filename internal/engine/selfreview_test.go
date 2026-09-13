package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/state"
)

func sessionKinds(r *state.Run) string {
	var kinds []string
	for _, s := range r.Sessions {
		kinds = append(kinds, s.Kind)
	}
	return strings.Join(kinds, ",")
}

func TestSelfReviewFindingsGetOneFixRoundBeforeThePR(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		cfg.Workflow.SelfReview = true
	})
	r := lc.run
	driveUntil(t, lc, r, state.PhaseDone)
	if got := sessionKinds(r); got != "session,self-review,self-review-fix" {
		t.Fatalf("sessions = %s", got)
	}
	findings, _ := os.ReadFile(r.FindingsFile())
	if !strings.Contains(string(findings), "missing the closing note") {
		t.Errorf("findings = %q", findings)
	}
	fix, _ := os.ReadFile(filepath.Join(r.Dir(), "session-03-self-review-fix.prompt.md"))
	if !strings.Contains(string(fix), "A review of the branch") || !strings.Contains(string(fix), "missing the closing note") || !strings.Contains(string(fix), "**self-review** (CHANGES_REQUESTED)") {
		t.Errorf("fix prompt:\n%s", fix)
	}
	if _, err := os.Stat(filepath.Join(r.Workdir, "review-scratch.txt")); !os.IsNotExist(err) {
		t.Error("what the review session left behind must be discarded")
	}
	if r.FixRounds != 0 {
		t.Errorf("the self-review round is free, fix rounds = %d", r.FixRounds)
	}
	if !r.SelfReviewed || r.PR == nil {
		t.Errorf("reviewed=%v pr=%v", r.SelfReviewed, r.PR)
	}
	verifyRuns, _ := os.ReadFile(filepath.Join(r.Dir(), "verify-runs"))
	if n := strings.Count(string(verifyRuns), "run"); n != 2 {
		t.Errorf("verify must run after the session and again after the fix round, ran %d times", n)
	}
	if !strings.Contains(lc.out.String(), "self-review found issues; running one fix round") {
		t.Errorf("log:\n%s", lc.out.String())
	}
}

func TestSelfReviewWithoutFindingsOpensThePRDirectly(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		cfg.Workflow.SelfReview = true
		cfg.Agent.Env = map[string]string{"FAKE_CLEAN_REVIEW": "1"}
	})
	r := lc.run
	driveUntil(t, lc, r, state.PhaseDone)
	if got := sessionKinds(r); got != "session,self-review" {
		t.Errorf("sessions = %s", got)
	}
	if !strings.Contains(lc.out.String(), "self-review found nothing to send back") {
		t.Errorf("log:\n%s", lc.out.String())
	}
}

func TestNoFindings(t *testing.T) {
	for _, s := range []string{"", "  No findings.\n", "- No findings", "none", "# Nothing"} {
		if !noFindings(s) {
			t.Errorf("%q must count as no findings", s)
		}
	}
	for _, s := range []string{"- feature.txt is wrong", "No findings in a.go, but b.go panics"} {
		if noFindings(s) {
			t.Errorf("%q is a finding", s)
		}
	}
}
