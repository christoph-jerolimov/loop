// Package markdown implements a backlog source backed by a folder of
// markdown files with optional YAML frontmatter.
//
// Frontmatter keys: id, title, status (open|in-progress|closed), created,
// labels, depends_on, model, loop_run. A missing title falls back to the
// first "# " heading, then to the file name.
package markdown

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/item"
)

// Source is a folder of markdown files.
type Source struct {
	cfg   config.SourceConfig
	index int
	dir   string
}

// New creates the source. dir is the absolute backlog folder.
func New(cfg config.SourceConfig, index int, dir string) *Source {
	return &Source{cfg: cfg, index: index, dir: dir}
}

func (s *Source) Name() string            { return s.cfg.Name }
func (s *Source) Type() string            { return "markdown" }
func (s *Source) SupportsAutoClose() bool { return false }

// Frontmatter is the parsed YAML header.
type Frontmatter struct {
	ID        string   `yaml:"id,omitempty"`
	Title     string   `yaml:"title,omitempty"`
	Status    string   `yaml:"status,omitempty"`
	Created   string   `yaml:"created,omitempty"`
	Labels    []string `yaml:"labels,omitempty"`
	DependsOn []string `yaml:"depends_on,omitempty"`
	Model     string   `yaml:"model,omitempty"`
	LoopRun   string   `yaml:"loop_run,omitempty"`
	Closed    string   `yaml:"closed,omitempty"`
	// Rest keeps unknown keys so rewriting the file does not lose them.
	Rest map[string]any `yaml:",inline"`
}

var headingRe = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)

// Parse splits a markdown document into frontmatter and body.
func Parse(raw []byte) (*Frontmatter, string, error) {
	fm := &Frontmatter{}
	header, body, has := splitHeader(raw)
	if !has {
		return fm, body, nil
	}
	if err := yaml.Unmarshal([]byte(header), fm); err != nil {
		return nil, "", fmt.Errorf("frontmatter: %w", err)
	}
	return fm, body, nil
}

func (s *Source) files() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read backlog folder %s: %w", s.dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") || strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		out = append(out, filepath.Join(s.dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

func nativeID(path string, fm *Frontmatter) string {
	if fm.ID != "" {
		return fm.ID
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func (s *Source) load(path string) (*item.Item, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, body, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	native := nativeID(path, fm)
	it := &item.Item{
		ID:          item.MakeID(s.cfg.Name, native),
		NativeID:    native,
		Source:      s.cfg.Name,
		SourceType:  "markdown",
		SourceIndex: s.index,
		Title:       fm.Title,
		Body:        strings.TrimSpace(body),
		URL:         path,
		Labels:      fm.Labels,
		DependsOn:   fm.DependsOn,
		Model:       fm.Model,
		ClaimedBy:   fm.LoopRun,
		Extra:       map[string]string{"path": path},
	}
	if it.Title == "" {
		if m := headingRe.FindStringSubmatch(body); m != nil {
			it.Title = m[1]
			it.Body = strings.TrimSpace(strings.Replace(body, m[0], "", 1))
		} else {
			it.Title = native
		}
	}
	st, _ := os.Stat(path)
	if fm.Created != "" {
		it.Created = parseDate(fm.Created)
	}
	if it.Created.IsZero() && st != nil {
		it.Created = st.ModTime()
	}
	status := strings.ToLower(strings.TrimSpace(fm.Status))
	it.Closed = status == "closed" || status == "done" || strings.EqualFold(fm.Closed, "true")
	it.InProgress = status == "in-progress" || status == "in progress" || fm.LoopRun != ""
	it.ApplyBodyDirectives()
	return it, nil
}

func parseDate(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Time{}
}

// List returns open items in the folder, oldest first.
func (s *Source) List(ctx context.Context) ([]*item.Item, error) {
	files, err := s.files()
	if err != nil {
		return nil, err
	}
	var out []*item.Item
	for _, f := range files {
		it, err := s.load(f)
		if err != nil {
			return nil, err
		}
		if it.Closed {
			continue
		}
		if !s.matchesLabels(it) {
			continue
		}
		out = append(out, it)
	}
	item.Order(out)
	return out, nil
}

func (s *Source) matchesLabels(it *item.Item) bool {
	for _, l := range s.cfg.Labels {
		if !it.HasLabel(l) {
			return false
		}
	}
	return true
}

func (s *Source) pathFor(native string) (string, error) {
	files, err := s.files()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		if base == native {
			return f, nil
		}
	}
	// Fall back to an explicit frontmatter id.
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		fm, _, err := Parse(raw)
		if err == nil && fm.ID == native {
			return f, nil
		}
	}
	return "", fmt.Errorf("markdown item %q not found in %s", native, s.dir)
}

// Get loads one item by file base name or frontmatter id.
func (s *Source) Get(ctx context.Context, native string) (*item.Item, error) {
	p, err := s.pathFor(native)
	if err != nil {
		return nil, err
	}
	return s.load(p)
}

// Resolve accepts "name", "name.md", "<folder>/name.md" and absolute paths
// inside the folder.
func (s *Source) Resolve(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "://") || strings.HasPrefix(ref, "#") {
		return "", false
	}
	base := filepath.Base(ref)
	if strings.Contains(ref, "/") {
		dir := filepath.Dir(ref)
		abs := dir
		if !filepath.IsAbs(dir) {
			abs = filepath.Join(filepath.Dir(s.dir), dir)
		}
		if filepath.Clean(abs) != filepath.Clean(s.dir) && dir != filepath.Base(s.dir) {
			return "", false
		}
	}
	if !strings.HasSuffix(strings.ToLower(base), ".md") {
		// A bare token is only ours when a file with that name exists.
		if _, err := s.pathFor(base); err != nil {
			return "", false
		}
		return base, true
	}
	return strings.TrimSuffix(base, filepath.Ext(base)), true
}

// splitHeader separates a document into its raw frontmatter text (without
// the --- fences), the body, and whether a header was present.
func splitHeader(raw []byte) (header, body string, has bool) {
	text := string(raw)
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return "", text, false
	}
	rest := text[strings.Index(text, "\n")+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", text, false
	}
	header = rest[:end]
	body = rest[end+4:]
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	return header, body, true
}

