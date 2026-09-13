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

func TestPlanIsPostedAndFollowedAfterApproval(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		cfg.Workflow.Plan = true
		cfg.Workflow.Gates = []string{config.GateBeforeCode}
	})
	r := lc.run
	lc.drive(t, state.PhasePlan)
	if r.Gate != config.GateBeforeCode {
		t.Fatalf("run must wait at before-code, gate=%q", r.Gate)
	}
	plan, err := os.ReadFile(r.PlanFile())
	if err != nil || !strings.Contains(string(plan), "Estimate: S") {
		t.Fatalf("plan file: %v %q", err, plan)
	}
	raw, _ := os.ReadFile(filepath.Join(lc.proj, "backlog", "auth.md"))
	if !strings.Contains(string(raw), "loop plan for run `"+r.ID+"`") || !strings.Contains(string(raw), "Estimate: S") || !strings.Contains(string(raw), "loop approve "+r.ID) {
		t.Errorf("plan not posted on the ticket:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(r.Workdir, "scratch.txt")); !os.IsNotExist(err) {
		t.Error("what the plan session left behind must be discarded")
	}
	if len(r.Sessions) != 1 || r.Sessions[0].Kind != "plan" {
		t.Errorf("sessions = %+v", r.Sessions)
	}

	// Approve, then the implementation runs with the plan in its prompt.
	r.GateApproved = r.Gate
	driveUntil(t, lc, r, state.PhaseDone)
	prompts, _ := filepath.Glob(filepath.Join(r.Dir(), "session-02-session.prompt.md"))
	if len(prompts) != 1 {
		t.Fatalf("implementation prompt: %v", prompts)
	}
	pb, _ := os.ReadFile(prompts[0])
	if !strings.Contains(string(pb), "## Plan") || !strings.Contains(string(pb), "Estimate: S, one session.") {
		t.Errorf("session prompt lacks the plan:\n%s", pb)
	}
	if len(r.Sessions) != 2 {
		t.Errorf("plan must run once, then the session: %d sessions", len(r.Sessions))
	}
}

func TestBeforeCodeGateWithoutPlan(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		cfg.Workflow.Gates = []string{config.GateBeforeCode}
	})
	r := lc.run
	lc.drive(t, state.PhasePlan)
	if r.Gate != config.GateBeforeCode || len(r.Sessions) != 0 {
		t.Fatalf("gate=%q sessions=%d", r.Gate, len(r.Sessions))
	}
	raw, _ := os.ReadFile(filepath.Join(lc.proj, "backlog", "auth.md"))
	if !strings.Contains(string(raw), "waiting at gate `before-code`") {
		t.Errorf("the ticket must say how to continue:\n%s", raw)
	}
	r.GateApproved = r.Gate
	driveUntil(t, lc, r, state.PhaseDone)
}

func TestIssueCommentReleasesBeforeCodeGate(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		manualMerge(cfg, lc)
		cfg.Workflow.Plan = true
		cfg.Workflow.Gates = []string{config.GateBeforeCode}
		cfg.Sources = append(cfg.Sources, config.SourceConfig{Name: "gh", Type: "github", Repo: "o/r", Comments: config.CommentsAll})
		lc.gh.perms = map[string]string{"ann": "write", "guest": "read"}
	})
	ctx := context.Background()
	it, err := lc.eng.Sources.Resolve(ctx, "gh:12")
	if err != nil {
		t.Fatal(err)
	}
	r, err := lc.eng.Start(ctx, it, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = lc.eng.Drive(ctx, r)
	if r.Gate != config.GateBeforeCode {
		t.Fatalf("gate=%q phase=%s\n%s", r.Gate, r.Phase, lc.out.String())
	}
	last := lc.gh.issueComments[len(lc.gh.issueComments)-1]["body"].(string)
	if !strings.Contains(last, "loop plan for run") || !strings.Contains(last, "`/loop approve` comment here") {
		t.Errorf("plan comment on the issue: %q", last)
	}
	// A read-only user cannot release the gate; a collaborator can.
	comment := func(login, body string) {
		lc.gh.mu.Lock()
		lc.gh.issueComments = append(lc.gh.issueComments, map[string]any{"id": 900 + len(lc.gh.issueComments), "body": body, "user": map[string]any{"login": login}, "created_at": time.Now().Add(time.Second)})
		lc.gh.mu.Unlock()
	}
	comment("guest", "/loop approve")
	time.Sleep(2 * time.Millisecond)
	_ = lc.eng.Drive(ctx, r)
	if r.Gate != config.GateBeforeCode {
		t.Fatalf("a read-only user must not release the gate")
	}
	comment("ann", "/loop approve")
	driveUntil(t, lc, r, state.PhaseDone)
	if !strings.Contains(lc.out.String(), "gate before-code approved by ann via PR comment") {
		t.Errorf("log:\n%s", lc.out.String())
	}
}

// driveUntil drives the run repeatedly, waiting out the fixture's poll
// interval between steps, until it reaches the phase or stops moving.
func driveUntil(t *testing.T, lc *lifecycle, r *state.Run, want state.Phase) {
	t.Helper()
	for i := 0; i < 20 && r.Phase != want && r.Phase.Active(); i++ {
		time.Sleep(2 * time.Millisecond)
		if err := lc.eng.Drive(context.Background(), r); err != nil {
			t.Fatalf("drive: %v\n%s", err, lc.out.String())
		}
	}
	if r.Phase != want {
		t.Fatalf("phase = %s, want %s (error=%q)\n%s", r.Phase, want, r.Error, lc.out.String())
	}
}
