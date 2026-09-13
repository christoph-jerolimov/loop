package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// newRun stores a run for a markdown item in the given phase. Run ids
// carry the second and the item, so runs created in one test use
// different item names.
func newRun(t *testing.T, p *project, name string, phase state.Phase, tweak func(r *state.Run)) *state.Run {
	t.Helper()
	store := state.NewStore(loadConfig(t, p).StatePath())
	r, err := store.Create(&item.Item{ID: "backlog:" + name, NativeID: name, Source: "backlog", Title: "Add " + name}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = phase
	if tweak != nil {
		tweak(r)
	}
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStatus(t *testing.T) {
	p := newProject(t)
	next := time.Now().Add(10 * time.Minute)
	polling := newRun(t, p, "auth", state.PhaseMonitor, func(r *state.Run) {
		r.Branch = "loop/auth"
		r.PR = &state.PR{Number: 7, URL: "https://github.com/o/r/pull/7"}
		r.NextPoll = next
	})
	gated := newRun(t, p, "search", state.PhasePR, func(r *state.Run) { r.Gate = "before-pr" })
	failed := newRun(t, p, "old", state.PhaseFailed, func(r *state.Run) { r.Error = "agent exited 3" })

	out, err := execute(t, p, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "RUN") || strings.Contains(out, failed.ID) {
		t.Errorf("finished runs are hidden by default:\n%s", out)
	}
	for _, want := range []string{polling.ID, "loop/auth", "https://github.com/o/r/pull/7", "next poll " + next.Format("15:04:05"), gated.ID, "waiting at gate before-pr"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	out, err = execute(t, p, "status", "--all")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, failed.ID) || !strings.Contains(out, "agent exited 3") {
		t.Errorf("--all must include the failed run with its error:\n%s", out)
	}
}

func TestApprove(t *testing.T) {
	p := newProject(t)
	r := newRun(t, p, "auth", state.PhaseMerge, func(r *state.Run) { r.Gate = "before-merge" })
	out, err := execute(t, p, "approve", r.ID)
	if err != nil || !strings.Contains(out, "approved gate before-merge for "+r.ID) {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	saved, _ := state.NewStore(loadConfig(t, p).StatePath()).Load(r.ID)
	if saved.GateApproved != "before-merge" {
		t.Errorf("approval not saved: %+v", saved)
	}
	log, _ := os.ReadFile(saved.LogFile())
	if !strings.Contains(string(log), "gate before-merge approved") {
		t.Errorf("approval not logged:\n%s", log)
	}

	plain := newRun(t, p, "search", state.PhaseMonitor, nil)
	if _, err := execute(t, p, "approve", plain.ID); err == nil || !strings.Contains(err.Error(), "not waiting at a gate") {
		t.Errorf("approving a run without a gate: %v", err)
	}
	if _, err := execute(t, p, "approve", "nope"); err == nil {
		t.Error("unknown run must fail")
	}
}

func TestResume(t *testing.T) {
	p := newProject(t)
	blocked := newRun(t, p, "auth", state.PhaseBlocked, func(r *state.Run) { r.Error = "api down"; r.PR = &state.PR{Number: 7} })
	out, err := execute(t, p, "resume", blocked.ID)
	if err != nil || !strings.Contains(out, "run "+blocked.ID+" resumed in phase monitor") {
		t.Fatalf("resume: %v\n%s", err, out)
	}
	saved, _ := state.NewStore(loadConfig(t, p).StatePath()).Load(blocked.ID)
	if saved.Phase != state.PhaseMonitor || saved.Error != "" {
		t.Errorf("resume not saved: phase=%s error=%q", saved.Phase, saved.Error)
	}
	if _, err := execute(t, p, "resume", blocked.ID); err == nil || !strings.Contains(err.Error(), "nothing to resume") {
		t.Errorf("resuming an active run: %v", err)
	}
}

func TestJoin(t *testing.T) {
	p := newProject(t)
	fresh := newRun(t, p, "search", state.PhaseQueued, nil)
	if _, err := execute(t, p, "join", fresh.ID); err == nil || !strings.Contains(err.Error(), "has no workdir yet") {
		t.Errorf("join before checkout: %v", err)
	}
	r := newRun(t, p, "auth", state.PhaseSession, func(r *state.Run) {
		r.Workdir = "/tmp/loop work/auth"
		r.Sessions = []state.Session{{ID: "first"}, {ID: "second"}, {ID: ""}}
	})
	out, err := execute(t, p, "join", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "cd '/tmp/loop work/auth' && claude --resume second" {
		t.Errorf("join = %q (latest session with an id, workdir quoted)", out)
	}
}

func TestClean(t *testing.T) {
	p := newProject(t)
	cfg := loadConfig(t, p)
	mkWorkdir := func(name string) string {
		dir := filepath.Join(cfg.StatePath("workdirs"), name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
		return dir
	}
	done := newRun(t, p, "done", state.PhaseDone, func(r *state.Run) { r.Workdir = mkWorkdir("done") })
	noDir := newRun(t, p, "nodir", state.PhaseFailed, nil)
	active := newRun(t, p, "auth", state.PhaseSession, func(r *state.Run) { r.Workdir = mkWorkdir("active") })

	out, err := execute(t, p, "clean")
	if err != nil {
		t.Fatalf("clean: %v\n%s", err, out)
	}
	if !strings.Contains(out, "removed "+done.Workdir) || strings.Contains(out, noDir.ID) {
		t.Errorf("clean output:\n%s", out)
	}
	if _, err := os.Stat(done.Workdir); !os.IsNotExist(err) {
		t.Error("finished workdir must be removed")
	}
	if _, err := os.Stat(active.Workdir); err != nil {
		t.Error("active workdir must be kept without --force")
	}
	if _, err := execute(t, p, "clean", active.ID); err == nil || !strings.Contains(err.Error(), "is still session; use --force") {
		t.Errorf("cleaning an active run: %v", err)
	}
	if _, err := execute(t, p, "clean", "--force", active.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(active.Workdir); !os.IsNotExist(err) {
		t.Error("--force must remove the active workdir")
	}
	saved, _ := state.NewStore(cfg.StatePath()).Load(active.ID)
	if saved.Phase != state.PhaseBlocked {
		t.Errorf("an active run loses its workdir and must be blocked, got %s", saved.Phase)
	}
}

func TestRunRefusesWhatItShould(t *testing.T) {
	p := newProject(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"run"}, "item argument required"},
		{[]string{"run", "--all", "auth.md"}, "--all takes no item argument"},
		{[]string{"run", "search.md"}, "not started; use --force"},
		{[]string{"run", "nope.md"}, "not found"},
	}
	for _, c := range cases {
		if _, err := execute(t, p, c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want %q", c.args, err, c.want)
		}
	}
	active := newRun(t, p, "auth", state.PhaseMonitor, nil)
	if _, err := execute(t, p, "run", "auth.md"); err == nil || !strings.Contains(err.Error(), "already has active run "+active.ID) {
		t.Errorf("run with an active run: %v", err)
	}
}

func TestWatchSeveralProjectsUntilInterrupted(t *testing.T) {
	a := newProject(t)
	b := newProject(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	out, err := executeCtx(t, ctx, a, "watch", a.dir, b.dir)
	if err != nil {
		t.Errorf("watch must end quietly with the context, got %v\n%s", err, out)
	}
	if _, err := executeCtx(t, ctx, a, "watch", t.TempDir()); err == nil || !strings.Contains(err.Error(), "no loop.yaml") {
		t.Errorf("a folder without a project: %v", err)
	}
}

func TestInitScaffoldsAProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	p := &project{dir: t.TempDir()}
	out, err := execute(t, p, "init", dir)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, f := range []string{"loop.yaml", "backlog/example.md", ".gitignore", "hooks/setup.sh", "prompts/session.md", "prompts/review.md", "prompts/ci.md", "prompts/conflict.md", "prompts/verify.md", "prompts/pr-body.md"} {
		info, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Errorf("%s not created: %v", f, err)
			continue
		}
		if strings.HasSuffix(f, ".sh") && info.Mode()&0o111 == 0 {
			t.Errorf("%s must be executable", f)
		}
		if !strings.Contains(out, "created "+filepath.Join(dir, f)) {
			t.Errorf("output does not mention %s", f)
		}
	}
	if !strings.Contains(out, "Next steps:") {
		t.Errorf("no next steps:\n%s", out)
	}
	// A second run keeps existing files; --force rewrites them.
	os.WriteFile(filepath.Join(dir, "loop.yaml"), []byte("name: mine\n"), 0o644)
	out, err = execute(t, p, "init", dir)
	if err != nil || !strings.Contains(out, "keep    "+filepath.Join(dir, "loop.yaml")+" (exists)") {
		t.Errorf("second init: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "loop.yaml")); string(b) != "name: mine\n" {
		t.Error("existing loop.yaml was overwritten without --force")
	}
	if _, err := execute(t, p, "init", "--force", dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "loop.yaml")); string(b) == "name: mine\n" {
		t.Error("--force must overwrite")
	}
}

func TestPrefixWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &prefixWriter{prefix: "demo | ", w: &buf}
	w.Write([]byte("one\ntwo"))
	w.Write([]byte(" more\n"))
	w.Write([]byte("\n"))
	w.Write([]byte("three"))
	if got := buf.String(); got != "demo | one\ndemo | two more\ndemo | \ndemo | three" {
		t.Errorf("prefixed output = %q", got)
	}
}

func TestSmallHelpers(t *testing.T) {
	if firstLine("  first\nsecond\n") != "first" || firstLine("only") != "only" {
		t.Error("firstLine")
	}
	if claimNote("") != "" || claimNote("run-1") != " by run run-1" {
		t.Error("claimNote")
	}
	if err := report(&state.Run{ID: "r", Phase: state.PhaseFailed, Error: "boom"}); err == nil || !strings.Contains(err.Error(), "run r failed: boom") {
		t.Errorf("report failed run: %v", err)
	}
	if err := report(&state.Run{ID: "r", Phase: state.PhaseBlocked, Error: "stuck"}); err == nil || !strings.Contains(err.Error(), "run r blocked: stuck") {
		t.Errorf("report blocked run: %v", err)
	}
}

func TestReportPrintsOutcome(t *testing.T) {
	capture := func(r *state.Run) string {
		t.Helper()
		out, _ := captureStdout(t, func() error { return report(r) })
		return out
	}
	done := capture(&state.Run{ItemID: "backlog:auth", Phase: state.PhaseDone, PR: &state.PR{URL: "https://gh/o/r/pull/7"}})
	if !strings.Contains(done, "backlog:auth done (https://gh/o/r/pull/7)") {
		t.Errorf("done report = %q", done)
	}
	parked := capture(&state.Run{ID: "r1", Phase: state.PhasePR, Gate: "before-pr"})
	if !strings.Contains(parked, "run r1 parked in phase pr at gate before-pr (loop approve r1); continue with: loop watch") {
		t.Errorf("parked report = %q", parked)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { buf.ReadFrom(r); close(done) }()
	fnErr := fn()
	os.Stdout = old
	w.Close()
	<-done
	return buf.String(), fnErr
}

func TestRunnerOverrideFromFlagAndEnvironment(t *testing.T) {
	p := newProject(t)
	// loop.yaml says claude; the environment and the flag override it, the flag winning.
	t.Setenv("LOOP_RUNNER", "aider")
	out, err := execute(t, p, "doctor")
	if err == nil || !strings.Contains(out, "FAIL  aider runner: \"aider\" not found on PATH") || strings.Contains(out, "agent.allow") {
		t.Errorf("LOOP_RUNNER must select the harness (and skip Claude's permission check): %v\n%s", err, out)
	}
	out, err = execute(t, p, "--runner", "codex", "doctor")
	if err == nil || !strings.Contains(out, "FAIL  codex runner: \"codex\" not found on PATH") || strings.Contains(out, "aider") {
		t.Errorf("--runner must win over LOOP_RUNNER: %v\n%s", err, out)
	}
	if _, err := execute(t, p, "--runner", "devin", "list"); err == nil || !strings.Contains(err.Error(), "agent.runner must be one of") {
		t.Errorf("an unknown override is rejected like a bad loop.yaml: %v", err)
	}
	t.Setenv("LOOP_RUNNER", "")
	out, err = execute(t, p, "doctor")
	if err != nil || !strings.Contains(out, "claude runner: claude 9.9.9 (fake)") {
		t.Errorf("without overrides loop.yaml decides: %v\n%s", err, out)
	}
}

func TestStatsCommand(t *testing.T) {
	p := newProject(t)
	newRun(t, p, "auth", state.PhaseDone, func(r *state.Run) {
		r.PR = &state.PR{Number: 7, Merged: true}
		r.FixRounds = 2
		r.Sessions = []state.Session{{Kind: "session", Started: time.Now().Add(-time.Minute), Ended: time.Now(), CostUSD: 2.5}}
	})
	newRun(t, p, "search", state.PhaseFailed, nil)
	out, err := execute(t, p, "stats")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"runs:            2 (done 1, failed 1)", "merged PRs:      1", "fix rounds:      2.0 per PR", "sessions:        1, 1m0s of agent time", "cost:            $2.50 total, $2.50 per merged PR", "today:           2 run(s), $2.50"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats lacks %q:\n%s", want, out)
		}
	}
	p.writeYAML(t, p.yaml+"budget:\n  daily_runs: 5\n  daily_cost: 10\n")
	out, _ = execute(t, p, "stats")
	if !strings.Contains(out, "today:           2 run(s) of 5, $2.50 of $10.00") {
		t.Errorf("budget limits missing:\n%s", out)
	}
	out, err = execute(t, p, "stats", "--json")
	if err != nil || !strings.Contains(out, `"merged": 1`) || !strings.Contains(out, `"cost_usd": 2.5`) {
		t.Errorf("json: %v\n%s", err, out)
	}
}

func TestListOrdersByPriorityFirst(t *testing.T) {
	p := newProject(t)
	os.WriteFile(filepath.Join(p.dir, "backlog", "urgent.md"), []byte("---\ntitle: Hotfix\ncreated: 2026-03-01\npriority: P0\n---\nNow.\n"), 0o644)
	out, err := execute(t, p, "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.Contains(lines[0], "PRIORITY") || !strings.Contains(lines[1], "backlog:urgent") || !strings.Contains(lines[1], "highest") {
		t.Errorf("the newest item with the highest priority must come first:\n%s", out)
	}
	if !strings.Contains(lines[2], "backlog:auth") {
		t.Errorf("then source order and age:\n%s", out)
	}
	out, _ = execute(t, p, "show", "urgent.md")
	if !strings.Contains(out, "priority:   highest") {
		t.Errorf("show lacks the priority:\n%s", out)
	}
}

func TestRunDryRunPrintsPlanWithoutStarting(t *testing.T) {
	p := newProject(t)
	p.writeYAML(t, p.yaml+"workflow:\n  plan: true\n  gates: [before-pr]\nsteps:\n  verify:\n    - run: go test ./...\n")
	out, err := execute(t, p, "run", "--dry-run", "search.md")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{
		"dry run for backlog:search  Add search",
		"would start:   only with --force, it depends on open items: auth.md",
		"branch:        loop/search-add-search",
		"workdir:       ",
		"harness:       claude (claude)",
		"phases:        checkout → setup → plan → session → verify → [gate before-pr] → pr → monitor → merge → close → cleanup",
		"steps.verify:  run: go test ./...",
		"gates before-pr",
		"pr title:      ",
		"--- plan prompt (",
		"--- session prompt (",
		"Index things.",
		"--- nothing was started, claimed or written ---",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	st, err := execute(t, p, "status", "-a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st, "search") {
		t.Errorf("dry run must not create a run:\n%s", st)
	}
	raw, _ := os.ReadFile(filepath.Join(p.dir, "backlog", "search.md"))
	if strings.Contains(string(raw), "in-progress") {
		t.Errorf("dry run must not claim the item:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(p.dir, ".loop", "workdirs")); !os.IsNotExist(err) {
		t.Error("dry run must not create a workdir")
	}
	out, err = execute(t, p, "run", "--dry-run", "auth.md")
	if err != nil || !strings.Contains(out, "would start:   yes") {
		t.Errorf("ready item: %v\n%s", err, out)
	}
}
