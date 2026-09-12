// Package config loads and validates loop.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FileName is the project configuration file.
const FileName = "loop.yaml"

// StateDir is the git-ignored folder next to loop.yaml that holds
// the base clone, workdirs and run state.
const StateDir = ".loop"

// Config is the root of loop.yaml.
type Config struct {
	Name     string         `yaml:"name"`
	Repo     Repo           `yaml:"repo"`
	Sources  []SourceConfig `yaml:"sources"`
	Prompts  Prompts        `yaml:"prompts"`
	Agent    Agent          `yaml:"agent"`
	Steps    Steps          `yaml:"steps"`
	PR       PR             `yaml:"pr"`
	Workflow Workflow       `yaml:"workflow"`

	// Dir is the directory that contains loop.yaml. Not part of the file.
	Dir string `yaml:"-"`
}

// Repo describes the target repository.
type Repo struct {
	URL          string `yaml:"url"`
	Base         string `yaml:"base"`
	Workdir      string `yaml:"workdir"` // worktree | clone
	BranchPrefix string `yaml:"branch_prefix"`
	// GitHub is "owner/name". Derived from URL when empty.
	GitHub string `yaml:"github"`
}

// SourceConfig configures one backlog source. Fields that do not apply to
// the type are ignored.
type SourceConfig struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"` // markdown | github | jira

	// markdown
	Path string `yaml:"path"`

	// github
	Repo string `yaml:"repo"` // owner/name, defaults to repo.github

	// jira
	URL string `yaml:"url"`
	JQL string `yaml:"jql"`
	// Transitions maps loop states to Jira transition names.
	Transitions map[string]string `yaml:"transitions"`

	// shared filters and claiming
	Labels     []string `yaml:"labels"`
	Claim      bool     `yaml:"claim"`
	ClaimLabel string   `yaml:"claim_label"`
	// Comments controls whether ticket comments are loaded into the prompt data.
	Comments *bool `yaml:"comments"`
}

// Prompts points to template files, relative to loop.yaml. Empty values
// fall back to the embedded defaults.
type Prompts struct {
	Session  string `yaml:"session"`
	Review   string `yaml:"review"`
	CI       string `yaml:"ci"`
	Conflict string `yaml:"conflict"`
	Verify   string `yaml:"verify"`
}

// Agent configures the coding agent runner.
type Agent struct {
	Runner         string            `yaml:"runner"` // claude | cursor
	Command        string            `yaml:"command"`
	Model          string            `yaml:"model"`
	PermissionMode string            `yaml:"permission_mode"`
	Timeout        Duration          `yaml:"timeout"`
	MaxTurns       int               `yaml:"max_turns"`
	Attempts       int               `yaml:"attempts"`
	Skills         []string          `yaml:"skills"`
	Env            map[string]string `yaml:"env"`
	ExtraArgs      []string          `yaml:"extra_args"`
}

// Step is a shell command (run), a script file relative to loop.yaml
// (script), or an agent session driven by a prompt template (agent).
// Every kind executes inside the workdir.
type Step struct {
	Name    string   `yaml:"name"`
	Run     string   `yaml:"run"`
	Script  string   `yaml:"script"`
	Agent   string   `yaml:"agent"`
	Model   string   `yaml:"model"`
	Timeout Duration `yaml:"timeout"`
}

// Kind returns run, script or agent.
func (s Step) Kind() string {
	switch {
	case s.Run != "":
		return "run"
	case s.Script != "":
		return "script"
	case s.Agent != "":
		return "agent"
	}
	return ""
}

// Label is the step name or, when unset, its command or path.
func (s Step) Label() string {
	if s.Name != "" {
		return s.Name
	}
	for _, v := range []string{s.Run, s.Script, s.Agent} {
		if v != "" {
			return v
		}
	}
	return "step"
}

// Steps groups steps by phase. All of them are optional.
type Steps struct {
	// Setup runs in the fresh workdir after checkout, before the session.
	Setup []Step `yaml:"setup"`
	// Verify runs after the session; a failing run/script step starts a fix session.
	Verify []Step `yaml:"verify"`
	// BeforePR runs after verify, before the branch is pushed.
	BeforePR []Step `yaml:"before_pr"`
	// Merged runs once the PR has been merged.
	Merged []Step `yaml:"merged"`
	// Cleanup runs before the workdir is removed.
	Cleanup []Step `yaml:"cleanup"`
	// Blocked runs when a run parks because it needs a human (fix rounds
	// used up, branch protection, PR closed). Use it to notify someone.
	Blocked []Step `yaml:"blocked"`
	// Failed runs when a run fails (no usable session result, setup or
	// verify errors, push or PR creation errors).
	Failed []Step `yaml:"failed"`
}

// All returns every configured step list with its phase name.
func (s Steps) All() map[string][]Step {
	return map[string][]Step{
		"setup": s.Setup, "verify": s.Verify, "before_pr": s.BeforePR, "merged": s.Merged, "cleanup": s.Cleanup,
		"blocked": s.Blocked, "failed": s.Failed,
	}
}

