package source

import (
	"context"
	"fmt"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/item"
)

// Builder creates a Source from its config; registered by the concrete
// packages to avoid an import cycle.
type Builder func(cfg config.SourceConfig, index int, root *config.Config) (Source, error)

var builders = map[string]Builder{}

// Register adds a builder for a source type.
func Register(typ string, b Builder) { builders[typ] = b }

// Build instantiates every configured source in order.
func Build(cfg *config.Config) (Set, error) {
	var set Set
	for i, sc := range cfg.Sources {
		b, ok := builders[sc.Type]
		if !ok {
			return nil, fmt.Errorf("source %s: no implementation for type %q", sc.Name, sc.Type)
		}
		s, err := b(sc, i, cfg)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", sc.Name, err)
		}
		set = append(set, s)
	}
	return set, nil
}

// ListAll collects open items from every source, in pick-up order. A
// source that fails is reported in errs and skipped.
func (s Set) ListAll(ctx context.Context, only string) (items []*item.Item, errs []error) {
	for _, src := range s {
		if only != "" && src.Name() != only && src.Type() != only {
			continue
		}
		list, err := src.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("source %s: %w", src.Name(), err))
			continue
		}
		items = append(items, list...)
	}
	item.Order(items)
	return items, errs
}
