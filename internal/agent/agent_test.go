package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCLI records argv, environment and stdin to files in its working
// directory (the session workdir) and prints one result event.
const fakeCLI = `#!/bin/sh
if [ "$1" = "create-chat" ]; then echo chat-123; exit 0; fi
printf '%s\n' "$@" > argv
env > env
cat > stdin
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

func TestSessionEnvWithholdsSecrets(t *testing.T) {
	dir := setupFake(t, "claude")
	t.Setenv("GITHUB_TOKEN", "ghp_secret")
	t.Setenv("JIRA_API_TOKEN", "jira_secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-agent")
	t.Setenv("MY_APP_DB", "postgres://x")
	t.Setenv("OTHER_SECRET", "nope")
	_, err := (Claude{}).Run(context.Background(), Options{
		Workdir: dir, Prompt: "p",
		Env:            map[string]string{"LOOP_RUN_ID": "r1"},
		EnvPassthrough: []string{"MY_APP_*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := read(t, filepath.Join(dir, "env"))
	for _, absent := range []string{"GITHUB_TOKEN=", "JIRA_API_TOKEN=", "OTHER_SECRET="} {
		if strings.Contains(env, absent) {
			t.Errorf("session environment must not contain %s:\n%s", absent, env)
		}
	}
	for _, present := range []string{"PATH=", "HOME=", "ANTHROPIC_API_KEY=sk-ant-agent", "MY_APP_DB=postgres://x", "LOOP_RUN_ID=r1"} {
		if !strings.Contains(env, present) {
			t.Errorf("session environment missing %s:\n%s", present, env)
		}
	}
}

func TestSessionEnvExtraWins(t *testing.T) {
	t.Setenv("HOME", "/inherited")
	env := SessionEnv(nil, map[string]string{"HOME": "/override"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "HOME=/override") || strings.Contains(joined, "HOME=/inherited") {
		t.Errorf("explicit values must win: %s", joined)
	}
}
