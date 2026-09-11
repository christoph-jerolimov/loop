package cli

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/christoph-jerolimov/loop/internal/prompt"
)

//go:embed scaffold/*
var scaffold embed.FS

var initForce bool

var initCmd = &cobra.Command{
	Use:   "init [dir]",
	Short: "Create a loop project: loop.yaml, prompts/, backlog/, hooks and .gitignore",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		files := map[string]string{
			"loop.yaml":                         "scaffold/loop.yaml",
			"backlog/example.md":                "scaffold/example.md",
			".gitignore":                        "scaffold/gitignore",
			".loop/hooks/setup.d/10-example.sh": "scaffold/setup-hook.sh",
		}
		for dst, src := range files {
			b, err := scaffold.ReadFile(src)
			if err != nil {
				return err
			}
			if err := writeNew(filepath.Join(dir, dst), b); err != nil {
				return err
			}
		}
		for _, name := range []string{prompt.TplSession, prompt.TplReview, prompt.TplCI, prompt.TplConflict, prompt.TplVerify, prompt.TplPRBody} {
			if err := writeNew(filepath.Join(dir, "prompts", name+".md"), []byte(prompt.Default(name))); err != nil {
				return err
			}
		}
		fmt.Println("Next steps:")
		fmt.Println("  1. edit loop.yaml: set repo.url and your sources")
		fmt.Println("  2. put ideas into backlog/*.md or label issues ready-for-agent")
		fmt.Println("  3. loop list, then loop run <item>")
		return nil
	},
}

func writeNew(path string, content []byte) error {
	if _, err := os.Stat(path); err == nil && !initForce {
		fmt.Printf("keep    %s (exists)\n", path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if filepath.Ext(path) == ".sh" {
		mode = 0o755
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return err
	}
	fmt.Printf("created %s\n", path)
	return nil
}

func init() {
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite existing files")
}
