package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/christoph-jerolimov/loop/internal/config"
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
	for _, want := range []string{
		"demo | watching active runs, polling PRs every 1m0s; not starting new items (use --pick for that); checking every 5s until interrupted\n",
		"demo | nothing to do: no active runs; checking again every 5s\n",
	} {
		if strings.Count(out, want) != 2 {
			t.Errorf("each project says what it does and why it waits, once:\nwant %q twice in\n%s", want, out)
		}
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
	for _, f := range []string{"loop.yaml", "backlog/example.md", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not created: %v", f, err)
			continue
		}
		if !strings.Contains(out, "created "+filepath.Join(dir, f)) {
			t.Errorf("output does not mention %s", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks")); !os.IsNotExist(err) {
		t.Error("no hooks folder: an empty setup script would run on every checkout for nothing")
	}
	if cfg := loadConfig(t, &project{dir: dir}); len(cfg.Steps.Setup) != 0 {
		t.Errorf("steps.setup must be empty by default: %+v", cfg.Steps.Setup)
	}
	if !strings.Contains(out, "Next steps:") {
		t.Errorf("no next steps:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "prompts")); !os.IsNotExist(err) {
		t.Error("prompts are built in; copies are written only with --prompts")
	}
	cfg := loadConfig(t, &project{dir: dir})
	if cfg.Prompts.Session != "" || cfg.PR.Body != "" {
		t.Errorf("the default loop.yaml must leave prompts at the built-ins: %+v", cfg.Prompts)
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

func TestInitDetectsToolchain(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	if _, err := exec.LookPath("gh"); err == nil {
		t.Skip("gh is installed and may hold a token")
	}
	cases := []struct {
		name   string
		files  map[string]string
		test   string
		allow  string
		marker string
	}{
		{"go", map[string]string{"go.mod": "module x\n"}, "go test ./...", "Bash(go test:*)", "go.mod found"},
		{"pnpm", map[string]string{"package.json": "{}", "pnpm-lock.yaml": ""}, "pnpm test", "Bash(pnpm test:*)", "package.json + pnpm-lock.yaml found"},
		{"npm", map[string]string{"package.json": "{}"}, "npm test", "Bash(npm test:*)", "package.json found"},
		{"make with test target", map[string]string{"Makefile": "build:\n\tgo build\ntest:\n\tgo test\n"}, "make test", "Bash(make:*)", "Makefile found"},
		{"make without test target", map[string]string{"Makefile": "build:\n\tgo build\n"}, "", "", ""},
		{"nothing", nil, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "widgets")
			os.MkdirAll(repo, 0o755)
			git(t, repo, "init", "-q")
			git(t, repo, "remote", "add", "origin", "https://github.com/acme/widgets.git")
			for name, content := range c.files {
				os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644)
			}
			dir := filepath.Join(t.TempDir(), "proj")
			out, err := execute(t, &project{dir: repo}, "init", "--no-doctor", dir)
			if err != nil {
				t.Fatalf("init: %v\n%s", err, out)
			}
			cfg := loadConfig(t, &project{dir: dir})
			if c.test == "" {
				if len(cfg.Steps.Verify) != 0 || len(cfg.Agent.Allow) != len(config.DefaultAllow) || strings.Contains(out, "verify step") {
					t.Errorf("nothing should be detected: verify=%+v allow=%v\n%s", cfg.Steps.Verify, cfg.Agent.Allow, out)
				}
				return
			}
			if len(cfg.Steps.Verify) != 1 || cfg.Steps.Verify[0].Run != c.test || cfg.Steps.Verify[0].Name != "tests" {
				t.Errorf("verify = %+v, want run %q", cfg.Steps.Verify, c.test)
			}
			if !strings.Contains(strings.Join(cfg.Agent.Allow, " "), c.allow) || !strings.Contains(strings.Join(cfg.Agent.Allow, " "), "Bash(git commit:*)") {
				t.Errorf("allow = %v, want %q and the default git rules", cfg.Agent.Allow, c.allow)
			}
			if !strings.Contains(out, "using   "+strconv.Quote(c.test)+" as verify step") || !strings.Contains(out, c.marker) {
				t.Errorf("init should say what it detected:\n%s", out)
			}
		})
	}
}

func TestInitInsideTheRepository(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "widgets")
	os.MkdirAll(repo, 0o755)
	git(t, repo, "init", "-q")
	git(t, repo, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules"), 0o644)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module widgets\n"), 0o644)

	p := &project{dir: repo}
	out, err := execute(t, p, "init", "--no-doctor", "--source", "markdown", repo)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	ignore, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if string(ignore) != "node_modules\n# loop state: base clone, workdirs and run logs\n.loop/\n" || !strings.Contains(out, "added   .loop/ to "+filepath.Join(repo, ".gitignore")) {
		t.Errorf("the repository's .gitignore must gain .loop/ without losing its lines:\n%s\n%s", ignore, out)
	}
	cfg := loadConfig(t, p)
	if cfg.Repo.GitHub != "acme/widgets" || cfg.Steps.Verify[0].Run != "go test ./..." || cfg.Dir != repo {
		t.Errorf("in-repo project: %+v", cfg.Repo)
	}
	// The project is found from any folder of the repository and lists the example item.
	os.MkdirAll(filepath.Join(repo, "internal", "pkg"), 0o755)
	out, err = execute(t, &project{dir: filepath.Join(repo, "internal", "pkg")}, "list")
	if err != nil || !strings.Contains(out, "backlog:example") {
		t.Errorf("list from a subfolder: %v\n%s", err, out)
	}
	// Running init again keeps everything and does not duplicate the ignore line.
	out, _ = execute(t, p, "init", "--no-doctor", "--source", "markdown", repo)
	ignore, _ = os.ReadFile(filepath.Join(repo, ".gitignore"))
	if strings.Count(string(ignore), ".loop/") != 1 || !strings.Contains(out, "already ignores .loop/") {
		t.Errorf("second init:\n%s\n%s", ignore, out)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "loop.yaml")); !strings.Contains(string(b), "acme/widgets") {
		t.Error("loop.yaml must be kept")
	}
}

