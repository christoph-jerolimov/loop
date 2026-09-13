// Package item defines the backlog item model shared by every source,
// plus dependency parsing and pick-up ordering.
package item

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Comment is a ticket comment.
type Comment struct {
	Author  string    `json:"author" yaml:"author"`
	Body    string    `json:"body" yaml:"body"`
	Created time.Time `json:"created" yaml:"created"`
	URL     string    `json:"url,omitempty" yaml:"url,omitempty"`
}

// Item is one backlog entry, independent of where it came from.
type Item struct {
	// ID is unique across sources: "<source>:<native id>".
	ID string `json:"id" yaml:"id"`
	// NativeID is the id inside the source: "123", "PROJ-7", "my-idea".
	NativeID   string `json:"native_id" yaml:"native_id"`
	Source     string `json:"source" yaml:"source"`
	SourceType string `json:"source_type" yaml:"source_type"`
	// SourceIndex is the position of the source in loop.yaml. Used for ordering.
	SourceIndex int `json:"source_index" yaml:"source_index"`

	Title   string    `json:"title" yaml:"title"`
	Body    string    `json:"body" yaml:"body"`
	URL     string    `json:"url,omitempty" yaml:"url,omitempty"`
	Labels  []string  `json:"labels,omitempty" yaml:"labels,omitempty"`
	Created time.Time `json:"created" yaml:"created"`

	Closed     bool   `json:"closed" yaml:"closed"`
	InProgress bool   `json:"in_progress" yaml:"in_progress"`
	ClaimedBy  string `json:"claimed_by,omitempty" yaml:"claimed_by,omitempty"`

	Comments []Comment `json:"comments,omitempty" yaml:"comments,omitempty"`
	// DependsOn holds raw references as written by the author.
	DependsOn []string `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	// Model overrides the agent model for this item.
	Model string `json:"model,omitempty" yaml:"model,omitempty"`
	// Priority orders items ahead of source order and age: 1 is the
	// highest, 0 means none. See ParsePriority for the accepted spellings.
	Priority int `json:"priority,omitempty" yaml:"priority,omitempty"`
	// Extra carries source-specific values, e.g. the GitHub node id.
	Extra map[string]string `json:"extra,omitempty" yaml:"extra,omitempty"`
}

// MakeID builds the cross-source item id.
func MakeID(source, native string) string { return source + ":" + native }

var (
	dependsRe = regexp.MustCompile(`(?im)^\s*depends[ -]on:\s*(.+?)\s*$`)
	modelRe   = regexp.MustCompile(`(?im)^\s*model:\s*(\S+)\s*$`)
	prioRe    = regexp.MustCompile(`(?im)^\s*prio(?:rity)?:\s*(\S+)\s*$`)
	labelRe   = regexp.MustCompile(`(?i)^prio(?:rity)?\s*[:/=-]\s*(.+)$`)
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
	if it.Priority == 0 {
		if m := prioRe.FindStringSubmatch(it.Body); m != nil {
			it.Priority = ParsePriority(m[1])
		}
	}
}

// ParsePriority turns a priority spelling into a rank: 1 is the highest.
// Accepted: the numbers 1 to 9, P0 to P9 (P0 is 1), and the words
// highest, critical, blocker, urgent (1), high (2), medium, normal,
// major (3), low, minor (4), lowest, trivial (5). Anything else is 0.
func ParsePriority(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "highest", "critical", "blocker", "urgent":
		return 1
	case "high":
		return 2
	case "medium", "normal", "major":
		return 3
	case "low", "minor":
		return 4
	case "lowest", "trivial":
		return 5
	}
	if len(s) == 2 && s[0] == 'p' && s[1] >= '0' && s[1] <= '9' {
		return int(s[1]-'0') + 1
	}
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		return int(s[0] - '0')
	}
	return 0
}

// PriorityFromLabel reads a priority from a label such as "priority: high",
// "priority/high", "prio-2" or "P1"; 0 when the label is not one.
func PriorityFromLabel(label string) int {
	if m := labelRe.FindStringSubmatch(strings.TrimSpace(label)); m != nil {
		return ParsePriority(m[1])
	}
	return ParsePriority(label)
}

// PriorityName renders a rank for people: highest, high, medium, low,
// lowest, the number for other ranks, and "" for none.
func PriorityName(p int) string {
	switch p {
	case 0:
		return ""
	case 1:
		return "highest"
	case 2:
		return "high"
	case 3:
		return "medium"
	case 4:
		return "low"
	case 5:
		return "lowest"
	}
	return strconv.Itoa(p)
}

func priorityRank(p int) int {
	if p == 0 {
		return 1 << 20 // unset sorts after every set priority
	}
	return p
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
		if pa, pb := priorityRank(a.Priority), priorityRank(b.Priority); pa != pb {
			return pa < pb
		}
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
