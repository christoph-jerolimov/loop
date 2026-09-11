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
	for _, want := range []string{"jql is required", "unknown type", "agent.runner", "workflow.merge", "unknown gate", "exactly one of run or agent", "repo.github"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}