func TestInitWritesShortConfigUnlessFull(t *testing.T) {
	p := &project{dir: t.TempDir()}
	short := filepath.Join(t.TempDir(), "short")
	if _, err := execute(t, p, "init", "--source", "markdown", short); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(short, "loop.yaml"))
	if n := strings.Count(string(b), "\n"); n > 30 || strings.Contains(string(b), "workdir: worktree") || !strings.Contains(string(b), "loop init --full") {
		t.Errorf("the default loop.yaml must be short and point at --full (%d lines):\n%s", n, b)
	}
	if !strings.Contains(string(b), "merge: manual") || strings.Contains(string(b), "base: main") {
		t.Errorf("the short loop.yaml keeps the merge knob and omits the default base:\n%s", b)
	}
	cfg := loadConfig(t, &project{dir: short})
	if cfg.Repo.Base != "main" || cfg.Workflow.Merge != "manual" || cfg.Sources[0].Path != "backlog" || !cfg.Sources[0].Claim {
		t.Errorf("defaults must fill the gaps: %+v", cfg.Repo)
	}
	full := filepath.Join(t.TempDir(), "full")
	if _, err := execute(t, p, "init", "--full", "--source", "markdown", full); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(full, "loop.yaml"))
	if !strings.Contains(string(b), "workdir: worktree") || !strings.Contains(string(b), "# Steps run inside the workdir") || !strings.Contains(string(b), "ci_rerun: true") {
		t.Errorf("--full must write the annotated loop.yaml:\n%s", b)
	}
	if cfg := loadConfig(t, &project{dir: full}); cfg.Workflow.Merge != "manual" || len(cfg.Sources) != 1 {
		t.Errorf("the full loop.yaml must load with the same decisions: %+v", cfg.Sources)
	}
}

func TestInitRunsDoctorWhenTheRepositoryIsKnown(t *testing.T) {
	// A local bare remote as origin: detected, but no host can be derived,
	// so the doctor's configuration check fails and init says so.
	p := newProject(t)
	root := filepath.Dir(p.dir)
	repo := filepath.Join(root, "checkout")
	git(t, root, "clone", "-q", filepath.Join(root, "remote.git"), repo)
	dir := filepath.Join(root, "checkout-loop")
	out, err := execute(t, &project{dir: repo}, "init", dir)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, want := range []string{"Configuration\n", "FAIL  ", "neither repo.github", "1 check(s) failed. Next steps:", "fix the failed checks above, then: loop doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("init should run the doctor and report its outcome, missing %q:\n%s", want, out)
		}
	}
	// --no-doctor skips the checks; without a detected repository there is nothing to check yet.
	out, _ = execute(t, &project{dir: repo}, "init", "--no-doctor", "--force", dir)
	if strings.Contains(out, "Configuration\n") || !strings.Contains(out, "loop run my-idea.md") {
		t.Errorf("--no-doctor:\n%s", out)
	}
	out, _ = execute(t, &project{dir: root}, "init", filepath.Join(root, "plain"))
	if strings.Contains(out, "Configuration\n") || !strings.Contains(out, "2. loop doctor") {
		t.Errorf("placeholders: doctor is a next step, not run:\n%s", out)
	}
}

