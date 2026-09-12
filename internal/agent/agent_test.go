package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCLI records argv and stdin to files in dir and prints one result event.
const fakeCLI = `#!/bin/sh
if [ "$1" = "create-chat" ]; then echo chat-123; exit 0; fi
printf '%s\n' "$@" > "$FAKE_DIR/argv"
cat > "$FAKE_DIR/stdin"
echo '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"abc"}'
`

func setupFake(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(fakeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(b)
}

func TestClaudePromptOnStdin(t *testing.T) {
	dir := setupFake(t, "claude")
	res, err := (Claude{}).Run(context.Background(), Options{Workdir: dir, Prompt: "do the thing", Model: "m1", MaxTurns: 3})
	if err != nil {
		t.Fatal(err)
	}
	argv := read(t, filepath.Join(dir, "argv"))
	if !strings.Contains(argv, "--session-id\n"+res.SessionID) || !strings.Contains(argv, "--model\nm1") || strings.Contains(argv, "do the thing") {
		t.Errorf("unexpected argv:\n%s", argv)
	}
	if got := read(t, filepath.Join(dir, "stdin")); got != "do the thing" {
		t.Errorf("stdin = %q", got)
	}
}

func TestCursorShortPromptAsArgument(t *testing.T) {
	dir := setupFake(t, "agent")
	res, err := (Cursor{}).Run(context.Background(), Options{Workdir: dir, Prompt: "short task", PromptFile: "/p.md"})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "chat-123" {
		t.Errorf("session id = %q, want the created chat id", res.SessionID)
	}
	argv := read(t, filepath.Join(dir, "argv"))
	if !strings.HasSuffix(argv, "short task\n") || !strings.Contains(argv, "--resume\nchat-123") {
		t.Errorf("unexpected argv:\n%s", argv)
	}
	if got := read(t, filepath.Join(dir, "stdin")); got != "" {
		t.Errorf("short prompts must not use stdin, got %q", got)
	}
}

func TestCursorLongPromptViaStdinAndFile(t *testing.T) {
	dir := setupFake(t, "agent")
	long := strings.Repeat("ticket comment line\n", 2000) // ~40 KiB
	if _, err := (Cursor{}).Run(context.Background(), Options{Workdir: dir, Prompt: long, PromptFile: "/run/session-01.prompt.md"}); err != nil {
		t.Fatal(err)
	}
	argv := read(t, filepath.Join(dir, "argv"))
	if strings.Contains(argv, "ticket comment line") || !strings.Contains(argv, "/run/session-01.prompt.md") {
		t.Errorf("long prompt must not be an argument; argv:\n%s", argv[:min(len(argv), 400)])
	}
	if got := read(t, filepath.Join(dir, "stdin")); got != long {
		t.Errorf("stdin should carry the full prompt (%d bytes), got %d", len(long), len(got))
	}
}

func TestClaudeSettings(t *testing.T) {
	b, err := ClaudeSettings("acceptEdits", []string{"Bash(npm test:*)"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"defaultMode": "acceptEdits"`, `"allow": [`, `"Bash(npm test:*)"`, `"deny": []`} {
		if !strings.Contains(got, want) {
			t.Errorf("settings missing %q:\n%s", want, got)
		}
	}
}