// PR configures pull request creation.
type PR struct {
	Draft     *bool    `yaml:"draft"`
	Title     string   `yaml:"title"`
	Body      string   `yaml:"body"`
	Reviewers []string `yaml:"reviewers"`
	Labels    []string `yaml:"labels"`
	LinkIssue *bool    `yaml:"link_issue"`
	// CommitUncommitted commits leftover changes after a session.
	CommitUncommitted *bool `yaml:"commit_uncommitted"`
}

// Workflow configures monitoring, fixing and merging.
type Workflow struct {
	PollInterval      Duration `yaml:"poll_interval"`
	FixRounds         int      `yaml:"fix_rounds"`
	ConflictAttempts  int      `yaml:"conflict_attempts"`
	Merge             string   `yaml:"merge"` // manual | when-green | when-green-and-approved | github-auto-merge
	MergeMethod       string   `yaml:"merge_method"`
	DeleteBranch      *bool    `yaml:"delete_branch"`
	CloseIssueOnMerge *bool    `yaml:"close_issue_on_merge"`
	Gates             []string `yaml:"gates"`
	Concurrency       int      `yaml:"concurrency"`
	// RequiredChecks: when set, only these check names must be green.
	RequiredChecks []string `yaml:"required_checks"`
	// CILogLines is how many lines from the end of a failed Actions job log
	// are passed to the CI fix prompt. 0 disables log fetching.
	CILogLines *int `yaml:"ci_log_lines"`
	// Cleanup removes the workdir once the item is closed.
	Cleanup *bool `yaml:"cleanup"`
}

// Duration is a yaml-friendly time.Duration.
type Duration time.Duration

// UnmarshalYAML parses "45m", "1h30m" etc.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML renders the duration in Go syntax.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Merge policies.
const (
	MergeManual           = "manual"
	MergeWhenGreen        = "when-green"
	MergeWhenGreenApprove = "when-green-and-approved"
	MergeGitHubAuto       = "github-auto-merge"
)

// Gate names.
const (
	GateBeforePR    = "before-pr"
	GateBeforeFix   = "before-fix"
	GateBeforeMerge = "before-merge"
)

// Load reads loop.yaml from dir (or a file path) and applies defaults.
func Load(path string) (*Config, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		path = filepath.Join(path, FileName)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	abs, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	cfg.Dir = abs
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Find walks up from dir until it finds loop.yaml.
func Find(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, FileName)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s found in %s or any parent", FileName, dir)
		}
		dir = parent
	}
}

var githubURLRe = regexp.MustCompile(`(?:github\.com[:/])([^/]+)/([^/]+?)(?:\.git)?/?$`)

// ApplyDefaults fills in every optional value.
func (c *Config) ApplyDefaults() {
	if c.Name == "" {
		c.Name = filepath.Base(c.Dir)
	}
	if c.Repo.Base == "" {
		c.Repo.Base = "main"
	}
	if c.Repo.Workdir == "" {
		c.Repo.Workdir = "worktree"
	}
	if c.Repo.BranchPrefix == "" {
		c.Repo.BranchPrefix = "loop/"
	}
	if c.Repo.GitHub == "" {
		if m := githubURLRe.FindStringSubmatch(c.Repo.URL); m != nil {
			c.Repo.GitHub = m[1] + "/" + m[2]
		}
	}
	for i := range c.Sources {
		s := &c.Sources[i]
		if s.Name == "" {
			s.Name = s.Type
		}
		if s.Type == "github" && s.Repo == "" {
			s.Repo = c.Repo.GitHub
		}
		if s.Type == "markdown" && s.Path == "" {
			s.Path = "backlog"
		}
		if s.Type == "jira" && s.Transitions == nil {
			s.Transitions = map[string]string{}
		}
		if s.ClaimLabel != "" {
			s.Claim = true
		}
		if s.Claim && s.ClaimLabel == "" && s.Type != "markdown" {
			s.ClaimLabel = "loop:in-progress"
		}
		if s.Comments == nil {
			t := true
			s.Comments = &t
		}
	}
	if c.Agent.Runner == "" {
		c.Agent.Runner = "claude"
	}
	if c.Agent.PermissionMode == "" {
		c.Agent.PermissionMode = "acceptEdits"
	}
	if c.Agent.Timeout == 0 {
		c.Agent.Timeout = Duration(45 * time.Minute)
	}
	if c.Agent.MaxTurns == 0 {
		c.Agent.MaxTurns = 200
	}
	if c.Agent.Attempts == 0 {
		c.Agent.Attempts = 2
	}
	if c.PR.Draft == nil {
		t := true
		c.PR.Draft = &t
	}
	if c.PR.LinkIssue == nil {
		t := true
		c.PR.LinkIssue = &t
	}
	if c.PR.CommitUncommitted == nil {
		t := true
		c.PR.CommitUncommitted = &t
	}
	if c.PR.Title == "" {
		c.PR.Title = "{{ .Item.Title }}"
	}
	if c.Workflow.PollInterval == 0 {
		c.Workflow.PollInterval = Duration(60 * time.Second)
	}
	if c.Workflow.FixRounds == 0 {
		c.Workflow.FixRounds = 3
	}
	if c.Workflow.ConflictAttempts == 0 {
		c.Workflow.ConflictAttempts = 1
	}
	if c.Workflow.Merge == "" {
		c.Workflow.Merge = MergeManual
	}
	if c.Workflow.MergeMethod == "" {
		c.Workflow.MergeMethod = "squash"
	}
	if c.Workflow.DeleteBranch == nil {
		t := true
		c.Workflow.DeleteBranch = &t
	}
	if c.Workflow.CloseIssueOnMerge == nil {
		t := true
		c.Workflow.CloseIssueOnMerge = &t
	}
	if c.Workflow.Cleanup == nil {
		t := true
		c.Workflow.Cleanup = &t
	}
	if c.Workflow.CILogLines == nil {
		n := 200
		c.Workflow.CILogLines = &n
	}
	if c.Workflow.Gates == nil && c.Workflow.Merge != MergeManual {
		c.Workflow.Gates = []string{GateBeforeMerge}
	}
	if c.Workflow.Concurrency == 0 {
		c.Workflow.Concurrency = 1
	}
}

