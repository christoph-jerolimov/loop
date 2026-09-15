package cli

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/ghapi"
	"github.com/christoph-jerolimov/loop/internal/gitx"
	"github.com/christoph-jerolimov/loop/internal/glapi"
	"github.com/christoph-jerolimov/loop/internal/prompt"
)

//go:embed scaffold/*
var scaffold embed.FS

var (
	initForce    bool
	initRepo     string
	initPrompts  bool
	initSources  []string
	initNoDoctor bool
	initFull     bool
)

var initCmd = &cobra.Command{
	Use:   "init [dir]",
	Short: "Create a loop project: loop.yaml, backlog/ and .gitignore",
	Long: `Creates a loop project in dir (default: the current folder). The target
repository is taken from --repo, or from the origin remote of the git
checkout that dir or the current folder is part of; the base branch from
that remote's HEAD. Without either, loop.yaml gets placeholders to edit.

The project can be a folder next to your checkout or the checkout itself:
run "loop init" in the repository root and loop.yaml and backlog/ live in
the repository while .loop/ is added to its .gitignore.

Prompts use the built-in templates unless --prompts is given, which
writes editable copies to prompts/ and points loop.yaml at them. In an
existing project --prompts only writes the files and prints the keys to
add to loop.yaml.

The backlog starts as a markdown folder. --source github, gitlab or jira
adds a source of that type; a source for the repository's own host is
added when its token (GITHUB_TOKEN or gh, GITLAB_TOKEN) is available.

When the checkout reveals its toolchain (go.mod, package.json, Cargo.toml,
pyproject.toml, pom.xml, build.gradle, a Makefile with a test target),
its test command becomes the verify step and the session may run it.

When the repository was detected, the doctor checks run at the end so
the first loop run does not fail on a missing tool or token; --no-doctor
skips them.

loop.yaml holds only what init decided; every other option keeps its
default. --full writes the annotated version with every option instead.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		det := detectRepo(cmd.Context(), dir, projectDir, initRepo)
		det.Prompts = initPrompts
		if det.From != "" {
			fmt.Printf("using   %s (%s)\n", det.URL, det.From)
		}
		if err := det.chooseSources(initSources); err != nil {
			return err
		}
		det.detectToolchain()
		if det.Test != "" {
			fmt.Printf("using   %q as verify step and allowing it in agent.allow (%s found)\n", det.Test, det.Marker)
		}
		tpl := "scaffold/loop.yaml"
		if initFull {
			tpl = "scaffold/loop-full.yaml"
		}
		cfg, err := renderScaffold(tpl, det)
		if err != nil {
			return err
		}
		wrote, err := writeNew(filepath.Join(dir, "loop.yaml"), cfg)
		if err != nil {
			return err
		}
		example, err := scaffold.ReadFile("scaffold/example.md")
		if err != nil {
			return err
		}
		if _, err := writeNew(filepath.Join(dir, "backlog", "example.md"), example); err != nil {
			return err
		}
		if err := ignoreState(dir); err != nil {
			return err
		}
		if initPrompts {
			for _, name := range promptNames {
				if _, err := writeNew(filepath.Join(dir, "prompts", name+".md"), []byte(prompt.Default(name))); err != nil {
					return err
				}
			}
			if !wrote {
				fmt.Println("add to loop.yaml to use the copies:")
				fmt.Print(promptKeys)
			}
		}
		if det.From == "" {
			fmt.Println("Next steps:")
			fmt.Println("  1. edit loop.yaml: set repo.url and your sources")
			fmt.Println("  2. loop doctor")
			fmt.Println("  3. put ideas into backlog/*.md or label issues ready-for-agent")
			fmt.Println("  4. loop list, then loop run <item>")
			return nil
		}
		if !initNoDoctor {
			fmt.Println()
			d := &doctor{ctx: cmd.Context()}
			saved := projectDir
			projectDir = dir
			d.run()
			projectDir = saved
			fmt.Println()
			if d.failed > 0 {
				fmt.Printf("%d check(s) failed. Next steps:\n", d.failed)
				fmt.Println("  1. fix the failed checks above, then: loop doctor")
				fmt.Println("  2. put ideas into backlog/*.md or label issues ready-for-agent")
				fmt.Println("  3. loop list, then loop run <item>")
				return nil
			}
			fmt.Printf("all checks passed (%d warning(s)). ", d.warned)
		}
		fmt.Println("Next steps:")
		fmt.Println("  1. write an idea: $EDITOR backlog/my-idea.md (see backlog/example.md)")
		fmt.Println("  2. loop list")
		fmt.Println("  3. loop run my-idea.md")
		return nil
	},
}

// ignoreState makes sure .loop/ is git-ignored: a fresh .gitignore in a
// new folder, or one line appended to the .gitignore that is already
// there when the project lives inside the repository itself.
func ignoreState(dir string) error {
	path := filepath.Join(dir, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil {
		b, err := scaffold.ReadFile("scaffold/gitignore")
		if err != nil {
			return err
		}
		_, err = writeNew(path, b)
		return err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		switch strings.TrimSpace(line) {
		case ".loop", ".loop/", "/.loop", "/.loop/":
			fmt.Printf("keep    %s (already ignores .loop/)\n", path)
			return nil
		}
	}
	add := "# loop state: base clone, workdirs and run logs\n.loop/\n"
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		add = "\n" + add
	}
	if err := os.WriteFile(path, append(existing, []byte(add)...), 0o644); err != nil {
		return err
	}
	fmt.Printf("added   .loop/ to %s\n", path)
	return nil
}

// promptNames are the templates --prompts writes, in the order of the keys.
var promptNames = []string{prompt.TplSession, prompt.TplPlan, prompt.TplSelfReview, prompt.TplReview, prompt.TplCI, prompt.TplConflict, prompt.TplVerify, prompt.TplPRBody}

// promptKeys is the loop.yaml fragment that points at the copies.
const promptKeys = `prompts:
  session: prompts/session.md
  plan: prompts/plan.md
  self_review: prompts/self-review.md
  review: prompts/review.md
  ci: prompts/ci.md
  conflict: prompts/conflict.md
  verify: prompts/verify.md
pr:
  body: prompts/pr-body.md
`

// repoDetection is what loop init found out about the target repository
// and how the scaffold is rendered.
type repoDetection struct {
	Name, URL, Base string
	// From says where the URL came from; "" means it is a placeholder.
	From string
	// Prompts points loop.yaml at copies of the templates in prompts/.
	Prompts bool
	// GitHub, GitLab and Jira enable the respective issue source.
	GitHub, GitLab, Jira bool
	// Checkout is the git checkout the repository was detected in.
	Checkout string
	// Test is the detected test command for steps.verify, Allow the
	// permission rules it needs; Marker the file that revealed them.
	Test   string
	Allow  []string
	Marker string
}

// toolchain maps a marker file in the checkout to its test command and
// the permission rules a headless session needs for it.
type toolchain struct {
	marker string
	test   string
	allow  []string
	// lock narrows the match: the file must exist too.
	lock string
}

var toolchains = []toolchain{
	{marker: "go.mod", test: "go test ./...", allow: []string{"Bash(go build:*)", "Bash(go test:*)", "Bash(go vet:*)"}},
	{marker: "package.json", lock: "pnpm-lock.yaml", test: "pnpm test", allow: []string{"Bash(pnpm install:*)", "Bash(pnpm test:*)", "Bash(pnpm run:*)"}},
	{marker: "package.json", lock: "yarn.lock", test: "yarn test", allow: []string{"Bash(yarn install:*)", "Bash(yarn test:*)", "Bash(yarn run:*)"}},
	{marker: "package.json", lock: "bun.lockb", test: "bun test", allow: []string{"Bash(bun install:*)", "Bash(bun test:*)", "Bash(bun run:*)"}},
	{marker: "package.json", test: "npm test", allow: []string{"Bash(npm ci:*)", "Bash(npm install:*)", "Bash(npm test:*)", "Bash(npm run:*)"}},
	{marker: "Cargo.toml", test: "cargo test", allow: []string{"Bash(cargo build:*)", "Bash(cargo test:*)", "Bash(cargo clippy:*)"}},
	{marker: "pyproject.toml", test: "pytest", allow: []string{"Bash(pytest:*)", "Bash(python -m pytest:*)", "Bash(pip install:*)"}},
	{marker: "pom.xml", test: "mvn -q test", allow: []string{"Bash(mvn:*)"}},
	{marker: "build.gradle", test: "./gradlew test", allow: []string{"Bash(./gradlew:*)"}},
	{marker: "build.gradle.kts", test: "./gradlew test", allow: []string{"Bash(./gradlew:*)"}},
	{marker: "Makefile", test: "make test", allow: []string{"Bash(make:*)"}},
}

// detectToolchain fills Test, Allow and Marker from the checkout. A
// Makefile counts only with a test target.
func (d *repoDetection) detectToolchain() {
	if d.Checkout == "" {
		return
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(d.Checkout, name))
		return err == nil
	}
	for _, tc := range toolchains {
		if !exists(tc.marker) || (tc.lock != "" && !exists(tc.lock)) {
			continue
		}
		if tc.marker == "Makefile" {
			b, _ := os.ReadFile(filepath.Join(d.Checkout, "Makefile"))
			if !strings.Contains("\n"+string(b), "\ntest:") {
				continue
			}
		}
		// Setting agent.allow replaces the defaults, so the git rules a
		// session needs to commit come along.
		d.Test, d.Marker = tc.test, tc.marker
		d.Allow = append(append([]string{}, config.DefaultAllow...), tc.allow...)
		if tc.lock != "" {
			d.Marker += " + " + tc.lock
		}
		return
	}
}

// chooseSources enables the sources named with --source and, without
// any, the issue source of the repository's host when its token is set.
func (d *repoDetection) chooseSources(flags []string) error {
	for _, s := range flags {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "github":
			d.GitHub = true
		case "gitlab":
			d.GitLab = true
		case "jira":
			d.Jira = true
		case "markdown", "":
		default:
			return fmt.Errorf("--source must be github, gitlab or jira, got %q", s)
		}
	}
	if len(flags) > 0 {
		return nil
	}
	host := detectHost(d.URL)
	switch {
	case host == config.HostGitHub && !d.GitHub:
		if _, err := ghapi.Token(); err == nil {
			d.GitHub = true
			fmt.Println("adding  github issues source (a GitHub token is available)")
		}
	case host == config.HostGitLab && !d.GitLab:
		if _, err := glapi.Token(); err == nil {
			d.GitLab = true
			fmt.Println("adding  gitlab issues source (GITLAB_TOKEN is set)")
		}
	}
	return nil
}

// detectHost applies the config rules to a clone URL without loading a
// config: github for github.com URLs, gitlab for hosts named gitlab.
func detectHost(url string) string {
	c := config.Config{Repo: config.Repo{URL: url}, Dir: "."}
	c.ApplyDefaults()
	if c.Repo.GitHub == "" && c.Repo.GitLab == "" {
		return ""
	}
	return c.Repo.Host()
}

const placeholderURL = "git@github.com:my-org/my-service.git"

// detectRepo picks the repository for loop.yaml: the --repo value, or the
// origin remote of the git checkout that dir or the project folder is
// part of. The base branch is the remote's HEAD when the checkout knows
// it, the name the repository's basename.
func detectRepo(ctx context.Context, dir, projectDir, flag string) repoDetection {
	det := repoDetection{URL: placeholderURL, Base: "main"}
	if flag != "" {
		det.URL, det.From = flag, "--repo"
	} else {
		for _, d := range []string{dir, projectDir} {
			top, err := gitx.Run(ctx, d, "rev-parse", "--show-toplevel")
			if err != nil {
				continue
			}
			url, err := gitx.Run(ctx, top, "remote", "get-url", "origin")
			if err != nil || url == "" {
				continue
			}
			det.URL, det.From, det.Checkout = url, "origin of "+top, top
			if head, err := gitx.Run(ctx, top, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
				det.Base = strings.TrimPrefix(head, "origin/")
			}
			break
		}
	}
	det.Name = repoName(det.URL)
	if det.From == "" || det.Name == "" {
		abs, err := filepath.Abs(dir)
		if err == nil {
			det.Name = filepath.Base(abs)
		}
	}
	return det
}

// repoName is the last path element of a clone URL without .git.
func repoName(url string) string {
	url = strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		url = url[i+1:]
	}
	return url
}

// renderScaffold fills the [[ ]] placeholders of a scaffold file. The
// delimiters keep the {{ }} of loop's own prompt templates untouched.
func renderScaffold(name string, det repoDetection) ([]byte, error) {
	b, err := scaffold.ReadFile(name)
	if err != nil {
		return nil, err
	}
	tpl, err := template.New(name).Delims("[[", "]]").Parse(string(b))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, det); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// writeNew writes the file unless it exists (or --force is given) and
// reports whether it wrote.
func writeNew(path string, content []byte) (bool, error) {
	if _, err := os.Stat(path); err == nil && !initForce {
		fmt.Printf("keep    %s (exists)\n", path)
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	mode := os.FileMode(0o644)
	if filepath.Ext(path) == ".sh" {
		mode = 0o755
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return false, err
	}
	fmt.Printf("created %s\n", path)
	return true, nil
}

func init() {
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite existing files")
	initCmd.Flags().StringVar(&initRepo, "repo", "", "clone URL of the target repository (default: origin of the surrounding git checkout)")
	initCmd.Flags().BoolVar(&initPrompts, "prompts", false, "write editable copies of the prompt templates to prompts/ and point loop.yaml at them")
	initCmd.Flags().StringSliceVar(&initSources, "source", nil, "issue sources to add besides the markdown backlog: github, gitlab, jira (default: the repository's host when its token is set)")
	initCmd.Flags().BoolVar(&initNoDoctor, "no-doctor", false, "do not run the doctor checks after scaffolding")
	initCmd.Flags().BoolVar(&initFull, "full", false, "write an annotated loop.yaml that lists every option")
}
