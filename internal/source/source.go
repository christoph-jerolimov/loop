// Package source defines the backlog source interface and cross-source
// reference resolution.
package source

import (
	"context"
	"fmt"
	"strings"

	"github.com/christoph-jerolimov/loop/internal/item"
)

// Source is one backlog provider (markdown folder, GitHub issues, Jira).
type Source interface {
	Name() string
	Type() string
	// List returns open items matching the configured filters, oldest first.
	List(ctx context.Context) ([]*item.Item, error)
	// Get returns one item by native id, regardless of state.
	Get(ctx context.Context, nativeID string) (*item.Item, error)
	// Resolve reports whether ref addresses an item of this source and
	// returns its native id.
	Resolve(ref string) (nativeID string, ok bool)
	// Claim marks an item as in progress by the given run. A no-op when
	// claiming is disabled for the source.
	Claim(ctx context.Context, it *item.Item, runID string) error
	// Release removes the claim.
	Release(ctx context.Context, it *item.Item) error
	// Close marks the item done.
	Close(ctx context.Context, it *item.Item, message string) error
	// Comment posts a note on the item; sources without comments may no-op.
	Comment(ctx context.Context, it *item.Item, body string) error
	// SupportsAutoClose reports whether the platform closes the item when a
	// PR with a closing keyword merges.
	SupportsAutoClose() bool
}

// Set is the ordered list of configured sources.
type Set []Source

// ByName returns the source with the given name.
func (s Set) ByName(name string) Source {
	for _, src := range s {
		if src.Name() == name {
			return src
		}
	}
	return nil
}

// Resolve finds the item addressed by ref. Accepted forms are the full
// "<source>:<id>" id, or anything a source recognises (e.g. "#12",
// "PROJ-4", "auth.md"). When several sources match, the first one that
// actually has the item wins.
func (s Set) Resolve(ctx context.Context, ref string) (*item.Item, error) {
	ref = strings.TrimSpace(ref)
	if name, native, ok := strings.Cut(ref, ":"); ok && !strings.Contains(name, "/") {
		if src := s.ByName(name); src != nil {
			return src.Get(ctx, native)
		}
	}
	var lastErr error
	for _, src := range s {
		native, ok := src.Resolve(ref)
		if !ok {
			continue
		}
		it, err := src.Get(ctx, native)
		if err == nil {
			return it, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no source recognises reference %q", ref)
}

// Dependency is one resolved dependency of an item.
type Dependency struct {
	Ref    string
	Item   *item.Item // nil when unresolved
	Closed bool
	Err    error
}

// Dependencies resolves every reference of the item.
func (s Set) Dependencies(ctx context.Context, it *item.Item) []Dependency {
	var out []Dependency
	for _, ref := range it.DependsOn {
		d := Dependency{Ref: ref}
		dep, err := s.Resolve(ctx, ref)
		switch {
		case err != nil:
			d.Err = err
		case dep.Recurring():
			// A recurring item is never closed, so nothing can wait for it.
			d.Item = dep
			d.Err = fmt.Errorf("%s is a recurring item (every %s) and never closes", dep.ID, dep.Every)
		default:
			d.Item = dep
			d.Closed = dep.Closed
		}
		out = append(out, d)
	}
	return out
}

// OpenDependencies returns the references that still block the item.
func OpenDependencies(deps []Dependency) []string {
	var open []string
	for _, d := range deps {
		if d.Err != nil || !d.Closed {
			open = append(open, d.Ref)
		}
	}
	return open
}
