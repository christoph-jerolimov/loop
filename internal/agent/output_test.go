package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractText(t *testing.T) {
	type msg = struct {
		Content json.RawMessage `json:"content"`
	}
	cases := []struct {
		name string
		in   *msg
		want []string
	}{
		{"nil", nil, nil},
		{"empty", &msg{}, nil},
		{"string", &msg{Content: json.RawMessage(`"plain text"`)}, []string{"plain text"}},
		{"blocks", &msg{Content: json.RawMessage(`[{"type":"text","text":"  hello \n"},{"type":"tool_use","name":"Bash"},{"type":"text","text":"   "},{"type":"image"}]`)}, []string{"hello", "[tool: Bash]"}},
		{"garbage", &msg{Content: json.RawMessage(`{"not":"a list"}`)}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractText(c.in); strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("got %v, want %v", got, c.want)
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