func TestInitChoosesSources(t *testing.T) {
	// No token anywhere: only the markdown backlog.
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
	if _, err := exec.LookPath("gh"); err == nil {
		t.Skip("gh is installed and may hold a token")
	}
	p := &project{dir: t.TempDir()}
	types := func(dir string) string {
		cfg := loadConfig(t, &project{dir: dir})
		var out []string
		for _, s := range cfg.Sources {
			out = append(out, s.Type)
		}
		return strings.Join(out, ",")
	}
	dir := filepath.Join(t.TempDir(), "md")
	out, err := execute(t, p, "init", "--no-doctor", "--repo", "https://github.com/acme/widgets.git", dir)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if got := types(dir); got != "markdown" {
		t.Errorf("without a token: sources = %s, want markdown only\n%s", got, out)
	}
	// The host's token is set: its issue source is added.
	t.Setenv("GITHUB_TOKEN", "x")
	dir = filepath.Join(t.TempDir(), "gh")
	out, _ = execute(t, p, "init", "--no-doctor", "--repo", "https://github.com/acme/widgets.git", dir)
	if got := types(dir); got != "markdown,github" || !strings.Contains(out, "adding  github issues source") {
		t.Errorf("with GITHUB_TOKEN: sources = %s\n%s", got, out)
	}
	// A GitLab repository with only a GitHub token gets no host source; --source decides explicitly.
	dir = filepath.Join(t.TempDir(), "gl")
	execute(t, p, "init", "--no-doctor", "--repo", "https://gitlab.com/g/tool.git", dir)
	if got := types(dir); got != "markdown" {
		t.Errorf("gitlab repo without GITLAB_TOKEN: sources = %s", got)
	}
	dir = filepath.Join(t.TempDir(), "explicit")
	execute(t, p, "init", "--no-doctor", "--repo", "https://gitlab.com/g/tool.git", "--source", "gitlab,jira", dir)
	if got := types(dir); got != "markdown,gitlab,jira" {
		t.Errorf("--source gitlab,jira: sources = %s", got)
	}
	if _, err := execute(t, p, "init", "--source", "trello", filepath.Join(t.TempDir(), "bad")); err == nil || !strings.Contains(err.Error(), "--source must be") {
		t.Errorf("unknown source: %v", err)
	}
}

