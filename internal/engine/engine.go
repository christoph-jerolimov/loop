// Package engine drives a run through checkout, agent session, PR,
// monitoring, fix rounds, merge and close.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/christoph-jerolimov/loop/internal/agent"
	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/ghapi"
	"github.com/christoph-jerolimov/loop/internal/gitx"
	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/prompt"
	"github.com/christoph-jerolimov/loop/internal/source"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// Engine holds everything a run needs.
type Engine struct {
	Cfg     *config.Config
	Sources source.Set
	Store   *state.Store
	Runner  agent.Runner
	Out     io.Writer
	// Interactive lets gates ask on the terminal instead of parking the run.
	Interactive bool
	// Gate is called at a gate when Interactive; it returns true to proceed.
	Gate func(r *state.Run, gate string) bool

	gh     *ghapi.Client
	ghOnce sync.Once
	ghErr  error
	self   string

	heavy chan struct{}
}

// New wires an engine from the config.
func New(cfg *config.Config, sources source.Set, out io.Writer) (*Engine, error) {
	runner, err := agent.New(cfg.Agent.Runner)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = io.Discard
	}
	return &Engine{
		Cfg: cfg, Sources: sources, Store: state.NewStore(cfg.StatePath()), Runner: runner, Out: out,
		heavy: make(chan struct{}, cfg.Workflow.Concurrency),
	}, nil
}

// GitHub returns the API client for the target repository.
func (e *Engine) GitHub() (*ghapi.Client, error) {
	e.ghOnce.Do(func() {
		e.gh, e.ghErr = ghapi.New(e.Cfg.Repo.GitHub)
		if e.ghErr == nil {
			e.self, _ = e.gh.Viewer(context.Background())
		}
	})
	return e.gh, e.ghErr
}

func (e *Engine) logf(r *state.Run, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.Log("%s", msg)
	fmt.Fprintf(e.Out, "[%s] %s\n", shortID(r), msg)
}

func shortID(r *state.Run) string {
	if r.Item != nil {
		return r.Item.ID
	}
	return r.ID
}

// Readiness explains whether an item can be started.
type Readiness struct {
	Ready      bool
	InProgress bool
	ActiveRun  *state.Run
	OpenDeps   []string
	DepErrors  []string
}

// Check evaluates dependencies and claims for an item.
func (e *Engine) Check(ctx context.Context, it *item.Item) Readiness {
	rd := Readiness{}
	if it.InProgress {
		rd.InProgress = true
	}
	if r, _ := e.Store.ForItem(it.ID); r != nil {
		rd.ActiveRun = r
	}
	for _, d := range e.Sources.Dependencies(ctx, it) {
		if d.Err != nil {
			rd.DepErrors = append(rd.DepErrors, fmt.Sprintf("%s (%v)", d.Ref, d.Err))
			rd.OpenDeps = append(rd.OpenDeps, d.Ref)
		} else if !d.Closed {
			rd.OpenDeps = append(rd.OpenDeps, d.Ref)
		}
	}
	rd.Ready = !rd.InProgress && rd.ActiveRun == nil && len(rd.OpenDeps) == 0 && !it.Closed
	return rd
}

// Start creates a run for the item. With force, open dependencies and
// in-progress markers are ignored (an existing active run never is).
func (e *Engine) Start(ctx context.Context, it *item.Item, force bool) (*state.Run, error) {
	rd := e.Check(ctx, it)
	if rd.ActiveRun != nil {
		return nil, fmt.Errorf("item %s already has active run %s (%s)", it.ID, rd.ActiveRun.ID, rd.ActiveRun.Phase)
	}
	if it.Closed {
		return nil, fmt.Errorf("item %s is closed", it.ID)
	}
	if !force {
		if rd.InProgress {
			return nil, fmt.Errorf("item %s is marked in progress%s; use --force to run anyway", it.ID, claimedBy(it))
		}
		if len(rd.OpenDeps) > 0 {
			return nil, fmt.Errorf("item %s depends on open items: %s; use --force to run anyway", it.ID, strings.Join(rd.OpenDeps, ", "))
		}
	}
	r, err := e.Store.Create(it, e.Runner.Name())
	if err != nil {
		return nil, err
	}
	r.Model = it.Model
	if r.Model == "" {
		r.Model = e.Cfg.Agent.Model
	}
	e.logf(r, "run %s created for %q", r.ID, it.Title)
	return r, e.Store.Save(r)
}

