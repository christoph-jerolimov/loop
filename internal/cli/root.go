// Package cli implements the loop command line.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/engine"
	"github.com/christoph-jerolimov/loop/internal/source"
	_ "github.com/christoph-jerolimov/loop/internal/source/all"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// Version is set at build time.
var Version = "dev"

var (
	projectDir string
	runnerFlag string
)

var root = &cobra.Command{
	Use:   "loop",
	Short: "Run AI coding sessions from a backlog and drive the resulting PRs to merge",
	Long: `loop picks ideas, goals or tickets from a backlog (markdown files, GitHub
issues, Jira), starts a coding agent (Claude Code, Cursor, Codex, Gemini, Aider,
OpenCode, Copilot, Amp or your own script) in a fresh checkout,
opens a pull request, and keeps working the PR through reviews and CI until
it is merged and the ticket is closed.

A project is a folder with a loop.yaml; run "loop init" to create one.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       Version,
}

func init() {
	root.PersistentFlags().StringVarP(&projectDir, "project", "C", ".", "project folder (or any folder below it)")
	root.PersistentFlags().StringVar(&runnerFlag, "runner", "", "agent harness for this invocation (overrides loop.yaml and LOOP_RUNNER)")
	root.AddCommand(initCmd, listCmd, showCmd, runCmd, watchCmd, statusCmd, approveCmd, resumeCmd, joinCmd, cleanCmd)
}

// Execute runs the CLI.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return err
	}
	return nil
}

// app bundles what most commands need.
type app struct {
	Cfg     *config.Config
	Sources source.Set
	Engine  *engine.Engine
}

func load() (*app, error) { return loadDir(projectDir, false) }

// loadDir loads the project at or above dir. With prefix, every output
// line carries the project name, for watching several projects at once.
func loadDir(dir string, prefix bool) (*app, error) {
	path, err := config.Find(dir)
	if err != nil {
		return nil, err
	}
	cfg, err := loadProjectConfig(path)
	if err != nil {
		return nil, err
	}
	srcs, err := source.Build(cfg)
	if err != nil {
		return nil, err
	}
	var out io.Writer = os.Stdout
	if prefix {
		out = &prefixWriter{prefix: cfg.Name + " | ", w: os.Stdout}
	}
	eng, err := engine.New(cfg, srcs, out)
	if err != nil {
		return nil, err
	}
	eng.Gate = askGate
	return &app{Cfg: cfg, Sources: srcs, Engine: eng}, nil
}

// loadProjectConfig loads loop.yaml and applies the command-line overrides: the
// harness from --runner, else from LOOP_RUNNER, else from loop.yaml.
func loadProjectConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if r := runnerOverride(); r != "" && r != cfg.Agent.Runner {
		cfg.Agent.Runner = r
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func runnerOverride() string {
	if runnerFlag != "" {
		return runnerFlag
	}
	return os.Getenv("LOOP_RUNNER")
}

// prefixWriter prepends a prefix to every line written through it.
type prefixWriter struct {
	prefix  string
	w       io.Writer
	mu      sync.Mutex
	midline bool
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(b)
	var buf bytes.Buffer
	for len(b) > 0 {
		if !p.midline {
			buf.WriteString(p.prefix)
			p.midline = true
		}
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			buf.Write(b)
			break
		}
		buf.Write(b[:i+1])
		p.midline = false
		b = b[i+1:]
	}
	if _, err := p.w.Write(buf.Bytes()); err != nil {
		return 0, err
	}
	return n, nil
}

func isTTY() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func askGate(r *state.Run, gate string) bool {
	fmt.Printf("\n[%s] gate %q reached", r.ItemID, gate)
	if r.PR != nil {
		fmt.Printf(" for %s", r.PR.URL)
	}
	fmt.Print(". Continue? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}
