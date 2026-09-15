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

	"github.com/christoph-jerolimov/loop/internal/agent"
	"github.com/christoph-jerolimov/loop/internal/item"
)

// FileName is the project configuration file.
const FileName = "loop.yaml"

// StateDir is the git-ignored folder next to loop.yaml that holds
// the base clone, workdirs and run state.
const StateDir = ".loop"

// Config is the root of loop.yaml.
type Config struct {
	Name      string         `yaml:"name"`
	Repo      Repo           `yaml:"repo"`
	Sources   []SourceConfig `yaml:"sources"`
	Prompts   Prompts        `yaml:"prompts"`
	Agent     Agent          `yaml:"agent"`
	Steps     Steps          `yaml:"steps"`
	PR        PR             `yaml:"pr"`
	Workflow  Workflow       `yaml:"workflow"`
	Budget    Budget         `yaml:"budget"`
	Retention Retention      `yaml:"retention"`

	// Dir is the directory that contains loop.yaml. Not part of the file.
	Dir string `yaml:"-"`
}

// Repo describes the target repository.
type Repo struct {
	URL          string `yaml:"url"`
	Base         string `yaml:"base"`
	Workdir      string `yaml:"workdir"` // worktree | clone
	BranchPrefix string `yaml:"branch_prefix"`
	// GitHub is "owner/name". Derived from URL when it is a github.com URL.
	GitHub string `yaml:"github"`
	// GitLab is the project path ("group/subgroup/project"). Derived from
	// URL when its host is gitlab.com. Exactly one of GitHub and GitLab is
	// set; it decides where pull (merge) requests are opened.
	GitLab string `yaml:"gitlab"`
	// GitLabURL is the GitLab instance, https://gitlab.com by default or
	// the host of URL.
	GitLabURL string `yaml:"gitlab_url"`
	// Fork is the project ("owner/name") to push branches to when you have
	// no push access to the repository. Pull requests are then opened from
	// the fork against the repository.
	Fork string `yaml:"fork"`
	// PushURL is the git URL of the fork. Derived from URL and Fork when empty.
	PushURL string `yaml:"push_url"`
}

// Code hosts.
const (
	HostGitHub = "github"
	HostGitLab = "gitlab"
)

// Host is the code host of the repository: github or gitlab.
func (r Repo) Host() string {
	if r.GitLab != "" {
		return HostGitLab
	}
	return HostGitHub
}

// Project is the repository's path on its host: "owner/name" on GitHub,
// the project path on GitLab.
func (r Repo) Project() string {
	if r.GitLab != "" {
		return r.GitLab
	}
	return r.GitHub
}

// ForkOwner returns the owner part of Fork.
func (r Repo) ForkOwner() string {
	owner, _, _ := strings.Cut(r.Fork, "/")
	return owner
}

// PushRepo is the project branches are pushed to: the fork when
// configured, otherwise the repository itself.
func (r Repo) PushRepo() string {
	if r.Fork != "" {
		return r.Fork
	}
	return r.Project()
}

// deriveForkURL rewrites a clone URL to point at the fork.
func deriveForkURL(url, fork string) string {
	if m := cloneURLRe.FindStringSubmatch(url); m != nil {
		return m[1] + fork + m[3]
	}
	return ""
}

// SourceConfig configures one backlog source. Fields that do not apply to
// the type are ignored.
type SourceConfig struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"` // markdown | github | gitlab | jira

	// markdown
	Path string `yaml:"path"`

	// github and gitlab
	Repo string `yaml:"repo"` // owner/name or group/project, defaults to the repository

	// jira (site) and gitlab (instance, defaults to repo.gitlab_url)
	URL string `yaml:"url"`
	JQL string `yaml:"jql"`
	// Transitions maps loop states to Jira transition names.
	Transitions map[string]string `yaml:"transitions"`

	// shared filters and claiming
	Labels     []string `yaml:"labels"`
	Claim      bool     `yaml:"claim"`
	ClaimLabel string   `yaml:"claim_label"`
	// Comments says whose ticket comments are loaded into the prompt data.
	// Unset means all for markdown and Jira and writers for GitHub and
	// GitLab: only comments by the repository owner and by members with
	// write access, because anyone can comment on a public repository and
	// comments reach the agent verbatim.
	Comments CommentsPolicy `yaml:"comments"`
}

