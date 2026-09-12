package markdown

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/christoph-jerolimov/loop/internal/config"
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
