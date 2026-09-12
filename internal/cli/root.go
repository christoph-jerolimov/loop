// Package cli implements the loop command line.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
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

var projectDir string

var root = &cobra.Command{
	Use:   "loop",
	Short: "Run AI coding sessions from a backlog and drive the resulting PRs to merge",
	Long: `loop picks ideas, goals or tickets from a backlog (markdown files, GitHub
issues, Jira), starts a coding agent (claude or cursor) in a fresh checkout,
opens a pull request, and keeps working the PR through reviews and CI until
it is merged and the ticket is closed.

A project is a folder with a loop.yaml; run "loop init" to create one.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       Version,
}

func init() {
	root.PersistentFlags().StringVarP(&projectDir, "project", "C", ".", "project folder (or any folder below it)")
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

func load() (*app, error) {
	path, err := config.Find(projectDir)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	srcs, err := source.Build(cfg)
	if err != nil {
		return nil, err
	}
	eng, err := engine.New(cfg, srcs, os.Stdout)
	if err != nil {
		return nil, err
	}
	eng.Gate = askGate
	return &app{Cfg: cfg, Sources: srcs, Engine: eng}, nil
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
