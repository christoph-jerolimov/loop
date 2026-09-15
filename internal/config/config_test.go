package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`
repo:
  url: git@github.com:acme/widgets.git
sources:
  - type: markdown
  - type: github
    claim_label: loop:wip
workflow:
  merge: when-green
  poll_interval: 2m
`), 0o644)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo.GitHub != "acme/widgets" {
		t.Errorf("github = %q", cfg.Repo.GitHub)
	}
	if cfg.Sources[0].Name != "markdown" || cfg.Sources[0].Path != "backlog" {
		t.Errorf("markdown defaults: %+v", cfg.Sources[0])
	}
	if cfg.Sources[1].Repo != "acme/widgets" || !cfg.Sources[1].Claim {
		t.Errorf("github defaults: %+v", cfg.Sources[1])
	}
	if cfg.Workflow.PollInterval.D() != 2*time.Minute || cfg.Workflow.FixRounds != 3 || cfg.Workflow.MergeMethod != "squash" {
		t.Errorf("workflow defaults: %+v", cfg.Workflow)
	}
	if cfg.Retention.WorkdirAge() != DefaultWorkdirRetention {
		t.Errorf("retention default = %v, want %v", cfg.Retention.WorkdirAge(), DefaultWorkdirRetention)
	}
	kept := t.TempDir()
	os.WriteFile(filepath.Join(kept, FileName), []byte("repo:\n  url: git@github.com:acme/widgets.git\nsources:\n  - type: markdown\nretention:\n  workdirs: 0\nagent:\n  timeout: 2d\n"), 0o644)
	explicit, err := Load(kept)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Retention.WorkdirAge() != 0 {
		t.Errorf("an explicit 0 keeps checkouts, got %v", explicit.Retention.WorkdirAge())
	}
	if explicit.Agent.Timeout.D() != 48*time.Hour {
		t.Errorf("durations accept days, got %v", explicit.Agent.Timeout.D())
	}
	if !cfg.HasGate(GateBeforeMerge) {
		t.Error("auto merge policies default to a before-merge gate")
	}
	if cfg.Agent.Runner != "claude" || cfg.Agent.Timeout.D() != 45*time.Minute || !*cfg.PR.Draft {
		t.Errorf("agent/pr defaults: %+v %+v", cfg.Agent, cfg.PR)
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`
repo:
  url: https://example.com/x.git
sources:
  - type: jira
  - type: nope
agent:
  runner: gpt
workflow:
  merge: sometimes
  gates: [before-lunch]
steps:
  verify:
    - name: both
      run: a
      agent: b
`), 0o644)
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"jql is required", "unknown type", "agent.runner", "workflow.merge", "unknown gate", "exactly one of run, script or agent", "repo.github"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

func TestCommentsPolicy(t *testing.T) {
	load := func(t *testing.T, sources string) (*Config, error) {
		t.Helper()
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, FileName), []byte("repo:\n  url: git@github.com:acme/widgets.git\nsources:\n"+sources), 0o644)
		return Load(dir)
	}
	cfg, err := load(t, "  - type: markdown\n  - type: github\n  - type: jira\n    url: https://j\n    jql: project = A\n    comments: none\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources[0].Comments != CommentsAll || cfg.Sources[1].Comments != CommentsWriters || cfg.Sources[2].Comments != CommentsNone {
		t.Errorf("defaults: markdown %q github %q jira %q", cfg.Sources[0].Comments, cfg.Sources[1].Comments, cfg.Sources[2].Comments)
	}
	cfg, err = load(t, "  - type: github\n    comments: all\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources[0].Comments != CommentsAll {
		t.Errorf("all: %q", cfg.Sources[0].Comments)
	}
	for _, v := range []string{"sometimes", "true", "false"} {
		if _, err := load(t, "  - type: github\n    comments: "+v+"\n"); err == nil || !strings.Contains(err.Error(), "comments must be all, writers or none") {
			t.Errorf("comments: %s must be rejected, got %v", v, err)
		}
	}
	if _, err := load(t, "  - type: markdown\n    comments: writers\n"); err == nil || !strings.Contains(err.Error(), "only supported by github") {
		t.Errorf("writers on markdown: %v", err)
	}
}

