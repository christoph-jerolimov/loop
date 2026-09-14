package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractBlocks(t *testing.T) {
	type msg = struct {
		Content json.RawMessage `json:"content"`
	}
	cases := []struct {
		name string
		in   *msg
		want string
	}{
		{"nil", nil, ""},
		{"empty", &msg{}, ""},
		{"string", &msg{Content: json.RawMessage(`"plain text"`)}, "text(plain text)"},
		{"blocks", &msg{Content: json.RawMessage(`[{"type":"text","text":"  hello \n"},{"type":"tool_use","name":"Bash","input":{"command":"go test ./...","description":"run tests"}},{"type":"text","text":"   "},{"type":"image"}]`)}, "text(hello)|tool(Bash: go test ./...)"},
		{"files", &msg{Content: json.RawMessage(`[{"type":"tool_use","name":"Read","input":{"file_path":"/w/a.go"}},{"type":"tool_use","name":"Edit","input":{"file_path":"/w/b.go","old_string":"x","new_string":"y"}},{"type":"tool_use","name":"Write","input":{"file_path":"/w/c.go","content":"..."}},{"type":"tool_use","name":"NotebookEdit","input":{"notebook_path":"/w/n.ipynb"}}]`)}, "tool(Read: /w/a.go)|tool(Edit: /w/b.go)|tool(Write: /w/c.go)|tool(NotebookEdit: /w/n.ipynb)"},
		{"search and fetch", &msg{Content: json.RawMessage(`[{"type":"tool_use","name":"Glob","input":{"pattern":"**/*.go","path":"/w"}},{"type":"tool_use","name":"Grep","input":{"pattern":"TODO"}},{"type":"tool_use","name":"WebFetch","input":{"url":"https://example.com"}},{"type":"tool_use","name":"Agent","input":{"description":"Find callers","prompt":"long..."}}]`)}, "tool(Glob: **/*.go)|tool(Grep: TODO)|tool(WebFetch: https://example.com)|tool(Agent: Find callers)"},
		{"no input", &msg{Content: json.RawMessage(`[{"type":"tool_use","name":"TodoWrite","input":{"todos":[]}},{"type":"tool_use","name":"Skill"}]`)}, "tool(TodoWrite: )|tool(Skill: )"},
		{"garbage", &msg{Content: json.RawMessage(`{"not":"a list"}`)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, b := range extractBlocks(c.in) {
				if b.Tool != "" {
					got = append(got, "tool("+b.Tool+": "+b.Detail+")")
				} else {
					got = append(got, "text("+b.Text+")")
				}
			}
			if strings.Join(got, "|") != c.want {
				t.Errorf("got %q, want %q", strings.Join(got, "|"), c.want)
			}
		})
	}
}

func TestProgress(t *testing.T) {
	progress(Options{}, "agent", "nothing to write to") // no Progress writer: no-op
	var buf bytes.Buffer
	progress(Options{Progress: &buf}, "agent", "  line one\nline two  ")
	if buf.String() != "  agent: line one line two\n" {
		t.Errorf("progress = %q", buf.String())
	}
	buf.Reset()
	progress(Options{Progress: &buf}, "tool", strings.Repeat("x", 250))
	if line := buf.String(); !strings.HasSuffix(line, "…\n") || len(line) > 220 {
		t.Errorf("long text must be truncated: %d bytes", len(line))
	}
	buf.Reset()
	progress(Options{Progress: &buf, Paint: true}, "agent", "hi")
	if want := "  \x1b[36magent:\x1b[0m hi\n"; buf.String() != want {
		t.Errorf("coloured progress = %q, want %q", buf.String(), want)
	}
}

func TestProgressTool(t *testing.T) {
	progressTool(Options{}, "Bash", "ls") // no Progress writer: no-op
	var buf bytes.Buffer
	o := Options{Progress: &buf, Workdir: "/work/auth"}
	progressTool(o, "Bash", "go test ./...\n")
	progressTool(o, "Read", "/work/auth/internal/a.go")
	progressTool(o, "Edit", "/work/authors/b.go")
	progressTool(o, "TodoWrite", "")
	want := "  tool: Bash go test ./...\n  tool: Read internal/a.go\n  tool: Edit /work/authors/b.go\n  tool: TodoWrite\n"
	if buf.String() != want {
		t.Errorf("progressTool =\n%q\nwant\n%q", buf.String(), want)
	}
	buf.Reset()
	progressTool(Options{Progress: &buf, Paint: true}, "Bash", "make")
	if want := "  \x1b[35mtool:\x1b[0m \x1b[1mBash\x1b[0m make\n"; buf.String() != want {
		t.Errorf("coloured tool line = %q, want %q", buf.String(), want)
	}
	buf.Reset()
	progressTool(Options{Progress: &buf}, "Bash", "cat <<EOF\n  a  \n\tb\nEOF")
	if want := "  tool: Bash cat <<EOF a b EOF\n"; buf.String() != want {
		t.Errorf("multi-line command must collapse to one line: %q", buf.String())
	}
}