// CommentsPolicy says whose ticket comments are loaded.
type CommentsPolicy string

const (
	// CommentsAll loads every comment.
	CommentsAll CommentsPolicy = "all"
	// CommentsWriters loads comments by the repository owner and by
	// collaborators with write access (GitHub only).
	CommentsWriters CommentsPolicy = "writers"
	// CommentsNone loads no comments.
	CommentsNone CommentsPolicy = "none"
)

// UnmarshalYAML accepts exactly the policy names.
func (p *CommentsPolicy) UnmarshalYAML(n *yaml.Node) error {
	switch strings.TrimSpace(n.Value) {
	case "", "~", "null":
		*p = ""
	case "all":
		*p = CommentsAll
	case "writers":
		*p = CommentsWriters
	case "none":
		*p = CommentsNone
	default:
		return fmt.Errorf("comments must be all, writers or none, got %q", n.Value)
	}
	return nil
}

// Loaded reports whether any comments are loaded under the policy.
func (p CommentsPolicy) Loaded() bool { return p != CommentsNone }

// Spec is the harness description handed to the agent package.
func (a Agent) Spec() agent.Spec {
	return agent.Spec{Runner: a.Runner, Command: a.Command, Args: a.Args, PromptVia: a.PromptVia, SessionID: a.SessionID, Resume: a.Resume, ExtraArgs: a.ExtraArgs}
}

// Prompts points to template files, relative to loop.yaml. Empty values
// fall back to the embedded defaults.
type Prompts struct {
	Session    string `yaml:"session"`
	Plan       string `yaml:"plan"`
	SelfReview string `yaml:"self_review"`
	Review     string `yaml:"review"`
	CI         string `yaml:"ci"`
	Conflict   string `yaml:"conflict"`
	Verify     string `yaml:"verify"`
}

// Agent configures the coding agent runner.
type Agent struct {
	// Runner is a built-in harness profile (claude, cursor, codex, gemini,
	// aider, opencode, copilot, amp) or custom. The --runner flag and the
	// LOOP_RUNNER variable override it.
	Runner  string `yaml:"runner"`
	Command string `yaml:"command"`
	// Args, PromptVia, SessionID and Resume define a custom harness or
	// override one field of a built-in profile. See agent.Runner.
	Args           []string `yaml:"args"`
	PromptVia      string   `yaml:"prompt_via"`
	SessionID      string   `yaml:"session_id"`
	Resume         string   `yaml:"resume"`
	Model          string   `yaml:"model"`
	PermissionMode string   `yaml:"permission_mode"`
	// Allow and Deny are Claude Code permission rules written to
	// <workdir>/.claude/settings.local.json before a session starts, so a
	// headless session can run the commands it needs without prompting.
	// Nil means the built-in defaults; an empty list means none.
	Allow    []string          `yaml:"allow"`
	Deny     []string          `yaml:"deny"`
	Timeout  Duration          `yaml:"timeout"`
	MaxTurns int               `yaml:"max_turns"`
	Attempts int               `yaml:"attempts"`
	Skills   []string          `yaml:"skills"`
	Env      map[string]string `yaml:"env"`
	// EnvPassthrough lists environment variable names or globs (for example
	// DATABASE_URL, MY_APP_*) that sessions inherit from loop's environment
	// on top of the built-in allowlist. Credentials such as GITHUB_TOKEN are
	// never inherited unless listed here.
	EnvPassthrough []string `yaml:"env_passthrough"`
	ExtraArgs      []string `yaml:"extra_args"`
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
	Merge             string   `yaml:"merge"` // manual | when-green | when-green-and-approved | auto-merge
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
	// CIRerun re-runs the failed Actions jobs once per head commit before a
	// CI fix round, so a flaky job does not cost an agent session.
	CIRerun *bool `yaml:"ci_rerun"`
	// PRStatus posts loop's phase, fix rounds and outcome as a commit
	// status named "loop" on the PR head, so reviewers see it on GitHub.
	PRStatus *bool `yaml:"pr_status"`
	// Cleanup removes the workdir once the item is closed.
	Cleanup *bool `yaml:"cleanup"`
	// PRCommands lets collaborators with push access drive a run from the
	// pull request: "/loop approve" releases a gate, "/loop resume" restarts
	// a blocked run.
	PRCommands *bool `yaml:"pr_commands"`
	// Plan runs a planning session before the implementation: the agent
	// explores the repository, writes a short plan with an estimate, and
	// loop posts it on the ticket. With the before-code gate, the run then
	// waits for a human to approve the plan.
	Plan bool `yaml:"plan"`
	// SelfReview reviews the branch's diff in a separate session after
	// verify and hands the findings to one fix round before the PR opens.
	SelfReview bool `yaml:"self_review"`
	// OnHumanPush says what happens when someone other than loop pushes to
	// the run branch: pause (park the run with a note, the default) or
	// continue (fast-forward the worktree and keep driving on top).
	OnHumanPush string `yaml:"on_human_push"`
}

