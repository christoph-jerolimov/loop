package markdown

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/item"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListClaimClose(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "auth.md", "---\ntitle: Add auth\ncreated: 2026-01-02\nlabels: [ready]\n---\nBuild login.\n")
	write(t, dir, "search.md", "# Search\n\nDepends on: auth.md\nmodel: claude-opus-5\n")
	write(t, dir, "done.md", "---\nstatus: closed\n---\nold\n")

	s := New(config.SourceConfig{Name: "backlog", Type: "markdown", Claim: true}, 0, dir)
	items, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].NativeID != "auth" {
		t.Errorf("first item = %s, want auth (older)", items[0].NativeID)
	}
	search := items[1]
	if search.Title != "Search" || search.Model != "claude-opus-5" || len(search.DependsOn) != 1 || search.DependsOn[0] != "auth.md" {
		t.Errorf("search item parsed wrong: %+v", search)
	}
	if id, ok := s.Resolve("auth.md"); !ok || id != "auth" {
		t.Errorf("resolve auth.md = %q,%v", id, ok)
	}
	if _, ok := s.Resolve("#12"); ok {
		t.Error("should not resolve GitHub refs")
	}

	if err := s.Claim(context.Background(), items[0], "run-1"); err != nil {
		t.Fatal(err)
	}
	it, _ := s.Get(context.Background(), "auth")
	if !it.InProgress || it.ClaimedBy != "run-1" || it.Title != "Add auth" || !it.HasLabel("ready") {
		t.Errorf("claim not persisted: %+v", it)
	}
	if err := s.Close(context.Background(), it, "merged"); err != nil {
		t.Fatal(err)
	}
	it, _ = s.Get(context.Background(), "auth")
	if !it.Closed {
		t.Error("close not persisted")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "auth.md"))
	if !strings.Contains(string(raw), "Build login.") {
		t.Errorf("body lost on rewrite: %s", raw)
	}
}

func TestEditPreservesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	original := "---\n# planning notes\ntitle: Add auth   # keep this comment\ncreated: 2026-01-02\npriority: 3\nlabels: [ready, auth]\n---\nBody stays.\n\n- list item\n"
	write(t, dir, "auth.md", original)
	s := New(config.SourceConfig{Name: "backlog", Type: "markdown", Claim: true}, 0, dir)
	it, err := s.Get(context.Background(), "auth")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(context.Background(), it, "run-7"); err != nil {
		t.Fatal(err)
	}
	got := string(readFile(t, dir, "auth.md"))
	for _, want := range []string{"# planning notes\n", "title: Add auth # keep this comment\n", "created: 2026-01-02\n", "priority: 3\n", "labels: [ready, auth]\n", "status: in-progress\n", "loop_run: run-7\n", "Body stays.\n\n- list item\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("after claim, missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "title:") > strings.Index(got, "created:") || strings.Index(got, "labels:") > strings.Index(got, "status:") {
		t.Errorf("key order changed:\n%s", got)
	}
	if err := s.Release(context.Background(), it); err != nil {
		t.Fatal(err)
	}
	got = string(readFile(t, dir, "auth.md"))
	if strings.Contains(got, "loop_run") || !strings.Contains(got, "status: open\n") {
		t.Errorf("release did not reset the claim:\n%s", got)
	}
	if err := s.Close(context.Background(), it, "merged #3"); err != nil {
		t.Fatal(err)
	}
	got = string(readFile(t, dir, "auth.md"))
	if !strings.Contains(got, "status: closed\n") || !strings.Contains(got, "closed_note: 'merged #3'\n") || !strings.Contains(got, "created: 2026-01-02\n") {
		t.Errorf("close rewrote the header badly:\n%s", got)
	}
}

func TestEditCreatesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "idea.md", "# Idea\n\nJust a body.\n")
	s := New(config.SourceConfig{Name: "backlog", Type: "markdown", Claim: true}, 0, dir)
	it, _ := s.Get(context.Background(), "idea")
	if err := s.Claim(context.Background(), it, "run-1"); err != nil {
		t.Fatal(err)
	}
	got := string(readFile(t, dir, "idea.md"))
	if !strings.HasPrefix(got, "---\nloop_run: run-1\nstatus: in-progress\n---\n# Idea\n\nJust a body.\n") {
		t.Errorf("unexpected file:\n%s", got)
	}
}

func readFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCommentAppendsLoopLog(t *testing.T) {
	dir := t.TempDir()
	// No trailing newline: the log must still start on its own line.
	write(t, dir, "auth.md", "---\ntitle: Add auth\nlabels: [ready]\n---\nBuild login.")
	s := New(config.SourceConfig{Name: "backlog", Type: "markdown"}, 0, dir)
	it, err := s.Get(context.Background(), "auth")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Comment(context.Background(), it, "  opened https://github.com/o/r/pull/1  \n"); err != nil {
		t.Fatal(err)
	}
	if err := s.Comment(context.Background(), it, "merged"); err != nil {
		t.Fatal(err)
	}
	raw := string(readFile(t, dir, "auth.md"))
	if strings.Count(raw, "## Loop log") != 1 {
		t.Errorf("the log heading must be written once:\n%s", raw)
	}
	if !strings.HasPrefix(raw, "---\ntitle: Add auth\nlabels: [ready]\n---\nBuild login.\n\n## Loop log\n\n- ") {
		t.Errorf("frontmatter, body and heading not preserved in order:\n%s", raw)
	}
	opened := strings.Index(raw, ": opened https://github.com/o/r/pull/1\n")
	merged := strings.Index(raw, ": merged\n")
	if opened < 0 || merged < 0 || merged < opened {
		t.Errorf("entries missing, untrimmed or out of order:\n%s", raw)
	}
	if !strings.HasSuffix(raw, ": merged\n") {
		t.Errorf("file must end with the last entry and a newline:\n%q", raw)
	}
	// The item still parses, with the log as part of its body.
	it, err = s.Get(context.Background(), "auth")
	if err != nil {
		t.Fatal(err)
	}
	if it.Title != "Add auth" || !it.HasLabel("ready") || !strings.Contains(it.Body, "## Loop log") {
		t.Errorf("item after comments: %+v", it)
	}
}

func TestCommentKeepsExistingLoopLog(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "auth.md", "# Auth\n\nBody.\n\n## Loop log\n\n- 2026-01-01 10:00: earlier entry\n")
	s := New(config.SourceConfig{Name: "backlog", Type: "markdown"}, 0, dir)
	it, err := s.Get(context.Background(), "auth")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Comment(context.Background(), it, "later entry"); err != nil {
		t.Fatal(err)
	}
	raw := string(readFile(t, dir, "auth.md"))
	if strings.Count(raw, "## Loop log") != 1 || !strings.Contains(raw, "- 2026-01-01 10:00: earlier entry\n\n- ") || !strings.HasSuffix(raw, ": later entry\n") {
		t.Errorf("entry must be appended under the existing heading:\n%s", raw)
	}
}

func TestCommentUnknownItem(t *testing.T) {
	s := New(config.SourceConfig{Name: "backlog", Type: "markdown"}, 0, t.TempDir())
	err := s.Comment(context.Background(), &item.Item{NativeID: "missing"}, "x")
	if err == nil {
		t.Error("commenting on a missing file must fail")
	}
}