func TestInitPromptsWritesCopiesAndPointsAtThem(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	p := &project{dir: t.TempDir()}
	out, err := execute(t, p, "init", "--prompts", dir)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, f := range []string{"session", "plan", "self-review", "review", "ci", "conflict", "verify", "pr-body"} {
		if _, err := os.Stat(filepath.Join(dir, "prompts", f+".md")); err != nil {
			t.Errorf("prompts/%s.md not created: %v", f, err)
		}
	}
	cfg := loadConfig(t, &project{dir: dir})
	if cfg.Prompts.Session != "prompts/session.md" || cfg.Prompts.Verify != "prompts/verify.md" || cfg.PR.Body != "prompts/pr-body.md" {
		t.Errorf("loop.yaml must point at the copies: %+v body=%q", cfg.Prompts, cfg.PR.Body)
	}
	if strings.Contains(out, "add to loop.yaml") {
		t.Errorf("a fresh project needs no manual edit:\n%s", out)
	}
	// In an existing project the files are written and the keys are printed.
	existing := filepath.Join(t.TempDir(), "old")
	if _, err := execute(t, p, "init", existing); err != nil {
		t.Fatal(err)
	}
	out, err = execute(t, p, "init", "--prompts", existing)
	if err != nil || !strings.Contains(out, "add to loop.yaml to use the copies:\nprompts:\n  session: prompts/session.md") {
		t.Errorf("existing project: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(existing, "prompts", "ci.md")); err != nil {
		t.Errorf("copies not written into the existing project: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(existing, "loop.yaml")); strings.Contains(string(b), "\nprompts:\n  session") {
		t.Error("an existing loop.yaml must not be rewritten")
	}
}

func TestInitDetectsRepositoryFromCheckout(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "widgets")
	os.MkdirAll(repo, 0o755)
	git(t, repo, "init", "-q")
	git(t, repo, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	git(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")

	// The project folder (-C) is the checkout; the loop project goes next to it.
	dir := filepath.Join(root, "widgets-loop")
	out, err := execute(t, &project{dir: repo}, "init", "--no-doctor", dir)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "loop.yaml"))
	for _, want := range []string{"name: widgets\n", "url: https://github.com/acme/widgets.git", "base: develop\n"} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("loop.yaml lacks %q:\n%s", want, cfg)
		}
	}
	if !strings.Contains(out, "using   https://github.com/acme/widgets.git (origin of "+repo+")") || !strings.Contains(out, "loop run my-idea.md") {
		t.Errorf("init should say what it detected:\n%s", out)
	}
	if loadConfig(t, &project{dir: dir}).Repo.GitHub != "acme/widgets" {
		t.Error("the generated loop.yaml must load and derive the GitHub repository")
	}

	// --repo wins over the checkout; the name comes from the URL.
	dir2 := filepath.Join(root, "other")
	if _, err := execute(t, &project{dir: repo}, "init", "--no-doctor", "--repo", "https://gitlab.example.com/g/sub/tool.git", dir2); err != nil {
		t.Fatal(err)
	}
	cfg, _ = os.ReadFile(filepath.Join(dir2, "loop.yaml"))
	if !strings.Contains(string(cfg), "name: tool\n") || !strings.Contains(string(cfg), "url: https://gitlab.example.com/g/sub/tool.git") || strings.Contains(string(cfg), "base:") {
		t.Errorf("--repo (the default base is left out): %s", cfg)
	}

	// Outside any checkout: placeholders, the folder name as project name.
	dir3 := filepath.Join(root, "plain")
	out, _ = execute(t, &project{dir: root}, "init", dir3)
	cfg, _ = os.ReadFile(filepath.Join(dir3, "loop.yaml"))
	if !strings.Contains(string(cfg), "name: plain\n") || !strings.Contains(string(cfg), "url: "+placeholderURL) || strings.Contains(out, "using   ") || !strings.Contains(out, "set repo.url") {
		t.Errorf("without a checkout:\n%s\n%s", cfg, out)
	}
	for url, want := range map[string]string{"git@github.com:a/b.git": "b", "https://x/g/sub/c": "c", "/srv/git/d.git/": "d", "": ""} {
		if got := repoName(url); got != want {
			t.Errorf("repoName(%q) = %q, want %q", url, got, want)
		}
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

// fakeGitLab serves what loop doctor and loop list ask a GitLab project.
func fakeGitLab(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch p := r.URL.EscapedPath(); {
		case p == "/api/v4/user":
			write(map[string]any{"id": 9, "username": "me"})
		case p == "/api/v4/projects/g%2Fr":
			write(map[string]any{"id": 5, "path_with_namespace": "g/r", "default_branch": "main", "visibility": "private", "permissions": map[string]any{"project_access": map[string]any{"access_level": 40}}})
		case p == "/api/v4/projects/g%2Fr/issues", p == "/api/v4/projects/g%2Fr/issues/3":
			is := map[string]any{"id": 100, "iid": 3, "title": "Fix typo", "description": "In the README.", "state": "opened", "web_url": "https://gitlab.com/g/r/-/issues/3", "labels": []string{"ready"}, "created_at": "2026-02-01T00:00:00Z", "author": map[string]any{"id": 1, "username": "ann"}}
			if strings.HasSuffix(p, "/3") {
				write(is)
			} else {
				write([]any{is})
			}
		case p == "/api/v4/projects/g%2Fr/issues/3/notes":
			write([]any{})
		default:
			t.Logf("unexpected GitLab request %s %s", r.Method, p)
			w.WriteHeader(404)
			write(map[string]any{"message": "404 Not Found"})
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GITLAB_TOKEN", "glpat")
	return srv
}

func TestDoctorAndListWithGitLab(t *testing.T) {
	p := newProject(t)
	srv := fakeGitLab(t)
	yaml := strings.Replace(p.yaml, "  github: o/r\n", "  gitlab: g/r\n  gitlab_url: "+srv.URL+"\n", 1)
	yaml = strings.Replace(yaml, "  - name: gh\n    type: github\n    labels: [ready]\n", "  - name: gl\n    type: gitlab\n    labels: [ready]\n", 1)
	p.writeYAML(t, yaml)
	out, err := execute(t, p, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, want := range []string{
		"gitlab: me can push to g/r",
		"source gl (gitlab): 1 open item(s)",
		"ticket comments loaded from members with developer access or more",
		"all checks passed (0 warning(s))",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	out, err = execute(t, p, "list")
	if err != nil || !strings.Contains(out, "gl:3") || !strings.Contains(out, "Fix typo") {
		t.Errorf("list: %v\n%s", err, out)
	}
	out, err = execute(t, p, "run", "--dry-run", "gl:3")
	if err != nil || !strings.Contains(out, "would start:   yes") || !strings.Contains(out, "pr title:      Fix typo") {
		t.Errorf("dry run of a GitLab issue: %v\n%s", err, out)
	}
}