// Budget caps what a run and a project may spend. Zero means unlimited.
// Costs come from harnesses that report them (Claude Code does); time
// budgets count agent session wall clock and work with every harness.
type Budget struct {
	// RunCost is the most one run may spend in USD across all its sessions.
	RunCost float64 `yaml:"run_cost"`
	// RunTime is the most agent session time one run may use.
	RunTime Duration `yaml:"run_time"`
	// DailyCost is the most the project may spend per calendar day.
	DailyCost float64 `yaml:"daily_cost"`
	// DailyRuns is the most runs the project may start per calendar day.
	DailyRuns int `yaml:"daily_runs"`
}

// Retention says how long finished runs keep what they leave on disk. Run
// folders (run.yaml, logs, prompts) are always kept; only checkouts are
// removed.
type Retention struct {
	// Workdirs removes the checkout of a done, failed or blocked run this
	// long after the run last changed. loop watch applies it; loop clean
	// --older-than uses the same rule by hand. Unset means
	// DefaultWorkdirRetention; an explicit 0 keeps every checkout until
	// loop clean.
	Workdirs *Duration `yaml:"workdirs"`
}

// WorkdirAge is the configured retention of checkouts; 0 means keep.
func (r Retention) WorkdirAge() time.Duration {
	if r.Workdirs == nil {
		return 0
	}
	return r.Workdirs.D()
}

// DefaultWorkdirRetention is how long finished runs keep their checkout
// when loop.yaml does not say.
const DefaultWorkdirRetention = 7 * 24 * time.Hour

// Reactions to a human pushing to the run branch.
const (
	HumanPushPause    = "pause"
	HumanPushContinue = "continue"
)

// Duration is a yaml-friendly time.Duration.
type Duration time.Duration

// UnmarshalYAML parses "45m", "1h30m", "2d" etc.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := item.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%w (Go syntax such as 45m or 1h30m, plus d for days and w for weeks)", err)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML renders the duration in Go syntax.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// DefaultAllow lets a headless session inspect and commit its work. Add the
// project's own test and build commands in loop.yaml (agent.allow).
var DefaultAllow = []string{
	"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
	"Bash(git add:*)", "Bash(git commit:*)", "Bash(git restore:*)", "Bash(git stash:*)",
	"Bash(git branch:*)", "Bash(git merge:*)", "Bash(git rm:*)", "Bash(git mv:*)",
}

// DefaultDeny keeps the agent from doing what loop does itself.
var DefaultDeny = []string{
	"Bash(git push:*)", "Bash(gh pr:*)", "Bash(gh api:*)", "Bash(git reset --hard:*)",
}

