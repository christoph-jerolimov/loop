package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var statsJSON bool

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Runs, merged PRs, fix rounds, cost and today's budget",
	Long: `Summarises every run in .loop/runs: how many ended in which phase, how
many PRs were merged, the mean number of fix rounds per PR, agent time and
cost (for harnesses that report it), the mean time from start to done,
and what was spent today against the configured budget.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := load()
		if err != nil {
			return err
		}
		st, err := a.Engine.Stats()
		if err != nil {
			return err
		}
		if statsJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(st)
		}
		phases := make([]string, 0, len(st.ByPhase))
		for p := range st.ByPhase {
			phases = append(phases, p)
		}
		sort.Strings(phases)
		var parts []string
		for _, p := range phases {
			parts = append(parts, fmt.Sprintf("%s %d", p, st.ByPhase[p]))
		}
		b := a.Cfg.Budget
		fmt.Printf("runs:            %d (%s)\n", st.Runs, strings.Join(parts, ", "))
		fmt.Printf("merged PRs:      %d\n", st.Merged)
		if st.NoChanges > 0 {
			fmt.Printf("no changes:      %d recurring run(s) without a PR\n", st.NoChanges)
		}
		fmt.Printf("fix rounds:      %.1f per PR\n", st.FixRounds)
		fmt.Printf("sessions:        %d, %s of agent time\n", st.Sessions, st.AgentTime.Round(time.Second))
		fmt.Printf("cost:            $%.2f total, $%.2f per merged PR\n", st.Cost, st.CostMerge)
		fmt.Printf("time to done:    %s mean\n", st.Duration)
		fmt.Printf("today:           %d run(s)%s, $%.2f%s\n", st.Today.Runs, limit(b.DailyRuns), st.Today.Cost, limitUSD(b.DailyCost))
		return nil
	},
}

func limit(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" of %d", n)
}

func limitUSD(v float64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf(" of $%.2f", v)
}

func init() {
	statsCmd.Flags().BoolVar(&statsJSON, "json", false, "print JSON")
	root.AddCommand(statsCmd)
}
