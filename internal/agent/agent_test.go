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

// runner resolves a built-in profile without overrides.
func runner(t *testing.T, name string) *Runner {
	t.Helper()
	r, err := Resolve(Spec{Runner: name})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// promptFile writes a prompt into the session folder and returns its path.
func promptFile(t *testing.T, dir, text string) string {
	t.Helper()
	p := filepath.Join(dir, "session-01-session.prompt.md")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaudeLoadsPromptFileOntoStdin(t *testing.T) {
	dir := setupFake(t, "claude")
	pf := promptFile(t, dir, "do the thing")
	res, err := runner(t, "claude").Run(context.Background(), Options{Workdir: dir, PromptFile: pf, Model: "m1", MaxTurns: 3})
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

func TestCursorPointsAtPromptFileAndPipesIt(t *testing.T) {
	dir := setupFake(t, "agent")
	long := strings.Repeat("ticket comment line\n", 2000) // ~40 KiB, beyond any argv limit
	pf := promptFile(t, dir, long)
	res, err := runner(t, "cursor").Run(context.Background(), Options{Workdir: dir, PromptFile: pf})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "chat-123" {
		t.Errorf("session id = %q, want the created chat id", res.SessionID)
	}
	argv := read(t, filepath.Join(dir, "argv"))
	if strings.Contains(argv, "ticket comment line") || !strings.Contains(argv, "instructions are in the file "+pf) || !strings.Contains(argv, "--resume\nchat-123") {
		t.Errorf("the argument must point at the prompt file; argv:\n%s", argv[:min(len(argv), 400)])
	}
	if got := read(t, filepath.Join(dir, "stdin")); got != long {
		t.Errorf("stdin should carry the full prompt (%d bytes), got %d", len(long), len(got))
	}
}

func TestRunnersRefuseMissingOrEmptyPrompts(t *testing.T) {
	dir := setupFake(t, "claude")
	setupFake(t, "agent")
	for _, r := range []*Runner{runner(t, "claude"), runner(t, "cursor")} {
		if _, err := r.Run(context.Background(), Options{Workdir: dir}); err == nil || !strings.Contains(err.Error(), "no prompt file") {
			t.Errorf("%s without a prompt file: %v", r.Name, err)
		}
		if _, err := r.Run(context.Background(), Options{Workdir: dir, PromptFile: filepath.Join(dir, "missing.md")}); err == nil || !strings.Contains(err.Error(), "read prompt") {
			t.Errorf("%s with a missing prompt file: %v", r.Name, err)
		}
		empty := promptFile(t, dir, "  \n")
		if _, err := r.Run(context.Background(), Options{Workdir: dir, PromptFile: empty}); err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Errorf("%s with an empty prompt file: %v", r.Name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
		t.Error("the CLI must not be started without a prompt")
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
	_, err := runner(t, "claude").Run(context.Background(), Options{
		Workdir: dir, PromptFile: promptFile(t, dir, "p"),
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