// PermissionModes accepted by the Claude runner.
var PermissionModes = []string{"default", "acceptEdits", "bypassPermissions", "plan"}

// Merge policies.
const (
	MergeManual           = "manual"
	MergeWhenGreen        = "when-green"
	MergeWhenGreenApprove = "when-green-and-approved"
	MergeAuto             = "auto-merge"
)

// Gate names.
const (
	GateBeforeCode  = "before-code"
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

var (
	githubURLRe = regexp.MustCompile(`(?:github\.com[:/])([^/]+)/([^/]+?)(?:\.git)?/?$`)
	// cloneURLRe splits a clone URL into scheme and host, project path
	// and the optional .git suffix: https://host/, git@host: or
	// ssh://git@host/ followed by group/project.
	cloneURLRe = regexp.MustCompile(`^((?:https?://|ssh://)[^/]+/|[^@/]+@[^:/]+:)(.+?)(\.git)?/?$`)
)

// hostOf returns the host name of a clone URL, "" when it is not one.
func hostOf(url string) string {
	m := cloneURLRe.FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	h := strings.TrimSuffix(m[1], "/")
	h = h[strings.LastIndexAny(h, "/@")+1:]
	return strings.TrimSuffix(h, ":")
}

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
	if c.Repo.GitHub == "" && c.Repo.GitLab == "" {
		if m := githubURLRe.FindStringSubmatch(c.Repo.URL); m != nil {
			c.Repo.GitHub = m[1] + "/" + m[2]
		} else if h := hostOf(c.Repo.URL); strings.Contains(h, "gitlab") {
			c.Repo.GitLab = cloneURLRe.FindStringSubmatch(c.Repo.URL)[2]
		}
	}
	if c.Repo.GitLab != "" && c.Repo.GitLabURL == "" {
		c.Repo.GitLabURL = "https://gitlab.com"
		if h := hostOf(c.Repo.URL); h != "" && h != "github.com" {
			c.Repo.GitLabURL = "https://" + h
		}
	}
	c.Repo.GitLabURL = strings.TrimRight(c.Repo.GitLabURL, "/")
	if c.Repo.Fork != "" && c.Repo.PushURL == "" {
		c.Repo.PushURL = deriveForkURL(c.Repo.URL, c.Repo.Fork)
	}
	for i := range c.Sources {
		s := &c.Sources[i]
		if s.Name == "" {
			s.Name = s.Type
		}
		if s.Type == "github" && s.Repo == "" {
			s.Repo = c.Repo.GitHub
		}
		if s.Type == "gitlab" {
			if s.Repo == "" {
				s.Repo = c.Repo.GitLab
			}
			if s.URL == "" {
				s.URL = c.Repo.GitLabURL
			}
			s.URL = strings.TrimRight(s.URL, "/")
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
		if s.Comments == "" {
			if s.Type == "github" || s.Type == "gitlab" {
				s.Comments = CommentsWriters
			} else {
				s.Comments = CommentsAll
			}
		}
	}
	if c.Agent.Runner == "" {
		c.Agent.Runner = "claude"
	}
	if c.Agent.PermissionMode == "" {
		c.Agent.PermissionMode = "acceptEdits"
	}
	if c.Agent.Allow == nil {
		c.Agent.Allow = append([]string(nil), DefaultAllow...)
	}
	if c.Agent.Deny == nil {
		c.Agent.Deny = append([]string(nil), DefaultDeny...)
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
	if c.Workflow.PRCommands == nil {
		t := true
		c.Workflow.PRCommands = &t
	}
	if c.Workflow.CILogLines == nil {
		n := 200
		c.Workflow.CILogLines = &n
	}
	if c.Workflow.PRStatus == nil {
		t := true
		c.Workflow.PRStatus = &t
	}
	if c.Workflow.CIRerun == nil {
		t := true
		c.Workflow.CIRerun = &t
	}
	if c.Workflow.OnHumanPush == "" {
		c.Workflow.OnHumanPush = HumanPushPause
	}
	if c.Workflow.Gates == nil && c.Workflow.Merge != MergeManual {
		c.Workflow.Gates = []string{GateBeforeMerge}
	}
	if c.Workflow.Concurrency == 0 {
		c.Workflow.Concurrency = 1
	}
	if c.Retention.Workdirs == nil {
		d := Duration(DefaultWorkdirRetention)
		c.Retention.Workdirs = &d
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
		if s.Comments == CommentsWriters && s.Type != "github" && s.Type != "gitlab" {
			errs = append(errs, fmt.Errorf("source %s: comments: writers is only supported by github and gitlab sources", s.Name))
		}
		switch s.Type {
		case "markdown":
		case "github":
			if s.Repo == "" {
				errs = append(errs, fmt.Errorf("source %s: repo is required (owner/name)", s.Name))
			}
		case "gitlab":
			if s.Repo == "" {
				errs = append(errs, fmt.Errorf("source %s: repo is required (group/project)", s.Name))
			}
			if s.URL == "" {
				errs = append(errs, fmt.Errorf("source %s: url is required (the GitLab instance)", s.Name))
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
	runner, err := agent.Resolve(c.Agent.Spec())
	if err != nil {
		errs = append(errs, err)
	}
	if runner != nil && runner.SettingsFile != "" {
		ok := false
		for _, m := range PermissionModes {
			ok = ok || m == c.Agent.PermissionMode
		}
		if !ok {
			errs = append(errs, fmt.Errorf("agent.permission_mode must be one of %s; got %q", strings.Join(PermissionModes, ", "), c.Agent.PermissionMode))
		}
	}
	switch c.Workflow.Merge {
	case MergeManual, MergeWhenGreen, MergeWhenGreenApprove, MergeAuto:
	default:
		errs = append(errs, fmt.Errorf("workflow.merge must be one of manual, when-green, when-green-and-approved, auto-merge; got %q", c.Workflow.Merge))
	}
	switch c.Workflow.MergeMethod {
	case "squash", "merge", "rebase":
	default:
		errs = append(errs, fmt.Errorf("workflow.merge_method must be squash, merge or rebase; got %q", c.Workflow.MergeMethod))
	}
	if c.Budget.RunCost < 0 || c.Budget.DailyCost < 0 || c.Budget.DailyRuns < 0 || c.Budget.RunTime < 0 {
		errs = append(errs, errors.New("budget values must not be negative"))
	}
	if c.Retention.WorkdirAge() < 0 {
		errs = append(errs, errors.New("retention.workdirs must not be negative (0 keeps checkouts until loop clean)"))
	}
	switch c.Workflow.OnHumanPush {
	case HumanPushPause, HumanPushContinue:
	default:
		errs = append(errs, fmt.Errorf("workflow.on_human_push must be %s or %s, got %q", HumanPushPause, HumanPushContinue, c.Workflow.OnHumanPush))
	}
	for _, g := range c.Workflow.Gates {
		switch g {
		case GateBeforeCode, GateBeforePR, GateBeforeFix, GateBeforeMerge:
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
	switch {
	case c.Repo.GitHub != "" && c.Repo.GitLab != "":
		errs = append(errs, errors.New("repo.github and repo.gitlab are mutually exclusive"))
	case c.Repo.GitHub == "" && c.Repo.GitLab == "":
		errs = append(errs, errors.New("neither repo.github (owner/name) nor repo.gitlab (group/project) could be derived from repo.url; set one explicitly"))
	}
	if c.Repo.Fork != "" {
		if !strings.Contains(c.Repo.Fork, "/") {
			errs = append(errs, fmt.Errorf("repo.fork must be owner/name, got %q", c.Repo.Fork))
		}
		if c.Repo.PushURL == "" {
			errs = append(errs, errors.New("repo.push_url is required with repo.fork when repo.url is not a clone URL loop can rewrite"))
		}
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
