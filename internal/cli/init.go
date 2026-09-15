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

	"github.com/christoph-jerolimov/loop/internal/gitx"
	"github.com/christoph-jerolimov/loop/internal/prompt"
)

//go:embed scaffold/*
var scaffold embed.FS

var (
	initForce   bool
	initRepo    string
	initPrompts bool
)

var initCmd = &cobra.Command{
	Use:   "init [dir]",
	Short: "Create a loop project: loop.yaml, backlog/, hooks/ and .gitignore",
	Long: `Creates a loop project in dir (default: the current folder). The target
repository is taken from --repo, or from the origin remote of the git
checkout that dir or the current folder is part of; the base branch from
that remote's HEAD. Without either, loop.yaml gets placeholders to edit.

Prompts use the built-in templates unless --prompts is given, which
writes editable copies to prompts/ and points loop.yaml at them. In an
existing project --prompts only writes the files and prints the keys to
add to loop.yaml.`,
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
		cfg, err := renderScaffold("scaffold/loop.yaml", det)
		if err != nil {
			return err
		}
		wrote, err := writeNew(filepath.Join(dir, "loop.yaml"), cfg)
		if err != nil {
			return err
		}
		files := map[string]string{
			"backlog/example.md": "scaffold/example.md",
			".gitignore":         "scaffold/gitignore",
			"hooks/setup.sh":     "scaffold/setup-hook.sh",
		}
		for dst, src := range files {
			b, err := scaffold.ReadFile(src)
			if err != nil {
				return err
			}
			if _, err := writeNew(filepath.Join(dir, dst), b); err != nil {
				return err
			}
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
		fmt.Println("Next steps:")
		if det.From == "" {
			fmt.Println("  1. edit loop.yaml: set repo.url and your sources")
		} else {
			fmt.Println("  1. check loop.yaml: repository, base branch and sources")
		}
		fmt.Println("  2. put ideas into backlog/*.md or label issues ready-for-agent")
		fmt.Println("  3. loop list, then loop run <item>")
		return nil
	},
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
			det.URL, det.From = url, "origin of "+top
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
}
