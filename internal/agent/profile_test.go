package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamesAreOrdered(t *testing.T) {
	if got := strings.Join(Names(), ","); got != "claude,cursor,codex,gemini,aider,opencode,copilot,amp" {
		t.Errorf("Names() = %s", got)
	}
}

func TestResolveRejectsBadSpecs(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{"unknown", Spec{Runner: "copilot-x"}, "agent.runner must be one of claude, cursor, codex"},
		{"custom without command", Spec{Runner: Custom, Args: []string{"{prompt}"}}, "needs agent.command and agent.args"},
		{"custom without args", Spec{Runner: Custom, Command: "x"}, "needs agent.command and agent.args"},
		{"bad prompt_via", Spec{Runner: "claude", PromptVia: "env"}, "agent.prompt_via must be"},
		{"bad session_id", Spec{Runner: "claude", SessionID: "guess"}, "agent.session_id must be"},
		{"args mode without placeholder", Spec{Runner: Custom, Command: "x", Args: []string{"run"}, PromptVia: PromptArgs}, "must contain {prompt} or {prompt_file}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Resolve(c.spec); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestResolveOverridesOneField(t *testing.T) {
	r, err := Resolve(Spec{Runner: "codex", Command: "/opt/codex", Resume: "codex exec resume {session}", ExtraArgs: []string{"--sandbox", "workspace-write"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "codex" || r.Command != "/opt/codex" || r.SessionID != "json:thread_id" || r.PromptVia != PromptArgs || len(r.ExtraArgs) != 2 {
		t.Errorf("resolved = %+v", r)
	}
	if got := r.JoinCommand("/w", "t1"); got != "cd /w && codex exec resume t1" {
		t.Errorf("join = %q", got)
	}
	// Overriding must not change the shared profile.
	if profiles["codex"].Command != "codex" || len(profiles["codex"].ExtraArgs) != 0 {
		t.Error("profile table was modified")
	}
}

func TestExpandDropsEmptyPlaceholdersWithTheirFlag(t *testing.T) {
	vars := map[string]string{"model": "", "session": "s1", "prompt": "do it", "max_turns": ""}
	got := expand([]string{"-p", "--model", "{model}", "--resume", "{session}", "--max-turns", "{max_turns}", "--out=x", "{prompt}", "x{model}y"}, vars)
	if want := "-p --resume s1 --out=x do it xy"; strings.Join(got, " ") != want {
		t.Errorf("expand = %q, want %q", strings.Join(got, " "), want)
	}
}

// argvOf runs the profile against the fake CLI and returns argv and stdin.
func argvOf(t *testing.T, name string, spec Spec, prompt string, o Options) (argv, stdin string) {
	t.Helper()
	r, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	dir := setupFake(t, r.Command)
	o.Workdir = dir
	o.PromptFile = promptFile(t, dir, prompt)
	if _, err := r.Run(context.Background(), o); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return read(t, filepath.Join(dir, "argv")), read(t, filepath.Join(dir, "stdin"))
}

func TestBuiltInProfilesCommandLines(t *testing.T) {
	cases := []struct {
		runner    string
		model     string
		wantArgv  []string // substrings of the newline-joined argv
		noArgv    []string
		wantStdin string
	}{
		{"claude", "m1", []string{"-p\n--output-format\nstream-json\n--verbose\n--session-id\n", "--permission-mode\nacceptEdits", "--model\nm1", "--max-turns\n3"}, []string{"do it"}, "do it"},
		{"claude", "", nil, []string{"--model"}, "do it"},
		{"cursor", "m1", []string{"-p\n--force\n--output-format\nstream-json\n--resume\nchat-123\n--model\nm1\n", "instructions are in the file"}, []string{"do it\n"}, "do it"},
		{"codex", "m1", []string{"exec\n--json\n--full-auto\n--model\nm1\ndo it\n"}, nil, ""},
		{"gemini", "", []string{"--output-format\nstream-json\n--yolo\n-p\ndo it\n"}, []string{"--model"}, ""},
		{"aider", "m1", []string{"--message-file\n", ".prompt.md\n--yes-always\n--no-check-update\n--model\nm1\n"}, []string{"do it"}, ""},
		{"opencode", "", []string{"run\n--format\njson\ndo it\n"}, []string{"--model"}, ""},
		{"copilot", "", []string{"--allow-all-tools\n-p\ndo it\n"}, nil, ""},
		{"amp", "", []string{"-x\n--stream-json\n--dangerously-allow-all\n"}, []string{"do it"}, "do it"},
	}
	for _, c := range cases {
		t.Run(c.runner+"/"+c.model, func(t *testing.T) {
			argv, stdin := argvOf(t, c.runner, Spec{Runner: c.runner}, "do it", Options{Model: c.model, PermissionMode: "acceptEdits", MaxTurns: 3})
			for _, w := range c.wantArgv {
				if !strings.Contains(argv, w) {
					t.Errorf("argv lacks %q:\n%s", w, argv)
				}
			}
			for _, n := range c.noArgv {
				if strings.Contains(argv, n) {
					t.Errorf("argv must not contain %q:\n%s", n, argv)
				}
			}
			if stdin != c.wantStdin {
				t.Errorf("stdin = %q, want %q", stdin, c.wantStdin)
			}
		})
	}
}

func TestLongPromptsBecomeAPointerInArgsMode(t *testing.T) {
	long := strings.Repeat("ticket comment line\n", 2000)
	argv, stdin := argvOf(t, "codex", Spec{Runner: "codex"}, long, Options{})
	if strings.Contains(argv, "ticket comment line") || !strings.Contains(argv, "instructions are in the file") || stdin != "" {
		t.Errorf("long prompt in args mode: argv=%q stdin=%d bytes", argv[:min(len(argv), 200)], len(stdin))
	}
}

func TestSessionIDFromJSONLine(t *testing.T) {
	r, err := Resolve(Spec{Runner: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	dir := setupFake(t, "codex")
	// The fake prints a Claude-style result line; make codex's thread event appear too.
	os.WriteFile(filepath.Join(dir, "bin", "codex"), []byte(fakeCLI+`echo '{"type":"thread.started","thread_id":"thr_42"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"done here"}}'
`), 0o755)
	var progress strings.Builder
	res, err := r.Run(context.Background(), Options{Workdir: dir, PromptFile: promptFile(t, dir, "x"), Progress: &progress})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "thr_42" {
		t.Errorf("session id = %q, want thr_42 from the JSON line", res.SessionID)
	}
	if !strings.Contains(progress.String(), "agent: done here") {
		t.Errorf("codex item text must reach the progress output: %q", progress.String())
	}
}

func TestCustomRunner(t *testing.T) {
	dir := setupFake(t, "my-agent")
	script := filepath.Join(dir, "bin", "my-agent")
	r, err := Resolve(Spec{Runner: Custom, Command: script, Args: []string{"--task", "{prompt_file}", "--in", "{workdir}"}, PromptVia: PromptArgs, SessionID: "json:session_id", Resume: "my-agent --continue {session}"})
	if err != nil {
		t.Fatal(err)
	}
	pf := promptFile(t, dir, "custom task")
	res, err := r.Run(context.Background(), Options{Workdir: dir, PromptFile: pf})
	if err != nil {
		t.Fatal(err)
	}
	if argv := read(t, filepath.Join(dir, "argv")); argv != "--task\n"+pf+"\n--in\n"+dir+"\n" {
		t.Errorf("argv = %q", argv)
	}
	if got := read(t, filepath.Join(dir, "stdin")); got != "" {
		t.Errorf("args mode must not pipe stdin, got %q", got)
	}
	if res.SessionID != "abc" || r.JoinCommand("/w", res.SessionID) != "cd /w && my-agent --continue abc" {
		t.Errorf("session %q join %q", res.SessionID, r.JoinCommand("/w", res.SessionID))
	}
}