func claimedBy(it *item.Item) string {
	if it.ClaimedBy != "" {
		return " by run " + it.ClaimedBy
	}
	return ""
}

// Drive advances the run until it waits (poll or gate) or ends.
func (e *Engine) Drive(ctx context.Context, r *state.Run) error {
	unlock, err := r.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	for r.Phase.Active() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wait, err := e.Step(ctx, r)
		if serr := e.Store.Save(r); serr != nil {
			return serr
		}
		if err != nil {
			return err
		}
		if wait {
			return nil
		}
	}
	return nil
}

// Step runs one phase. wait=true means the run parked itself (gate or
// next poll) and Drive should return.
func (e *Engine) Step(ctx context.Context, r *state.Run) (wait bool, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic in phase %s: %v", r.Phase, rec)
			r.Fail(err)
		}
	}()
	// A parked run waits until its gate is approved (loop approve, an
	// interactive answer, or a PR comment). The approval is consumed by
	// gate() when the phase reaches the gate again, so the phase handler
	// re-runs and continues past it.
	if r.Gate != "" && r.GateApproved != r.Gate {
		e.checkPRCommands(ctx, r)
		if r.GateApproved != r.Gate {
			return true, nil
		}
	}
	switch r.Phase {
	case state.PhaseQueued, state.PhaseCheckout, state.PhaseSetup, state.PhaseSession, state.PhaseVerify, state.PhasePR, state.PhaseFix:
		return e.heavyStep(ctx, r)
	case state.PhaseMonitor:
		return e.monitor(ctx, r)
	case state.PhaseMerge:
		return e.merge(ctx, r)
	case state.PhaseClose:
		return e.closeItem(ctx, r)
	case state.PhaseCleanup:
		return false, e.cleanup(ctx, r)
	}
	return false, fmt.Errorf("unknown phase %q", r.Phase)
}

func (e *Engine) heavyStep(ctx context.Context, r *state.Run) (bool, error) {
	select {
	case e.heavy <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-e.heavy }()
	var err error
	switch r.Phase {
	case state.PhaseQueued, state.PhaseCheckout:
		err = e.checkout(ctx, r)
	case state.PhaseSetup:
		err = e.setup(ctx, r)
	case state.PhaseSession:
		err = e.session(ctx, r)
	case state.PhaseVerify:
		return e.verify(ctx, r)
	case state.PhasePR:
		return e.openPR(ctx, r)
	case state.PhaseFix:
		return e.fix(ctx, r)
	}
	if err != nil {
		e.abandon(ctx, r, err)
		return false, err
	}
	return false, nil
}

// abandon fails the run, releases the claim, leaves a note on the item
// and runs steps.failed.
func (e *Engine) abandon(ctx context.Context, r *state.Run, err error) {
	r.Fail(err)
	e.logf(r, "failed: %v", err)
	if src := e.Sources.ByName(r.Item.Source); src != nil {
		_ = src.Comment(ctx, r.Item, fmt.Sprintf("loop run `%s` failed: %v", r.ID, err))
		_ = src.Release(ctx, r.Item)
	}
	e.notify(ctx, r, e.Cfg.Steps.Failed, "failed")
}

// block parks the run as blocked and runs steps.blocked.
func (e *Engine) block(ctx context.Context, r *state.Run, reason string) {
	r.Block(reason)
	e.notify(ctx, r, e.Cfg.Steps.Blocked, "blocked")
}

// notify runs notification steps; their failures are logged, never fatal.
func (e *Engine) notify(ctx context.Context, r *state.Run, steps []config.Step, phase string) {
	if len(steps) == 0 {
		return
	}
	if r.Workdir == "" {
		return
	}
	if err := e.runSteps(ctx, r, steps, phase); err != nil {
		e.logf(r, "%s step failed: %v", phase, err)
	}
}

