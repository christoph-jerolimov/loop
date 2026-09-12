// Command loop orchestrates AI coding sessions from a backlog of ideas,
// goals and tickets and drives the resulting pull requests to merge.
package main

import (
	"os"

	"github.com/christoph-jerolimov/loop/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
