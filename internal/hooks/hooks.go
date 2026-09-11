// Package hooks runs user scripts at defined points. Global hooks live in
// $XDG_CONFIG_HOME/loop/hooks (default ~/.config/loop/hooks), project
// hooks in <project>/.loop/hooks. A hook is either an executable file
// named after the phase or every executable inside a directory named
// after the phase, run in name order. Global hooks run first.
package hooks

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// Phases.
const (
	Setup    = "setup"
	BeforePR = "before-pr"
	Merged   = "merged"
	Cleanup  = "cleanup"
)

// GlobalDir returns the global hooks folder.
func GlobalDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "loop", "hooks")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "loop", "hooks")
}

// ProjectDir returns the project hooks folder.
func ProjectDir(projectDir string) string { return filepath.Join(projectDir, ".loop", "hooks") }

// Find lists the scripts for a phase in execution order.
func Find(projectDir, phase string) []string {
	var out []string
	for _, dir := range []string{GlobalDir(), ProjectDir(projectDir)} {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, phase)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !st.IsDir() {
			if st.Mode()&0o111 != 0 {
				out = append(out, p)
			}
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Mode()&0o111 == 0 {
				continue
			}
			names = append(names, filepath.Join(p, e.Name()))
		}
		sort.Strings(names)
		out = append(out, names...)
	}
	return out
}

// Run executes every hook of the phase in workdir with env added.
func Run(ctx context.Context, projectDir, phase, workdir string, env map[string]string, out io.Writer) error {
	for _, script := range Find(projectDir, phase) {
		fmt.Fprintf(out, "  hook %s: %s\n", phase, script)
		cmd := exec.CommandContext(ctx, script)
		cmd.Dir = workdir
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.Stdout = out
		cmd.Stderr = out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("hook %s failed: %w", script, err)
		}
	}
	return nil
}
