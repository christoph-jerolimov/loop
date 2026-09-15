package cli

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/christoph-jerolimov/loop/internal/engine"
	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/state"
	"github.com/christoph-jerolimov/loop/internal/term"
)

var (
	runForce   bool
	runAll     bool
	runSource  string
	runNoWatch bool
	runDryRun  bool
)

var runCmd = &cobra.Command{
	Use:   "run [item]",
	Short: "Work on one item (or --all ready items) and drive the PR until the ticket is closed",
	Long: `Starts a run for the item: checkout, setup steps, agent session, verify
steps, pull request, then polls the PR for reviews and CI until it is merged
and the ticket closed. Items with open dependencies or an in-progress marker
are refused unless --force is given. A recurring item (every: 7d) starts
even when it is not due yet: this command is its manual trigger.

With --all every ready item is picked up in backlog order, respecting
workflow.concurrency, and the command returns when nothing is left.
Recurring items are started only when they are due.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := load()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		a.Engine.Interactive = isTTY()
		if runAll {
			if len(args) > 0 {
				return errors.New("--all takes no item argument")
			}
			return a.Engine.Watch(ctx, engine.WatchOptions{PickNew: true, Force: runForce, Source: runSource, ExitWhenIdle: true})
		}
		if len(args) == 0 {
			return errors.New("item argument required (or --all)")
		}
		it, err := a.Sources.Resolve(ctx, args[0])
		if err != nil {
			return err
		}
		rd := a.Engine.Check(ctx, it)
		if runDryRun {
			return dryRun(a, it, rd)
		}
		if !rd.Ready && !runForce {
			if rd.ActiveRun != nil {
				return fmt.Errorf("item %s already has active run %s in phase %s (see: loop status)", it.ID, rd.ActiveRun.ID, rd.ActiveRun.Phase)
			}
			if rd.InProgress {
				fmt.Fprintf(os.Stderr, "warning: %s is marked in progress%s\n", it.ID, claimNote(it.ClaimedBy))
			}
			for _, d := range rd.OpenDeps {
				fmt.Fprintf(os.Stderr, "warning: %s depends on open item %s\n", it.ID, d)
			}
			for _, d := range rd.DepErrors {
				fmt.Fprintf(os.Stderr, "warning: dependency could not be resolved: %s\n", d)
			}
			return errors.New("not started; use --force to run anyway")
		}
		if rd.Ready && !rd.Due {
			fmt.Fprintf(os.Stderr, "note: %s is not due until %s; starting it now anyway\n", it.ID, rd.NextDue.Local().Format("2006-01-02 15:04"))
		}
		r, err := a.Engine.Start(ctx, it, runForce)
		if err != nil {
			return err
		}
		if runNoWatch {
			if err := a.Engine.Drive(ctx, r); err != nil {
				return err
			}
			return report(r)
		}
		if err := a.Engine.Watch(ctx, engine.WatchOptions{Only: []string{r.ID}, ExitWhenIdle: true, Tick: 2 * time.Second}); err != nil {
			return err
		}
		r, _ = a.Engine.Store.Load(r.ID)
		return report(r)
	},
}

func claimNote(by string) string {
	if by == "" {
		return ""
	}
	return " by run " + by
}

func report(r *state.Run) error {
	p := term.Detect(os.Stdout)
	switch r.Phase {
	case state.PhaseDone:
		fmt.Printf("\n%s %s", r.ItemID, p.Paint("done", term.Green))
		switch {
		case r.Outcome == state.OutcomeNoChanges:
			fmt.Print(" (no changes, no PR)")
		case r.PR != nil:
			fmt.Printf(" (%s)", r.PR.URL)
		}
		fmt.Println()
		return nil
	case state.PhaseFailed, state.PhaseBlocked:
		return fmt.Errorf("run %s %s: %s", r.ID, r.Phase, r.Error)
	}
	fmt.Printf("\nrun %s %s", r.ID, p.Paint("parked in phase "+string(r.Phase), term.Yellow))
	if r.Gate != "" {
		fmt.Printf(" at gate %s (loop approve %s)", r.Gate, r.ID)
	}
	fmt.Println("; continue with: loop watch")
	return nil
}

func init() {
	runCmd.Flags().BoolVar(&runForce, "force", false, "ignore open dependencies and in-progress markers")
	runCmd.Flags().BoolVar(&runAll, "all", false, "run every ready item")
	runCmd.Flags().StringVarP(&runSource, "source", "s", "", "with --all: only items from this source")
	runCmd.Flags().BoolVar(&runNoWatch, "no-watch", false, "stop after the PR is opened; monitor later with loop watch")
	runCmd.Flags().BoolVar(&runDryRun, "dry-run", false, "print what the run would do (branch, harness, steps, policy, prompt) without doing any of it")
}

var watchPick bool

var watchCmd = &cobra.Command{
	Use:   "watch [project-dir...]",
	Short: "Drive all active runs: poll PRs, run fix rounds, merge, close",
	Long: `Keeps running until interrupted. With --pick it also starts ready backlog
items whenever capacity (workflow.concurrency) is free, turning loop into a
long-running worker.

Without arguments it watches the current project. With one or more project
folders it watches all of them at once, prefixing output with the project
name, so one process can drive every loop project you have:

  loop watch --pick ~/loops/*`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dirs := args
		if len(dirs) == 0 {
			dirs = []string{projectDir}
		}
		var apps []*app
		for _, dir := range dirs {
			a, err := loadDir(dir, len(dirs) > 1)
			if err != nil {
				return err
			}
			apps = append(apps, a)
		}
		ctx := cmd.Context()
		var wg sync.WaitGroup
		errs := make([]error, len(apps))
		for i, a := range apps {
			wg.Add(1)
			go func(i int, a *app) {
				defer wg.Done()
				err := a.Engine.Watch(ctx, engine.WatchOptions{PickNew: watchPick, Force: false})
				if err != nil && !errors.Is(err, ctx.Err()) {
					errs[i] = fmt.Errorf("%s: %w", a.Cfg.Name, err)
				}
			}(i, a)
		}
		wg.Wait()
		return errors.Join(errs...)
	},
}

func init() {
	watchCmd.Flags().BoolVar(&watchPick, "pick", false, "also start ready backlog items")
}

var statusAll bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show runs and their phase",
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := load()
		if err != nil {
			return err
		}
		runs, err := a.Engine.Store.List()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "RUN\tITEM\tPHASE\tBRANCH\tPR\tNOTE")
		for _, r := range runs {
			if !statusAll && !r.Phase.Active() {
				continue
			}
			pr := ""
			if r.PR != nil {
				pr = r.PR.URL
			}
			note := r.Error
			if r.Gate != "" {
				note = "waiting at gate " + r.Gate
			} else if r.Phase == state.PhaseMonitor && !r.NextPoll.IsZero() {
				note = "next poll " + r.NextPoll.Format("15:04:05")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.ItemID, r.Phase, r.Branch, pr, note)
		}
		return tw.Flush()
	},
}

func init() {
	statusCmd.Flags().BoolVarP(&statusAll, "all", "a", false, "include finished runs")
}

var approveCmd = &cobra.Command{
	Use:   "approve <run>",
	Short: "Let a run continue past the gate it is waiting at",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := load()
		if err != nil {
			return err
		}
		r, err := a.Engine.Store.Find(args[0])
		if err != nil {
			return err
		}
		if r.Gate == "" {
			return fmt.Errorf("run %s is not waiting at a gate (phase %s)", r.ID, r.Phase)
		}
		r.GateApproved = r.Gate
		r.Log("gate %s approved", r.Gate)
		if err := a.Engine.Store.Save(r); err != nil {
			return err
		}
		fmt.Printf("approved gate %s for %s; it continues on the next watch tick\n", r.Gate, r.ID)
		return nil
	},
}

var resumeCmd = &cobra.Command{
	Use:   "resume <run>",
	Short: "Put a blocked or failed run back into the workflow",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := load()
		if err != nil {
			return err
		}
		r, err := a.Engine.Store.Find(args[0])
		if err != nil {
			return err
		}
		if err := a.Engine.Resume(r); err != nil {
			return err
		}
		fmt.Printf("run %s resumed in phase %s; drive it with: loop watch\n", r.ID, r.Phase)
		return nil
	},
}

var joinCmd = &cobra.Command{
	Use:   "join <run>",
	Short: "Print the command to open the run's workdir and continue its agent session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := load()
		if err != nil {
			return err
		}
		r, err := a.Engine.Store.Find(args[0])
		if err != nil {
			return err
		}
		if r.Workdir == "" {
			return fmt.Errorf("run %s has no workdir yet", r.ID)
		}
		if !r.HasWorkdir() {
			return fmt.Errorf("run %s: workdir %s was removed (loop clean or retention.workdirs); loop resume %s checks the branch out again", r.ID, r.Workdir, r.ID)
		}
		sessionID := ""
		for i := len(r.Sessions) - 1; i >= 0; i-- {
			if r.Sessions[i].ID != "" {
				sessionID = r.Sessions[i].ID
				break
			}
		}
		fmt.Println(a.Engine.Runner.JoinCommand(r.Workdir, sessionID))
		return nil
	},
}

var (
	cleanAll       bool
	cleanOlderThan string
)

var cleanCmd = &cobra.Command{
	Use:   "clean [run]",
	Short: "Remove workdirs of finished runs; run folders with logs and sessions stay",
	Long: `Removes the checkouts (worktrees or clones) of finished runs, or of one
run. The run folders under .loop/runs with run.yaml, the log and every
session transcript and prompt are kept. loop resume checks the branch of a
cleaned run out again when the run still has a PR.

--older-than applies the retention rule by hand: only runs that last
changed more than the given time ago (7d, 2w, 36h) lose their checkout.
loop watch does the same every ten minutes with retention.workdirs.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := load()
		if err != nil {
			return err
		}
		if cleanOlderThan != "" {
			if len(args) > 0 || cleanAll {
				return errors.New("--older-than takes no run argument and no --force")
			}
			age, err := item.ParseInterval(cleanOlderThan)
			if err != nil {
				return fmt.Errorf("--older-than: %w", err)
			}
			retired, err := a.Engine.Retire(cmd.Context(), age)
			for _, r := range retired {
				fmt.Printf("removed %s\n", r.Workdir)
			}
			if err != nil {
				return err
			}
			if len(retired) == 0 {
				fmt.Printf("nothing to remove: no finished run with a workdir last changed more than %s ago\n", item.FormatInterval(age))
			}
			return nil
		}
		runs, err := a.Engine.Store.List()
		if err != nil {
			return err
		}
		var targets []*state.Run
		if len(args) == 1 {
			r, err := a.Engine.Store.Find(args[0])
			if err != nil {
				return err
			}
			targets = append(targets, r)
		} else {
			for _, r := range runs {
				if !r.Phase.Active() {
					targets = append(targets, r)
				}
			}
		}
		for _, r := range targets {
			if r.Phase.Active() && !cleanAll {
				return fmt.Errorf("run %s is still %s; use --force", r.ID, r.Phase)
			}
			if !r.HasWorkdir() {
				continue
			}
			if err := a.Engine.RemoveWorkdir(cmd.Context(), r); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", r.Workdir)
		}
		return nil
	},
}

func init() {
	cleanCmd.Flags().BoolVar(&cleanAll, "force", false, "also clean active runs")
	cleanCmd.Flags().StringVar(&cleanOlderThan, "older-than", "", "only finished runs that last changed more than this long ago (7d, 2w, 36h)")
}
