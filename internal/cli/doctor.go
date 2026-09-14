package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/christoph-jerolimov/loop/internal/agent"
	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/gitx"
	"github.com/christoph-jerolimov/loop/internal/host"
	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/jiraapi"
	"github.com/christoph-jerolimov/loop/internal/prompt"
	"github.com/christoph-jerolimov/loop/internal/source"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check the project configuration, tools, credentials and sources",
	Long: `Runs every check a run depends on before any worktree is created: loop.yaml
is valid and its referenced files exist, git and the agent CLI are on the
PATH, the repository and base branch are reachable, the GitHub or GitLab token works
and can push, Jira credentials work, and every source can be listed.

Exits non-zero when a check fails.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		d := &doctor{ctx: cmd.Context()}
		d.run()
		fmt.Println()
		if d.failed > 0 {
			return fmt.Errorf("%d check(s) failed", d.failed)
		}
		fmt.Printf("all checks passed (%d warning(s))\n", d.warned)
		return nil
	},
}

func init() { root.AddCommand(doctorCmd) }

type doctor struct {
	ctx    context.Context
	failed int
	warned int
}

func (d *doctor) ok(format string, a ...any) { fmt.Printf("  ok    %s\n", fmt.Sprintf(format, a...)) }
func (d *doctor) warn(format string, a ...any) {
	d.warned++
	fmt.Printf("  warn  %s\n", fmt.Sprintf(format, a...))
}
func (d *doctor) fail(format string, a ...any) {
	d.failed++
	fmt.Printf("  FAIL  %s\n", fmt.Sprintf(format, a...))
}

func (d *doctor) run() {
	fmt.Println("Configuration")
	path, err := config.Find(projectDir)
	if err != nil {
		d.fail("%v", err)
		return
	}
	cfg, err := loadProjectConfig(path)
	if err != nil {
		d.fail("%v", err)
		return
	}
	d.ok("%s is valid (project %q)", path, cfg.Name)
	d.checkFiles(cfg)

	fmt.Println("Tools")
	d.checkTool("git", "git", "--version")
	runner, _ := agent.Resolve(cfg.Agent.Spec()) // validated with the config
	d.checkTool(runner.Name+" runner", runner.Command, "--version")
	d.checkPermissions(cfg, runner)

	fmt.Println("Repository")
	d.checkRepo(cfg)

	fmt.Println("Credentials")
	h := d.checkHost(cfg)
	d.checkJira(cfg)

	fmt.Println("Sources")
	d.checkSources(cfg, h)

	fmt.Println("State")
	if err := os.MkdirAll(cfg.StatePath(), 0o755); err != nil {
		d.fail("cannot create %s: %v", cfg.StatePath(), err)
	} else {
		d.ok("%s is writable", cfg.StatePath())
	}
}

func (d *doctor) checkFiles(cfg *config.Config) {
	tpls := map[string]string{"session": cfg.Prompts.Session, "plan": cfg.Prompts.Plan, "self-review": cfg.Prompts.SelfReview, "review": cfg.Prompts.Review, "ci": cfg.Prompts.CI, "conflict": cfg.Prompts.Conflict, "verify": cfg.Prompts.Verify}
	for name, p := range tpls {
		if p == "" {
			continue
		}
		if _, err := prompt.RenderFile(name, cfg.Resolve(p), &prompt.Data{Item: &item.Item{}, PR: &prompt.PRRef{}}); err != nil {
			d.fail("prompts.%s: %v", name, err)
		}
	}
	for phase, steps := range cfg.Steps.All() {
		for _, st := range steps {
			switch {
			case st.Script != "":
				if info, err := os.Stat(cfg.Resolve(st.Script)); err != nil {
					d.fail("steps.%s script %s: %v", phase, st.Script, err)
				} else if info.Mode()&0o111 == 0 {
					d.fail("steps.%s script %s is not executable", phase, st.Script)
				}
			case st.Agent != "":
				if _, err := os.Stat(cfg.Resolve(st.Agent)); err != nil {
					d.fail("steps.%s agent prompt %s: %v", phase, st.Agent, err)
				}
			}
		}
	}
	for _, s := range cfg.Agent.Skills {
		if _, err := os.Stat(cfg.Resolve(s)); err != nil {
			d.fail("agent.skills %s: %v", s, err)
		}
	}
	d.ok("referenced prompts, scripts and skills exist")
}

// checkPermissions warns when a headless Claude session could not run the
// project's checks because no matching Bash rule is allowed.
func (d *doctor) checkPermissions(cfg *config.Config, runner *agent.Runner) {
	if runner.SettingsFile == "" {
		return
	}
	if cfg.Agent.PermissionMode == "bypassPermissions" {
		d.warn("agent.permission_mode is bypassPermissions: the agent can run any command without prompting")
		return
	}
	custom := 0
	for _, rule := range cfg.Agent.Allow {
		isDefault := false
		for _, def := range config.DefaultAllow {
			isDefault = isDefault || rule == def
		}
		if !isDefault {
			custom++
		}
	}
	if custom == 0 {
		d.warn("agent.allow has only the default git rules; add the project's test and build commands (for example \"Bash(npm test:*)\") or headless sessions stall on the first denied command")
		return
	}
	d.ok("agent.allow has %d project rule(s) besides the git defaults", custom)
}

func (d *doctor) checkTool(label, bin string, args ...string) {
	p, err := exec.LookPath(bin)
	if err != nil {
		d.fail("%s: %q not found on PATH", label, bin)
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, p, args...).Output()
	v := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if v == "" {
		v = p
	}
	d.ok("%s: %s", label, v)
}

func (d *doctor) checkRepo(cfg *config.Config) {
	ctx, cancel := context.WithTimeout(d.ctx, 60*time.Second)
	defer cancel()
	out, err := gitx.Run(ctx, "", "ls-remote", "--heads", cfg.Repo.URL, cfg.Repo.Base)
	switch {
	case err != nil:
		d.fail("cannot reach %s: %v", cfg.Repo.URL, firstLine(err.Error()))
	case out == "":
		d.fail("base branch %q does not exist on %s", cfg.Repo.Base, cfg.Repo.URL)
	default:
		d.ok("%s has branch %s", cfg.Repo.URL, cfg.Repo.Base)
	}
	if cfg.Repo.PushURL != "" {
		if _, err := gitx.Run(ctx, "", "ls-remote", "--heads", cfg.Repo.PushURL); err != nil {
			d.fail("cannot reach fork %s: %v", cfg.Repo.PushURL, firstLine(err.Error()))
		} else {
			d.ok("fork %s is reachable", cfg.Repo.PushURL)
		}
	}
}

func (d *doctor) checkHost(cfg *config.Config) host.Host {
	name := cfg.Repo.Host()
	h, err := host.New(cfg.Repo)
	if err != nil {
		d.fail("%s: %v", name, err)
		return nil
	}
	login, err := h.Viewer(d.ctx)
	if err != nil {
		d.fail("%s token rejected: %v", name, firstLine(err.Error()))
		return nil
	}
	repo, err := h.GetRepository(d.ctx)
	if err != nil {
		d.fail("%s: cannot read %s as %s: %v", name, cfg.Repo.Project(), login, firstLine(err.Error()))
		return nil
	}
	switch {
	case cfg.Repo.Fork != "":
		d.ok("%s: %s can read %s", name, login, repo.FullName)
		fork, ferr := host.NewProject(cfg.Repo, cfg.Repo.Fork)
		if ferr == nil {
			if fr, rerr := fork.GetRepository(d.ctx); rerr != nil {
				d.fail("%s: cannot read fork %s: %v", name, cfg.Repo.Fork, firstLine(rerr.Error()))
			} else if !fr.CanPush {
				d.fail("%s: %s has no push access to fork %s", name, login, fr.FullName)
			} else {
				d.ok("%s: %s can push to fork %s; pull requests open from there against %s", name, login, fr.FullName, repo.FullName)
			}
		}
		if !repo.CanPush {
			d.warn("%s: no push access to %s, so merge policies other than manual cannot merge", name, repo.FullName)
		}
	case !repo.CanPush:
		d.fail("%s: %s has no push access to %s (needed to push branches and merge); set repo.fork to contribute through a fork", name, login, repo.FullName)
	default:
		d.ok("%s: %s can push to %s", name, login, repo.FullName)
	}
	if repo.DefaultBranch != cfg.Repo.Base {
		d.warn("%s: default branch is %s, loop.yaml uses base %s", name, repo.DefaultBranch, cfg.Repo.Base)
	}
	return h
}

func (d *doctor) checkJira(cfg *config.Config) {
	seen := map[string]bool{}
	for _, sc := range cfg.Sources {
		if sc.Type != "jira" || seen[sc.URL] {
			continue
		}
		seen[sc.URL] = true
		c, err := jiraapi.New(sc.URL)
		if err != nil {
			d.fail("jira %s: %v", sc.URL, err)
			continue
		}
		who, err := c.Myself(d.ctx)
		if err != nil {
			d.fail("jira %s: credentials rejected: %v", sc.URL, firstLine(err.Error()))
			continue
		}
		d.ok("jira %s: authenticated as %s", sc.URL, who)
	}
}

func (d *doctor) checkSources(cfg *config.Config, h host.Host) {
	srcs, err := source.Build(cfg)
	if err != nil {
		d.fail("%v", err)
		return
	}
	for _, s := range srcs {
		if s.Type() == cfg.Repo.Host() && h == nil {
			d.fail("source %s: skipped, %s credentials failed", s.Name(), s.Type())
			continue
		}
		items, err := s.List(d.ctx)
		if err != nil {
			d.fail("source %s (%s): %v", s.Name(), s.Type(), firstLine(err.Error()))
			continue
		}
		d.ok("source %s (%s): %d open item(s)", s.Name(), s.Type(), len(items))
		if g, ok := s.(interface{ CommentsReason(context.Context) string }); ok {
			d.ok("source %s: ticket comments %s", s.Name(), g.CommentsReason(d.ctx))
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
