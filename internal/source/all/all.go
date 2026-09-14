// Package all registers every source implementation.
package all

import (
	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/source"
	"github.com/christoph-jerolimov/loop/internal/source/github"
	"github.com/christoph-jerolimov/loop/internal/source/gitlab"
	"github.com/christoph-jerolimov/loop/internal/source/jira"
	"github.com/christoph-jerolimov/loop/internal/source/markdown"
)

func init() {
	source.Register("markdown", func(sc config.SourceConfig, i int, root *config.Config) (source.Source, error) {
		return markdown.New(sc, i, root.Resolve(sc.Path)), nil
	})
	source.Register("github", func(sc config.SourceConfig, i int, root *config.Config) (source.Source, error) {
		return github.New(sc, i), nil
	})
	source.Register("gitlab", func(sc config.SourceConfig, i int, root *config.Config) (source.Source, error) {
		return gitlab.New(sc, i), nil
	})
	source.Register("jira", func(sc config.SourceConfig, i int, root *config.Config) (source.Source, error) {
		return jira.New(sc, i), nil
	})
}