func TestAgentRunnerValidation(t *testing.T) {
	load := func(t *testing.T, agent string) error {
		t.Helper()
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, FileName), []byte("repo:\n  url: git@github.com:acme/widgets.git\nsources:\n  - type: markdown\nagent:\n"+agent), 0o644)
		_, err := Load(dir)
		return err
	}
	for _, name := range []string{"claude", "cursor", "codex", "gemini", "aider", "opencode", "copilot", "amp"} {
		if err := load(t, "  runner: "+name+"\n"); err != nil {
			t.Errorf("runner %s: %v", name, err)
		}
	}
	if err := load(t, "  runner: devin\n"); err == nil || !strings.Contains(err.Error(), "agent.runner must be one of claude, cursor, codex, gemini, aider, opencode, copilot, amp or custom") {
		t.Errorf("unknown runner: %v", err)
	}
	if err := load(t, "  runner: custom\n  command: ./hooks/agent.sh\n"); err == nil || !strings.Contains(err.Error(), "needs agent.command and agent.args") {
		t.Errorf("custom without args: %v", err)
	}
	if err := load(t, "  runner: custom\n  command: ./hooks/agent.sh\n  args: [\"{prompt_file}\"]\n  prompt_via: args\n  session_id: json:id\n  resume: \"./hooks/agent.sh --continue {session}\"\n"); err != nil {
		t.Errorf("complete custom runner: %v", err)
	}
	// permission_mode is validated only for the harness that uses it.
	if err := load(t, "  runner: codex\n  permission_mode: whatever\n"); err != nil {
		t.Errorf("permission_mode is Claude only: %v", err)
	}
	if err := load(t, "  runner: claude\n  permission_mode: whatever\n"); err == nil || !strings.Contains(err.Error(), "agent.permission_mode must be one of") {
		t.Errorf("claude permission_mode: %v", err)
	}
}

func TestBudgetValidation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("repo:\n  url: git@github.com:acme/widgets.git\nsources:\n  - type: markdown\nbudget:\n  run_cost: 5\n  run_time: 90m\n  daily_runs: -1\n"), 0o644)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "budget values must not be negative") {
		t.Errorf("negative budget: %v", err)
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("repo:\n  url: git@github.com:acme/widgets.git\nsources:\n  - type: markdown\nbudget:\n  run_cost: 5\n  run_time: 90m\n"), 0o644)
	cfg, err := Load(dir)
	if err != nil || cfg.Budget.RunCost != 5 || cfg.Budget.RunTime.D() != 90*time.Minute || cfg.Budget.DailyCost != 0 {
		t.Errorf("budget = %+v, %v", cfg.Budget, err)
	}
}

func TestSandboxValidation(t *testing.T) {
	load := func(t *testing.T, body string) (*Config, error) {
		t.Helper()
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, FileName), []byte("name: My Service\nrepo:\n  url: git@github.com:acme/widgets.git\n"+body+"sources:\n  - type: markdown\n"), 0o644)
		return Load(dir)
	}
	cfg, err := load(t, "  workdir: clone\nagent:\n  skills: [skills/team]\n  sandbox:\n    image: ghcr.io/acme/harness:1\n    mounts: [\"~/.cache/go-build:/home/agent/.cache/go-build:ro\", \"cache:/home/agent/.npm\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if sb := cfg.Agent.Sandbox; sb.Runtime != "podman" || sb.Home != "loop-my-service-home" {
		t.Errorf("sandbox defaults: %+v", sb)
	}
	spec := cfg.SandboxSpec()
	home, _ := os.UserHomeDir()
	if spec == nil || spec.Mounts[0] != filepath.Join(home, ".cache/go-build")+":/home/agent/.cache/go-build:ro" || spec.Mounts[1] != "cache:/home/agent/.npm" {
		t.Errorf("sandbox spec: %+v", spec)
	}
	if m := cfg.SkillMounts(); len(m) != 1 || m[0].Path != filepath.Join(cfg.Dir, "skills/team") || !m[0].ReadOnly {
		t.Errorf("skill mounts: %+v", m)
	}
	if _, err := load(t, "agent:\n  sandbox:\n    image: ghcr.io/acme/harness:1\n"); err == nil || !strings.Contains(err.Error(), "agent.sandbox needs repo.workdir: clone") {
		t.Errorf("worktree with sandbox: %v", err)
	}
	if _, err := load(t, "  workdir: clone\nagent:\n  sandbox:\n    runtime: podman\n"); err == nil || !strings.Contains(err.Error(), "agent.sandbox.image is required") {
		t.Errorf("runtime without image: %v", err)
	}
	if _, err := load(t, "  workdir: clone\nagent:\n  sandbox:\n    runtime: docker\n    image: x\n"); err == nil || !strings.Contains(err.Error(), "agent.sandbox.runtime must be podman or none") {
		t.Errorf("docker runtime: %v", err)
	}
	if _, err := load(t, "  workdir: clone\nagent:\n  sandbox:\n    image: x\n    mounts: [\"/only-host\"]\n"); err == nil || !strings.Contains(err.Error(), "agent.sandbox.mounts entry") {
		t.Errorf("bad mount: %v", err)
	}
	cfg, err = load(t, "agent:\n  sandbox:\n    runtime: none\n")
	if err != nil || cfg.Agent.Sandbox.On() || cfg.SandboxSpec() != nil {
		t.Errorf("runtime none: %v, %+v", err, cfg.Agent.Sandbox)
	}
}
