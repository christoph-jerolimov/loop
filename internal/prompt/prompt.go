// Package prompt renders the prompt templates that drive agent sessions.
package prompt

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/christoph-jerolimov/loop/internal/item"
)

//go:embed templates/*.md
var Templates embed.FS

// Template names, matching the files in templates/ and the keys in
// loop.yaml prompts:.
const (
	TplSession  = "session"
	TplReview   = "review"
	TplCI       = "ci"
	TplConflict = "conflict"
	TplVerify   = "verify"
	TplPRBody   = "pr-body"
)

// Review is a submitted PR review.
type Review struct {
	Author string
	State  string
	Body   string
	URL    string
}

// ReviewComment is an inline code comment.
type ReviewComment struct {
	// ID identifies the comment so the agent can report what it did with it.
	ID       int64
	Author   string
	Path     string
	Line     int
	Body     string
	DiffHunk string
	URL      string
}

// Check is a failed CI check.
type Check struct {
	Name       string
	Conclusion string
	URL        string
	Summary    string
	Text       string
	// Log is the tail of the GitHub Actions job log, when the check is an Actions job.
	Log string
}

// PRRef identifies the pull request.
type PRRef struct {
	Number int
	URL    string
	Title  string
}

// Data is what templates see.
type Data struct {
	Project     string
	Item        *item.Item
	Branch      string
	Base        string
	Workdir     string
	RunID       string
	SummaryFile string
	Attempt     int
	Round       int

	PR             *PRRef
	Reviews        []Review
	ReviewComments []ReviewComment
	PRComments     []item.Comment
	Checks         []Check
	Conflicts      []string
	VerifyStep     string
	VerifyOutput   string
	Summary        string
	// RepliesFile is where a review fix session records, per inline comment
	// id, what it did, so loop can answer each thread and resolve it.
	RepliesFile string
}

var funcs = template.FuncMap{
	"indent": func(n int, s string) string {
		pad := strings.Repeat(" ", n)
		return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
	},
	"quote": func(s string) string {
		return "> " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ")
	},
	"trunc": func(n int, s string) string {
		if len(s) <= n {
			return s
		}
		return s[:n] + "…"
	},
	"join": strings.Join,
	"trim": strings.TrimSpace,
	"default": func(def, s string) string {
		if s == "" {
			return def
		}
		return s
	},
}

// Load returns the template text: from path when set, otherwise the
// embedded default.
func Load(name, path string) (string, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("prompt %s: %w", name, err)
		}
		return string(b), nil
	}
	b, err := Templates.ReadFile("templates/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("no embedded prompt template %q", name)
	}
	return string(b), nil
}

// Render executes template text with data.
func Render(name, text string, d *Data) (string, error) {
	t, err := template.New(name).Funcs(funcs).Option("missingkey=zero").Parse(text)
	if err != nil {
		return "", fmt.Errorf("prompt %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return "", fmt.Errorf("prompt %s: %w", name, err)
	}
	return strings.TrimSpace(buf.String()) + "\n", nil
}

// RenderFile loads and renders in one step.
func RenderFile(name, path string, d *Data) (string, error) {
	text, err := Load(name, path)
	if err != nil {
		return "", err
	}
	return Render(name, text, d)
}

// Default returns the embedded template text (for `loop init`).
func Default(name string) string {
	b, _ := Templates.ReadFile("templates/" + name + ".md")
	return string(b)
}

// Trunc shortens s to n bytes with an ellipsis.
func Trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
