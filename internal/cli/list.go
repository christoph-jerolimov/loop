package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/christoph-jerolimov/loop/internal/engine"
	"github.com/christoph-jerolimov/loop/internal/prompt"
)

var (
	listSource string
	listJSON   bool
	listReady  bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List open backlog items in pick-up order",
	Long: `Items are ordered by the position of their source in loop.yaml, then
oldest first. The STATUS column shows why an item would be skipped:
blocked by open dependencies, in progress (claimed), or already running.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := load()
		if err != nil {
			return err
		}
		items, errs := a.Engine.ListReady(cmd.Context(), listSource)
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, "warning:", err)
		}
		if listJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			type row struct {
				Order  int              `json:"order"`
				Status string           `json:"status"`
				Item   any              `json:"item"`
				Ready  engine.Readiness `json:"readiness"`
			}
			var rows []row
			for i, it := range items {
				if listReady && !it.Readiness.Ready {
					continue
				}
				rows = append(rows, row{i + 1, status(it), it.Item, it.Readiness})
			}
			return enc.Encode(rows)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "#\tID\tTITLE\tSTATUS\tDEPENDS ON")
		for i, it := range items {
			if listReady && !it.Readiness.Ready {
				continue
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", i+1, it.Item.ID, prompt.Trunc(it.Item.Title, 60), status(it), strings.Join(it.Item.DependsOn, ", "))
		}
		return tw.Flush()
	},
}

func status(it engine.ReadyItem) string {
	rd := it.Readiness
	switch {
	case rd.ActiveRun != nil:
		return "running (" + string(rd.ActiveRun.Phase) + ")"
	case rd.InProgress:
		if it.Item.ClaimedBy != "" {
			return "in progress (" + it.Item.ClaimedBy + ")"
		}
		return "in progress"
	case len(rd.OpenDeps) > 0:
		return "blocked by " + strings.Join(rd.OpenDeps, ", ")
	}
	return "ready"
}

func init() {
	listCmd.Flags().StringVarP(&listSource, "source", "s", "", "only items from this source (name or type)")
	listCmd.Flags().BoolVar(&listJSON, "json", false, "print JSON")
	listCmd.Flags().BoolVar(&listReady, "ready", false, "only items that can be started now")
}

var showPrompt bool

var showCmd = &cobra.Command{
	Use:   "show <item>",
	Short: "Show one backlog item, optionally with the rendered session prompt",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := load()
		if err != nil {
			return err
		}
		it, err := a.Sources.Resolve(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if showPrompt {
			d := &prompt.Data{Project: a.Cfg.Name, Item: it, Branch: "<branch>", Base: a.Cfg.Repo.Base, Workdir: "<workdir>", RunID: "<run>", SummaryFile: "<summary.md>", Attempt: 1}
			if sc := a.Cfg.Source(it.Source); sc != nil && !sc.Comments.Loaded() {
				it.Comments = nil
			}
			text, err := prompt.RenderFile(prompt.TplSession, a.Cfg.Resolve(a.Cfg.Prompts.Session), d)
			if err != nil {
				return err
			}
			fmt.Print(text)
			return nil
		}
		rd := a.Engine.Check(cmd.Context(), it)
		fmt.Printf("%s  %s\n", it.ID, it.Title)
		if it.URL != "" {
			fmt.Printf("url:        %s\n", it.URL)
		}
		fmt.Printf("created:    %s\n", it.Created.Format("2006-01-02 15:04"))
		fmt.Printf("status:     %s\n", status(engine.ReadyItem{Item: it, Readiness: rd}))
		if len(it.Labels) > 0 {
			fmt.Printf("labels:     %s\n", strings.Join(it.Labels, ", "))
		}
		if len(it.DependsOn) > 0 {
			fmt.Printf("depends on: %s\n", strings.Join(it.DependsOn, ", "))
		}
		if it.Model != "" {
			fmt.Printf("model:      %s\n", it.Model)
		}
		fmt.Printf("comments:   %d\n\n%s\n", len(it.Comments), it.Body)
		return nil
	},
}

func init() {
	showCmd.Flags().BoolVar(&showPrompt, "prompt", false, "print the rendered session prompt instead")
}