// env builds the environment for hooks, steps and agent sessions.
func (e *Engine) env(r *state.Run) map[string]string {
	m := map[string]string{
		"LOOP_PROJECT":      e.Cfg.Name,
		"LOOP_PROJECT_DIR":  e.Cfg.Dir,
		"LOOP_WORKDIR":      r.Workdir,
		"LOOP_BRANCH":       r.Branch,
		"LOOP_BASE":         e.Cfg.Repo.Base,
		"LOOP_ITEM_ID":      r.Item.ID,
		"LOOP_ITEM_TITLE":   r.Item.Title,
		"LOOP_ITEM_URL":     r.Item.URL,
		"LOOP_RUN_ID":       r.ID,
		"LOOP_RUN_DIR":      r.Dir(),
		"LOOP_SUMMARY_FILE": r.SummaryFile(),
		"LOOP_RUN_PHASE":    string(r.Phase),
		"LOOP_RUN_ERROR":    r.Error,
	}
	if r.PR != nil {
		m["LOOP_PR_URL"] = r.PR.URL
		m["LOOP_PR_NUMBER"] = fmt.Sprint(r.PR.Number)
	}
	for k, v := range e.Cfg.Agent.Env {
		m[k] = v
	}
	return m
}

func (e *Engine) baseRepo() string { return e.Cfg.StatePath("repo") }

// pushRemote is the git remote branches are pushed to.
func (e *Engine) pushRemote() string {
	if e.Cfg.Repo.Fork != "" {
		return "fork"
	}
	return "origin"
}

// remotes lists the remotes a branch name must be free on.
func (e *Engine) remotes() []string {
	if e.Cfg.Repo.Fork != "" {
		return []string{"origin", "fork"}
	}
	return []string{"origin"}
}

// ensureFork registers the fork remote in the given repository.
func (e *Engine) ensureFork(ctx context.Context, repo string) error {
	if e.Cfg.Repo.Fork == "" {
		return nil
	}
	return gitx.EnsureRemote(ctx, repo, "fork", e.Cfg.Repo.PushURL)
}

func (e *Engine) checkout(ctx context.Context, r *state.Run) error {
	r.SetPhase(state.PhaseCheckout, "")
	it := r.Item
	want := e.Cfg.Repo.BranchPrefix + item.Slug(it.NativeID+"-"+it.Title, 60)
	var branch string
	var err error
	if e.Cfg.Repo.Workdir == "worktree" {
		e.logf(r, "updating base clone %s", e.baseRepo())
		if err := gitx.EnsureBaseClone(ctx, e.Cfg.Repo.URL, e.baseRepo(), e.Cfg.Repo.Base); err != nil {
			return err
		}
		if err := e.ensureFork(ctx, e.baseRepo()); err != nil {
			return err
		}
		branch, err = gitx.UniqueBranch(ctx, e.baseRepo(), want, e.remotes()...)
		if err != nil {
			return err
		}
		r.Branch = branch
		r.Workdir = e.Cfg.StatePath("workdirs", strings.TrimPrefix(branch, e.Cfg.Repo.BranchPrefix))
		e.logf(r, "creating worktree %s on branch %s", r.Workdir, branch)
		if err := gitx.AddWorktree(ctx, e.baseRepo(), r.Workdir, branch, e.Cfg.Repo.Base); err != nil {
			return err
		}
	} else {
		r.Workdir = e.Cfg.StatePath("workdirs", strings.TrimPrefix(want, e.Cfg.Repo.BranchPrefix))
		e.logf(r, "cloning into %s", r.Workdir)
		if err := gitx.Clone(ctx, e.Cfg.Repo.URL, r.Workdir, want+"-tmp", e.Cfg.Repo.Base); err != nil {
			return err
		}
		if err := e.ensureFork(ctx, r.Workdir); err != nil {
			return err
		}
		branch, err = gitx.UniqueBranch(ctx, r.Workdir, want, e.remotes()...)
		if err != nil {
			return err
		}
		if _, err := gitx.Run(ctx, r.Workdir, "branch", "-m", branch); err != nil {
			return err
		}
		r.Branch = branch
	}
	if err := e.linkSkills(r); err != nil {
		return err
	}
	if err := e.writeAgentSettings(r); err != nil {
		return err
	}
	if src := e.Sources.ByName(it.Source); src != nil {
		if err := src.Claim(ctx, it, r.ID); err != nil {
			return fmt.Errorf("claim: %w", err)
		}
	}
	r.SetPhase(state.PhaseSetup, "")
	return nil
}

