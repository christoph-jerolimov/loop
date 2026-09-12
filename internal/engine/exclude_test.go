package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureExcludedIsIdempotent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", dir)
	// Pre-existing content without a trailing newline must stay intact.
	exclude := filepath.Join(dir, ".git", "info", "exclude")
	os.MkdirAll(filepath.Dir(exclude), 0o755)
	os.WriteFile(exclude, []byte("*.tmp"), 0o644)

	e := &Engine{}
	for i := 0; i < 3; i++ {
		e.ensureExcluded(dir, ".claude/skills/team")
	}
	e.ensureExcluded(dir, ".claude/skills/other")
	got, _ := os.ReadFile(exclude)
	want := "*.tmp\n.claude/skills/team\n.claude/skills/other\n"
	if string(got) != want {
		t.Errorf("exclude file = %q, want %q", got, want)
	}
	if strings.Count(string(got), "team") != 1 {
		t.Errorf("pattern appended more than once:\n%s", got)
	}
}