func TestToolCallsReachTheProgressOutput(t *testing.T) {
	r, err := Resolve(Spec{Runner: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	dir := setupFake(t, "claude")
	os.WriteFile(filepath.Join(dir, "bin", "claude"), []byte(fakeCLI+`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Looking around."},{"type":"tool_use","name":"Bash","input":{"command":"git status"}},{"type":"tool_use","name":"Read","input":{"file_path":"'"$PWD"'/main.go"}}]}}'
`), 0o755)
	var progress strings.Builder
	if _, err := r.Run(context.Background(), Options{Workdir: dir, PromptFile: promptFile(t, dir, "x"), Progress: &progress}); err != nil {
		t.Fatal(err)
	}
	want := "  agent: Looking around.\n  tool: Bash git status\n  tool: Read main.go\n"
	if !strings.Contains(progress.String(), want) {
		t.Errorf("progress output:\n%s\nwant to contain:\n%s", progress.String(), want)
	}
}

func TestCodexToolItemsReachTheProgressOutput(t *testing.T) {
	r, err := Resolve(Spec{Runner: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	dir := setupFake(t, "codex")
	os.WriteFile(filepath.Join(dir, "bin", "codex"), []byte(fakeCLI+`echo '{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"bash -lc ls","status":"in_progress"}}'
echo '{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"bash -lc ls","status":"completed","exit_code":0}}'
echo '{"type":"item.completed","item":{"id":"i2","type":"file_change","status":"completed","changes":[{"path":"'"$PWD"'/a.go","kind":"update"},{"path":"b.go","kind":"add"}]}}'
`), 0o755)
	var progress strings.Builder
	if _, err := r.Run(context.Background(), Options{Workdir: dir, PromptFile: promptFile(t, dir, "x"), Progress: &progress}); err != nil {
		t.Fatal(err)
	}
	want := "  tool: shell bash -lc ls\n  tool: update a.go\n  tool: add b.go\n"
	if !strings.Contains(progress.String(), want) {
		t.Errorf("progress output:\n%s\nwant to contain:\n%s", progress.String(), want)
	}
	if strings.Count(progress.String(), "bash -lc ls") != 1 {
		t.Errorf("a command is announced once, when it starts:\n%s", progress.String())
	}
}

func TestJoinCommands(t *testing.T) {
	cases := []struct {
		runner string
		dir    string
		sid    string
		want   string
	}{
		{"claude", "/work/auth", "abc-123", "cd /work/auth && claude --resume abc-123"},
		{"claude", "/work/my project", "abc", "cd '/work/my project' && claude --resume abc"},
		{"cursor", "/work/auth", "chat-1", "cd /work/auth && agent --resume chat-1"},
		{"cursor", "/work/it's", "", `cd '/work/it'\''s' && agent`},
		{"codex", "/work/auth", "thr_9", "cd /work/auth && codex resume thr_9"},
		{"aider", "/work/auth", "", "cd /work/auth && aider"},
		{"copilot", "/work/auth", "", "cd /work/auth && copilot --resume"},
	}
	for _, c := range cases {
		if got := runner(t, c.runner).JoinCommand(c.dir, c.sid); got != c.want {
			t.Errorf("%s JoinCommand(%q, %q) = %q, want %q", c.runner, c.dir, c.sid, got, c.want)
		}
	}
	custom, err := Resolve(Spec{Runner: Custom, Command: "./agent.sh", Args: []string{"{prompt_file}"}, PromptVia: PromptArgs})
	if err != nil {
		t.Fatal(err)
	}
	if got := custom.JoinCommand("/work/auth", "x"); got != "cd /work/auth" {
		t.Errorf("custom runner without resume = %q", got)
	}
}
