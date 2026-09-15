package cli

import (
	"fmt"
	"strings"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/engine"
	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/prompt"
)

// dryRun prints what loop run would do for the item without creating a
// run, claiming the item, touching git or starting an agent.
func dryRun(a *app, it *item.Item, rd engine.Readiness) error {
	cfg, e := a.Cfg, a.Engine
	branch := e.BranchFor(it)
	fmt.Printf("dry run for %s  %s\n\n", it.ID, it.Title)
	fmt.Printf("would start:   %s\n", startVerdict(it, rd))
	if it.Recurring() {
		fmt.Printf("schedule:      every %s; %s\n", it.Every, scheduleVerdict(rd))
	}
	fmt.Printf("branch:        %s (a taken name gets a -2, -3 suffix)\n", branch)
	fmt.Printf("workdir:       %s (%s)\n", e.WorkdirFor(branch), cfg.Repo.Workdir)
	fmt.Printf("base:          %s on %s%s\n", cfg.Repo.Base, cfg.Repo.URL, forkNote(cfg))
	model := it.Model
	if model == "" {
		model = cfg.Agent.Model
	}
	if model == "" {
		model = "harness default"
	}
	fmt.Printf("harness:       %s (%s), model %s, %d attempt(s), timeout %s\n", e.Runner.Name, e.Runner.Command, model, cfg.Agent.Attempts, cfg.Agent.Timeout.D())
	fmt.Printf("phases:        %s\n", phaseLine(cfg))
	for _, ph := range []string{"setup", "verify", "before_pr", "merged", "cleanup", "blocked", "failed"} {
		if steps := cfg.Steps.All()[ph]; len(steps) > 0 {
			var names []string
			for _, st := range steps {
				names = append(names, st.Kind()+": "+st.Label())
			}
			fmt.Printf("steps.%-8s %s\n", ph+":", strings.Join(names, "; "))
		}
	}
	fmt.Printf("pull request:  draft=%v, merge %s (%s), gates %s\n", *cfg.PR.Draft, cfg.Workflow.Merge, cfg.Workflow.MergeMethod, listOrNone(cfg.Workflow.Gates))
	if b := cfg.Budget; b.RunCost > 0 || b.RunTime > 0 || b.DailyCost > 0 || b.DailyRuns > 0 {
		fmt.Printf("budget:        run $%.2f / %s, day $%.2f / %d runs (0 = no cap)\n", b.RunCost, b.RunTime.D(), b.DailyCost, b.DailyRuns)
	}
	d := &prompt.Data{Project: cfg.Name, Item: it, Branch: branch, Base: cfg.Repo.Base, Workdir: e.WorkdirFor(branch), RunID: "<run>", SummaryFile: "<run dir>/summary.md", PlanFile: "<run dir>/plan.md", Attempt: 1}
	if sc := cfg.Source(it.Source); sc != nil && !sc.Comments.Loaded() {
		cp := *it
		cp.Comments = nil
		d.Item = &cp
	}
	title, err := prompt.Render("pr-title", cfg.PR.Title, d)
	if err != nil {
		return err
	}
	fmt.Printf("pr title:      %s\n", strings.TrimSpace(title))
	if cfg.Workflow.Plan {
		text, err := prompt.RenderFile(prompt.TplPlan, cfg.Resolve(cfg.Prompts.Plan), d)
		if err != nil {
			return err
		}
		fmt.Printf("\n--- plan prompt (%d bytes) ---\n%s\n", len(text), text)
	}
	text, err := prompt.RenderFile(prompt.TplSession, cfg.Resolve(cfg.Prompts.Session), d)
	if err != nil {
		return err
	}
	fmt.Printf("\n--- session prompt (%d bytes) ---\n%s\n", len(text), text)
	fmt.Println("--- nothing was started, claimed or written ---")
	return nil
}

func startVerdict(it *item.Item, rd engine.Readiness) string {
	switch {
	case rd.ActiveRun != nil && rd.ActiveRun.Phase.Active():
		return "no, run " + rd.ActiveRun.ID + " is active in phase " + string(rd.ActiveRun.Phase)
	case rd.ActiveRun != nil:
		return "no, run " + rd.ActiveRun.ID + " is " + string(rd.ActiveRun.Phase) + " with an open PR"
	case it.Closed:
		return "no, the item is closed"
	case rd.ScheduleError != "":
		return "no, " + rd.ScheduleError
	case rd.InProgress:
		return "only with --force, the item is marked in progress" + claimNote(it.ClaimedBy)
	case len(rd.OpenDeps) > 0:
		return "only with --force, it depends on open items: " + strings.Join(rd.OpenDeps, ", ")
	}
	return "yes"
}

// scheduleVerdict says where a recurring item is in its cycle.
func scheduleVerdict(rd engine.Readiness) string {
	switch {
	case rd.ScheduleError != "":
		return rd.ScheduleError
	case rd.LastRun == nil:
		return "never ran, due now; loop watch --pick would start it"
	case rd.Due:
		return fmt.Sprintf("last run %s, due since %s; loop watch --pick would start it", rd.LastRun.ID, rd.NextDue.Local().Format("2006-01-02 15:04"))
	}
	return fmt.Sprintf("last run %s, next due %s; loop run starts it anyway, loop watch --pick waits", rd.LastRun.ID, rd.NextDue.Local().Format("2006-01-02 15:04"))
}

func phaseLine(cfg *config.Config) string {
	var parts []string
	add := func(s string) { parts = append(parts, s) }
	add("checkout")
	add("setup")
	if cfg.Workflow.Plan {
		add("plan")
	}
	if cfg.HasGate(config.GateBeforeCode) {
		add("[gate before-code]")
	}
	add("session")
	add("verify")
	if cfg.Workflow.SelfReview {
		add("self-review")
	}
	if cfg.HasGate(config.GateBeforePR) {
		add("[gate before-pr]")
	}
	add("pr")
	add("monitor")
	if cfg.HasGate(config.GateBeforeFix) {
		add("[gate before-fix]")
	}
	if cfg.HasGate(config.GateBeforeMerge) {
		add("[gate before-merge]")
	}
	add("merge")
	add("close")
	add("cleanup")
	return strings.Join(parts, " → ")
}

func forkNote(cfg *config.Config) string {
	if cfg.Repo.Fork == "" {
		return ""
	}
	return ", pushed to fork " + cfg.Repo.Fork
}

func listOrNone(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}