// writeAgentSettings puts the permission rules into the workdir so a
// headless Claude session can run tests and commit without prompting.
func (e *Engine) writeAgentSettings(r *state.Run) error {
	if e.Cfg.Agent.Runner != "claude" {
		return nil
	}
	b, err := agent.ClaudeSettings(e.Cfg.Agent.PermissionMode, e.Cfg.Agent.Allow, e.Cfg.Agent.Deny)
	if err != nil {
		return err
	}
	dst := filepath.Join(r.Workdir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return err
	}
	e.ensureExcluded(r.Workdir, ".claude/settings.local.json")
	e.logf(r, "wrote %d allow / %d deny permission rules to .claude/settings.local.json", len(e.Cfg.Agent.Allow), len(e.Cfg.Agent.Deny))
	return nil
}

// linkSkills symlinks configured skill folders into .claude/skills of the workdir.
func (e *Engine) linkSkills(r *state.Run) error {
	for _, s := range e.Cfg.Agent.Skills {
		src := e.Cfg.Resolve(s)
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("skill %s: %w", s, err)
		}
		dst := filepath.Join(r.Workdir, ".claude", "skills", filepath.Base(src))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		_ = os.Remove(dst)
		if err := os.Symlink(src, dst); err != nil {
			return err
		}
		e.logf(r, "linked skill %s", filepath.Base(src))
		e.ensureExcluded(r.Workdir, ".claude/skills/"+filepath.Base(src))
	}
	return nil
}

// ensureExcluded keeps linked skills out of git via .git/info/exclude.
// Worktrees share that file with the base clone, so a pattern is only
// appended when it is not there yet.
func (e *Engine) ensureExcluded(workdir, pattern string) {
	out, err := gitx.Run(context.Background(), workdir, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(workdir, out)
	}
	_ = os.MkdirAll(filepath.Dir(out), 0o755)
	existing, _ := os.ReadFile(out)
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == pattern {
			return
		}
	}
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		fmt.Fprintln(f)
	}
	fmt.Fprintln(f, pattern)
	f.Close()
}

func (e *Engine) setup(ctx context.Context, r *state.Run) error {
	r.SetPhase(state.PhaseSetup, "")
	if err := e.runSteps(ctx, r, e.Cfg.Steps.Setup, "setup"); err != nil {
		return err
	}
	r.SetPhase(state.PhaseSession, "")
	return nil
}

// runSteps executes a step list in order and stops at the first failure.
func (e *Engine) runSteps(ctx context.Context, r *state.Run, steps []config.Step, phase string) error {
	for _, st := range steps {
		if _, err := e.runStep(ctx, r, st, phase); err != nil {
			return err
		}
	}
	return nil
}