// Validate reports configuration errors.
func (c *Config) Validate() error {
	var errs []error
	if c.Repo.URL == "" {
		errs = append(errs, errors.New("repo.url is required"))
	}
	if c.Repo.Workdir != "worktree" && c.Repo.Workdir != "clone" {
		errs = append(errs, fmt.Errorf("repo.workdir must be worktree or clone, got %q", c.Repo.Workdir))
	}
	if len(c.Sources) == 0 {
		errs = append(errs, errors.New("at least one source is required"))
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		if seen[s.Name] {
			errs = append(errs, fmt.Errorf("duplicate source name %q", s.Name))
		}
		seen[s.Name] = true
		switch s.Type {
		case "markdown":
		case "github":
			if s.Repo == "" {
				errs = append(errs, fmt.Errorf("source %s: repo is required (owner/name)", s.Name))
			}
		case "jira":
			if s.URL == "" {
				errs = append(errs, fmt.Errorf("source %s: url is required", s.Name))
			}
			if s.JQL == "" {
				errs = append(errs, fmt.Errorf("source %s: jql is required", s.Name))
			}
		default:
			errs = append(errs, fmt.Errorf("source %s: unknown type %q", s.Name, s.Type))
		}
	}
	switch c.Agent.Runner {
	case "claude", "cursor":
	default:
		errs = append(errs, fmt.Errorf("agent.runner must be claude or cursor, got %q", c.Agent.Runner))
	}
	switch c.Workflow.Merge {
	case MergeManual, MergeWhenGreen, MergeWhenGreenApprove, MergeGitHubAuto:
	default:
		errs = append(errs, fmt.Errorf("workflow.merge must be one of manual, when-green, when-green-and-approved, github-auto-merge; got %q", c.Workflow.Merge))
	}
	switch c.Workflow.MergeMethod {
	case "squash", "merge", "rebase":
	default:
		errs = append(errs, fmt.Errorf("workflow.merge_method must be squash, merge or rebase; got %q", c.Workflow.MergeMethod))
	}
	for _, g := range c.Workflow.Gates {
		switch g {
		case GateBeforePR, GateBeforeFix, GateBeforeMerge:
		default:
			errs = append(errs, fmt.Errorf("unknown gate %q", g))
		}
	}
	for phase, steps := range c.Steps.All() {
		for _, st := range steps {
			n := 0
			for _, v := range []string{st.Run, st.Script, st.Agent} {
				if v != "" {
					n++
				}
			}
			if n != 1 {
				errs = append(errs, fmt.Errorf("steps.%s step %q must set exactly one of run, script or agent", phase, st.Label()))
			}
		}
	}
	if c.Repo.GitHub == "" {
		errs = append(errs, errors.New("repo.github (owner/name) could not be derived from repo.url; set it explicitly"))
	}
	return errors.Join(errs...)
}

// HasGate reports whether the workflow pauses at the given gate.
func (c *Config) HasGate(name string) bool {
	for _, g := range c.Workflow.Gates {
		if g == name {
			return true
		}
	}
	return false
}

// Source returns the source config by name.
func (c *Config) Source(name string) *SourceConfig {
	for i := range c.Sources {
		if c.Sources[i].Name == name {
			return &c.Sources[i]
		}
	}
	return nil
}

// StatePath returns a path inside the .loop folder.
func (c *Config) StatePath(parts ...string) string {
	return filepath.Join(append([]string{c.Dir, StateDir}, parts...)...)
}

// Resolve turns a path relative to loop.yaml into an absolute path.
func (c *Config) Resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return filepath.Join(c.Dir, p)
}
