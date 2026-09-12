package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/christoph-jerolimov/loop/internal/state"
)

var (
	logsSession int
	logsPrompt  bool
	logsFollow  bool
	logsLines   int
)

var logsCmd = &cobra.Command{
	Use:   "logs <run>",
	Short: "Show the run log or an agent session transcript",
	Long: `Without flags prints the run log (.loop/runs/<run>/run.log): every phase
change, session start and end, push, poll result and error.

--session prints the raw transcript of an agent session instead, by default
the latest one; --session=2 selects the second. --prompt prints the exact
prompt that session received. --follow keeps printing as the file grows,
which is the easiest way to watch a running session.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := load()
		if err != nil {
			return err
		}
		r, err := a.Engine.Store.Find(args[0])
		if err != nil {
			return err
		}
		path, err := logPath(r, cmd.Flags().Changed("session"))
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "# %s\n", path)
		return printLog(cmd.Context(), path, logsLines, logsFollow)
	},
}

func logPath(r *state.Run, sessionFlag bool) (string, error) {
	if !sessionFlag && !logsPrompt {
		return r.LogFile(), nil
	}
	if len(r.Sessions) == 0 {
		return "", fmt.Errorf("run %s has no agent sessions yet", r.ID)
	}
	n := logsSession
	if n <= 0 {
		n = len(r.Sessions)
	}
	if n > len(r.Sessions) {
		return "", fmt.Errorf("run %s has %d session(s), no session %d", r.ID, len(r.Sessions), n)
	}
	s := r.Sessions[n-1]
	if logsPrompt {
		return strings.TrimSuffix(s.LogFile, ".log") + ".prompt.md", nil
	}
	return s.LogFile, nil
}

// printLog writes the last n lines of path (n <= 0: all) and, with follow,
// keeps streaming appended data until the context ends.
func printLog(ctx context.Context, path string, n int, follow bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if n > 0 {
		var ring []string
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			ring = append(ring, sc.Text())
			if len(ring) > n {
				ring = ring[1:]
			}
		}
		for _, l := range ring {
			fmt.Println(l)
		}
	} else if _, err := io.Copy(os.Stdout, f); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
		if _, err := io.Copy(os.Stdout, f); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
	}
}

func init() {
	logsCmd.Flags().IntVar(&logsSession, "session", 0, "print an agent session transcript instead (0 = latest)")
	logsCmd.Flags().Lookup("session").NoOptDefVal = "0"
	logsCmd.Flags().BoolVar(&logsPrompt, "prompt", false, "print the prompt of the selected session")
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "keep printing as the file grows")
	logsCmd.Flags().IntVarP(&logsLines, "lines", "n", 0, "only the last N lines (default: all)")
	root.AddCommand(logsCmd)
}