// edit rewrites the frontmatter of an item through a yaml.Node so that key
// order, comments and scalar styles (for example an unquoted date) are
// preserved. changes maps keys to new values; a nil value removes the key.
func (s *Source) edit(native string, changes map[string]*string) error {
	p, err := s.pathFor(native)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	header, body, _ := splitHeader(raw)
	var doc yaml.Node
	if strings.TrimSpace(header) != "" {
		if err := yaml.Unmarshal([]byte(header), &doc); err != nil {
			return fmt.Errorf("%s: frontmatter: %w", p, err)
		}
	}
	m := mappingOf(&doc)
	for _, key := range sortedKeys(changes) {
		if v := changes[key]; v == nil {
			deleteKey(m, key)
		} else {
			setKey(m, key, *v)
		}
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	buf.WriteString("---\n")
	buf.WriteString(body)
	return os.WriteFile(p, buf.Bytes(), 0o644)
}

// mappingOf returns the top-level mapping of a document node, creating an
// empty document and mapping when needed.
func mappingOf(doc *yaml.Node) *yaml.Node {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	return doc.Content[0]
}

func setKey(m *yaml.Node, key, val string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			v.Kind, v.Tag, v.Value, v.Style, v.Content = yaml.ScalarNode, "!!str", val, 0, nil
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
}

func deleteKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func sortedKeys(m map[string]*string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func str(s string) *string { return &s }

// Claim writes status: in-progress and loop_run into the frontmatter.
func (s *Source) Claim(ctx context.Context, it *item.Item, runID string) error {
	if !s.cfg.Claim {
		return nil
	}
	return s.edit(it.NativeID, map[string]*string{"status": str("in-progress"), "loop_run": str(runID)})
}

// Release resets the claim.
func (s *Source) Release(ctx context.Context, it *item.Item) error {
	if !s.cfg.Claim {
		return nil
	}
	changes := map[string]*string{"loop_run": nil}
	if cur, err := s.Get(ctx, it.NativeID); err == nil && cur.InProgress && !cur.Closed {
		changes["status"] = str("open")
	}
	return s.edit(it.NativeID, changes)
}

// Close sets status: closed.
func (s *Source) Close(ctx context.Context, it *item.Item, message string) error {
	changes := map[string]*string{"status": str("closed"), "loop_run": nil}
	if message != "" {
		changes["closed_note"] = str(message)
	}
	return s.edit(it.NativeID, changes)
}

// Comment appends a log line to the file's "Loop log" section.
func (s *Source) Comment(ctx context.Context, it *item.Item, body string) error {
	p, err := s.pathFor(it.NativeID)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	text := string(raw)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if !strings.Contains(text, "\n## Loop log\n") {
		text += "\n## Loop log\n"
	}
	text += fmt.Sprintf("\n- %s: %s\n", time.Now().Format("2006-01-02 15:04"), strings.TrimSpace(body))
	return os.WriteFile(p, []byte(text), 0o644)
}