// runStep executes one configured step inside the workdir. For run and
// script steps it returns the combined output and an error on non-zero exit.
func (e *Engine) runStep(ctx context.Context, r *state.Run, st config.Step, phase string) (string, error) {
	name := st.Label()
	if st.Run != "" || st.Script != "" {
		var cmd *exec.Cmd
		if st.Run != "" {
			e.logf(r, "%s step: %s", phase, name)
			cmd = exec.CommandContext(ctx, "sh", "-c", st.Run)
		} else {
			script := e.Cfg.Resolve(st.Script)
			if _, err := os.Stat(script); err != nil {
				return "", fmt.Errorf("step %q: %w", name, err)
			}
			e.logf(r, "%s script: %s", phase, st.Script)
			cmd = exec.CommandContext(ctx, script)
		}
		cmd.Dir = r.Workdir
		cmd.Env = os.Environ()
		for k, v := range e.env(r) {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		var buf strings.Builder
		cmd.Stdout = io.MultiWriter(&buf, e.Out)
		cmd.Stderr = io.MultiWriter(&buf, e.Out)
		if err := cmd.Run(); err != nil {
			return buf.String(), fmt.Errorf("step %q failed: %w", name, err)
		}
		return buf.String(), nil
	}
	e.logf(r, "%s agent step: %s", phase, name)
	d := e.data(r)
	text, err := prompt.RenderFile(name, e.Cfg.Resolve(st.Agent), d)
	if err != nil {
		return "", err
	}
	res, err := e.runAgent(ctx, r, "step-"+item.Slug(name, 20), text, st.Model, st.Timeout.D())
	if err != nil {
		return "", err
	}
	return res.Output, nil
}

// data builds template data for the run.
func (e *Engine) data(r *state.Run) *prompt.Data {
	d := &prompt.Data{
		Project: e.Cfg.Name, Item: r.Item, Branch: r.Branch, Base: e.Cfg.Repo.Base, Workdir: r.Workdir,
		RunID: r.ID, SummaryFile: r.SummaryFile(), Attempt: r.Attempt, Round: r.FixRounds,
	}
	if sc := e.Cfg.Source(r.Item.Source); sc != nil && sc.Comments != nil && !*sc.Comments {
		cp := *r.Item
		cp.Comments = nil
		d.Item = &cp
	}
	if r.PR != nil {
		d.PR = &prompt.PRRef{Number: r.PR.Number, URL: r.PR.URL}
	}
	if b, err := os.ReadFile(r.SummaryFile()); err == nil {
		d.Summary = strings.TrimSpace(string(b))
	}
	return d
}

// runAgent executes one agent session and records it on the run. extra
// adds environment variables for this session only.
func (e *Engine) runAgent(ctx context.Context, r *state.Run, kind, text, model string, timeout time.Duration, extra ...map[string]string) (*agent.Result, error) {
	if model == "" {
		model = r.Model
	}
	if timeout == 0 {
		timeout = e.Cfg.Agent.Timeout.D()
	}
	n := len(r.Sessions) + 1
	logPath := filepath.Join(r.Dir(), fmt.Sprintf("session-%02d-%s.log", n, kind))
	promptPath := filepath.Join(r.Dir(), fmt.Sprintf("session-%02d-%s.prompt.md", n, kind))
	_ = os.WriteFile(promptPath, []byte(text), 0o644)
	logf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	sess := state.Session{Kind: kind, Started: time.Now(), LogFile: logPath}
	e.logf(r, "starting %s session (%s%s)", kind, e.Runner.Name(), modelSuffix(model))
	env := e.env(r)
	for _, m := range extra {
		for k, v := range m {
			env[k] = v
		}
	}
	res, err := e.Runner.Run(ctx, agent.Options{
		Workdir: r.Workdir, Prompt: text, PromptFile: promptPath, Model: model, PermissionMode: e.Cfg.Agent.PermissionMode,
		MaxTurns: e.Cfg.Agent.MaxTurns, Timeout: timeout, Env: env, EnvPassthrough: e.Cfg.Agent.EnvPassthrough, ExtraArgs: e.Cfg.Agent.ExtraArgs,
		Command: e.Cfg.Agent.Command, Log: logf, Progress: e.Out,
	})
	sess.Ended = time.Now()
	if res != nil {
		sess.ID, sess.CostUSD, sess.Turns = res.SessionID, res.CostUSD, res.Turns
	}
	if err != nil {
		sess.Error = err.Error()
	}
	r.Sessions = append(r.Sessions, sess)
	_ = e.Store.Save(r)
	if res != nil && res.SessionID != "" {
		e.logf(r, "session %s ended after %s; join with: %s", sess.ID, res.Duration.Round(time.Second), e.Runner.JoinCommand(r.Workdir, res.SessionID))
	}
	if err != nil {
		return res, err
	}
	if res.IsError {
		return res, fmt.Errorf("agent reported an error: %s", prompt.Trunc(res.Output, 500))
	}
	return res, nil
}

func modelSuffix(m string) string {
	if m == "" {
		return ""
	}
	return ", model " + m
}

func (e *Engine) session(ctx context.Context, r *state.Run) error {
	r.SetPhase(state.PhaseSession, "")
	for r.Attempt < e.Cfg.Agent.Attempts {
		r.Attempt++
		text, err := prompt.RenderFile(prompt.TplSession, e.Cfg.Resolve(e.Cfg.Prompts.Session), e.data(r))
		if err != nil {
			return err
		}
		_, err = e.runAgent(ctx, r, "session", text, "", 0)
		if err != nil {
			e.logf(r, "attempt %d failed: %v", r.Attempt, err)
			continue
		}
		if err := e.commitLeftovers(ctx, r, "loop: commit remaining changes from agent session"); err != nil {
			return err
		}
		n, err := gitx.AheadOfBase(ctx, r.Workdir, e.Cfg.Repo.Base)
		if err != nil {
			return err
		}
		if n == 0 {
			e.logf(r, "attempt %d produced no commits", r.Attempt)
			continue
		}
		r.SetPhase(state.PhaseVerify, fmt.Sprintf("%d commit(s)", n))
		return nil
	}
	return fmt.Errorf("no usable result after %d attempt(s)", r.Attempt)
}

func (e *Engine) commitLeftovers(ctx context.Context, r *state.Run, msg string) error {
	if *e.Cfg.PR.CommitUncommitted {
		return gitx.CommitAll(ctx, r.Workdir, msg)
	}
	dirty, err := gitx.HasUncommitted(ctx, r.Workdir)
	if err != nil {
		return err
	}
	if dirty {
		return errors.New("agent left uncommitted changes and pr.commit_uncommitted is false")
	}
	return nil
}

// verify is the phase after the initial session: run the verify steps,
// then continue to the PR gate.
func (e *Engine) verify(ctx context.Context, r *state.Run) (bool, error) {
	r.SetPhase(state.PhaseVerify, "")
	if err := e.runVerify(ctx, r); err != nil {
		e.abandon(ctx, r, err)
		return false, err
	}
	if !e.gate(r, config.GateBeforePR) {
		return true, nil
	}
	r.SetPhase(state.PhasePR, "")
	return false, nil
}

// runVerify runs steps.verify until they all pass. A failing run or
// script step starts a session with the verify template and then the
// whole list runs again; each attempt counts as a fix round. It returns
// an error when a step keeps failing after the rounds are used up, or an
// agent step fails. It is used after the initial session and after every
// fix round, so a fix can never push what the initial round would have
// rejected.
func (e *Engine) runVerify(ctx context.Context, r *state.Run) error {
	for {
		failed, out, err := e.runVerifyOnce(ctx, r)
		if err != nil {
			return err
		}
		if failed == nil {
			return nil
		}
		if r.FixRounds >= e.Cfg.Workflow.FixRounds {
			return fmt.Errorf("verify step %q still failing after %d fix rounds", failed.Label(), r.FixRounds)
		}
		r.FixRounds++
		d := e.data(r)
		d.Round = r.FixRounds
		d.VerifyStep = failed.Label()
		d.VerifyOutput = out
		text, rerr := prompt.RenderFile(prompt.TplVerify, e.Cfg.Resolve(e.Cfg.Prompts.Verify), d)
		if rerr != nil {
			return rerr
		}
		if _, aerr := e.runAgent(ctx, r, "verify-fix", text, "", 0); aerr != nil {
			return aerr
		}
		if cerr := e.commitLeftovers(ctx, r, "loop: fix verification failure"); cerr != nil {
			return cerr
		}
	}
}

// runVerifyOnce runs the verify list once. It returns the first failing
// run/script step with its output, or an error for agent-step failures.
func (e *Engine) runVerifyOnce(ctx context.Context, r *state.Run) (*config.Step, string, error) {
	for i := range e.Cfg.Steps.Verify {
		st := e.Cfg.Steps.Verify[i]
		out, err := e.runStep(ctx, r, st, "verify")
		if err == nil {
			continue
		}
		if st.Agent != "" {
			return nil, "", err
		}
		return &st, out, nil
	}
	return nil, "", nil
}

// gate returns true when the run may continue past the named gate.
func (e *Engine) gate(r *state.Run, name string) bool {
	if !e.Cfg.HasGate(name) {
		return true
	}
	if r.GateApproved == name {
		r.Gate, r.GateApproved = "", ""
		return true
	}
	if e.Interactive && e.Gate != nil {
		if e.Gate(r, name) {
			return true
		}
		e.block(context.Background(), r, "gate "+name+" declined")
		return false
	}
	r.Gate = name
	r.NextPoll = time.Now().Add(e.Cfg.Workflow.PollInterval.D())
	e.logf(r, "waiting at gate %s; continue with: loop approve %s", name, r.ID)
	e.gateNote(context.Background(), r, name)
	return false
}
