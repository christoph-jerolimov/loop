// Package item defines the backlog item model shared by every source,
// plus dependency parsing and pick-up ordering.
package item

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Comment is a ticket comment.
type Comment struct {
	Author  string    `json:"author"`
	Body    string    `json:"body"`
	Created time.Time `json:"created"`
	URL     string    `json:"url,omitempty"`
}

// Item is one backlog entry, independent of where it came from.
type Item struct {
	// ID is unique across sources: "<source>:<native id>".
	ID string `json:"id"`
	// NativeID is the id inside the source: "123", "PROJ-7", "my-idea".
	NativeID   string `json:"native_id"`
	Source     string `json:"source"`
	SourceType string `json:"source_type"`
	// SourceIndex is the position of the source in loop.yaml. Used for ordering.
	SourceIndex int `json:"source_index"`

	Title   string    `json:"title"`
	Body    string    `json:"body"`
	URL     string    `json:"url,omitempty"`
	Labels  []string  `json:"labels,omitempty"`
	Created time.Time `json:"created"`

	Closed     bool   `json:"closed"`
	InProgress bool   `json:"in_progress"`
	ClaimedBy  string `json:"claimed_by,omitempty"`

	Comments []Comment `json:"comments,omitempty"`
	// DependsOn holds raw references as written by the author.
	DependsOn []string `json:"depends_on,omitempty"`
	// Model overrides the agent model for this item.
	Model string `json:"model,omitempty"`
	// Extra carries source-specific values, e.g. the GitHub node id.
	Extra map[string]string `json:"extra,omitempty"`
}

// MakeID builds the cross-source item id.
func MakeID(source, native string) string { return source + ":" + native }

var (
	dependsRe = regexp.MustCompile(`(?im)^\s*depends[ -]on:\s*(.+?)\s*$`)
	modelRe   = regexp.MustCompile(`(?im)^\s*model:\s*(\S+)\s*$`)
	splitRe   = regexp.MustCompile(`[,\s]+`)
)

// ParseBodyDirectives extracts "depends on:" and "model:" lines from a body.
func ParseBodyDirectives(body string) (deps []string, model string) {
	for _, m := range dependsRe.FindAllStringSubmatch(body, -1) {
		for _, ref := range splitRe.Split(m[1], -1) {
			ref = strings.Trim(ref, " \t;")
			if ref == "" || strings.EqualFold(ref, "and") {
				continue
			}
			deps = append(deps, ref)
		}
	}
	if m := modelRe.FindStringSubmatch(body); m != nil {
		model = m[1]
	}
	return deps, model
}

// ApplyBodyDirectives merges directives found in the body into the item.
func (it *Item) ApplyBodyDirectives() {
	deps, model := ParseBodyDirectives(it.Body)
	it.DependsOn = appendUnique(it.DependsOn, deps...)
	if it.Model == "" {
		it.Model = model
	}
}

func appendUnique(dst []string, src ...string) []string {
	seen := map[string]bool{}
	for _, s := range dst {
		seen[s] = true
	}
	for _, s := range src {
		if !seen[s] {
			seen[s] = true
			dst = append(dst, s)
		}
	}
	return dst
}

// HasLabel reports whether the item carries the label (case-insensitive).
func (it *Item) HasLabel(l string) bool {
	for _, x := range it.Labels {
		if strings.EqualFold(x, l) {
			return true
		}
	}
	return false
}

// Order sorts items into pick-up order: by source position in loop.yaml,
// then oldest first, then by native id for stability.
func Order(items []*Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.SourceIndex != b.SourceIndex {
			return a.SourceIndex < b.SourceIndex
		}
		if !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		return a.NativeID < b.NativeID
	})
}

// Slug turns text into a branch- and file-friendly token.
func Slug(s string, max int) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if max > 0 && len(out) > max {
		out = strings.Trim(out[:max], "-")
	}
	return out
}
