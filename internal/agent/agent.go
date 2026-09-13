// Package agent runs coding-agent CLIs (claude, cursor) headlessly in a
// workdir and reports the session id so a human can join later.
package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Options configures one headless session.
type Options struct {
	Workdir string
	// PromptFile is the file the prompt was written to before the session
	// starts. Runners load the prompt from there: the file is the single
	// source of what the agent was told, and the agent can read it again.
	PromptFile     string
	Model          string
	PermissionMode string
	MaxTurns       int
	Timeout        time.Duration
	// Env holds explicit variables for the session (LOOP_* plus agent.env).
	Env map[string]string
	// EnvPassthrough lists additional variable names or globs to inherit
	// from loop's own environment on top of the built-in allowlist.
	EnvPassthrough []string
	ExtraArgs      []string
	// Command overrides the executable name.
	Command string
	// Log receives the raw agent output (stream-json lines).
	Log io.Writer
	// Progress receives short human-readable lines.
	Progress io.Writer
}

// Result summarises a finished session.
type Result struct {
	SessionID string
	Output    string
	CostUSD   float64
	Turns     int
	IsError   bool
	ExitCode  int
	Duration  time.Duration
}

// Runner is one agent CLI.
type Runner interface {
	Name() string
	// Run executes the session and blocks until it ends.
	Run(ctx context.Context, o Options) (*Result, error)
	// JoinCommand is what a human types to continue the session.
	JoinCommand(workdir, sessionID string) string
}

// New returns the runner for a name.
func New(name string) (Runner, error) {
	switch name {
	case "claude":
		return &Claude{}, nil
	case "cursor":
		return &Cursor{}, nil
	}
	return nil, fmt.Errorf("unknown agent runner %q", name)
}

// ClaudeSettings renders the permission rules as a Claude Code settings
// file. loop writes it to <workdir>/.claude/settings.local.json so both
// the headless session and a human who joins it later get the same rules.
func ClaudeSettings(mode string, allow, deny []string) ([]byte, error) {
	perms := map[string]any{"allow": nonNil(allow), "deny": nonNil(deny)}
	if mode != "" {
		perms["defaultMode"] = mode
	}
	return json.MarshalIndent(map[string]any{"permissions": perms}, "", "  ")
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// NewUUID returns a random v4 UUID.
func NewUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// streamEvent is the common shape of claude/cursor stream-json lines.
type streamEvent struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	SessionID string  `json:"session_id"`
	ChatID    string  `json:"chat_id"`
	IsError   bool    `json:"is_error"`
	Result    string  `json:"result"`
	NumTurns  int     `json:"num_turns"`
	CostUSD   float64 `json:"total_cost_usd"`
	Message   *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// runProcess starts the command, streams output, and parses stream-json.
func runProcess(ctx context.Context, o Options, name string, args []string, stdin string, res *Result) error {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = o.Workdir
	cmd.Env = SessionEnv(o.EnvPassthrough, o.Env)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &limitedBuffer{max: 64 << 10}
	cmd.Stderr = io.MultiWriter(stderr, nopIfNil(o.Log))
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var lastText strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if o.Log != nil {
			fmt.Fprintln(o.Log, line)
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.SessionID != "" && res.SessionID == "" {
			res.SessionID = ev.SessionID
		}
		if ev.ChatID != "" && res.SessionID == "" {
			res.SessionID = ev.ChatID
		}
		switch ev.Type {
		case "assistant":
			for _, t := range extractText(ev.Message) {
				lastText.Reset()
				lastText.WriteString(t)
				progress(o, "agent", t)
			}
		case "result":
			res.Output = ev.Result
			res.IsError = ev.IsError || (ev.Subtype != "" && ev.Subtype != "success")
			res.Turns = ev.NumTurns
			res.CostUSD = ev.CostUSD
		}
	}
	werr := cmd.Wait()
	res.Duration = time.Since(start)
	if res.Output == "" {
		res.Output = lastText.String()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("agent session timed out after %s", o.Timeout)
	}
	if werr != nil {
		if ee, ok := werr.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		}
		res.IsError = true
		return fmt.Errorf("%s exited: %w\n%s", name, werr, stderr.String())
	}
	return nil
}

func extractText(m *struct {
	Content json.RawMessage `json:"content"`
}) []string {
	if m == nil || len(m.Content) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return []string{s}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Name string `json:"name"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, strings.TrimSpace(b.Text))
			}
		case "tool_use":
			out = append(out, "[tool: "+b.Name+"]")
		}
	}
	return out
}

func progress(o Options, prefix, text string) {
	if o.Progress == nil {
		return
	}
	text = strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	fmt.Fprintf(o.Progress, "  %s: %s\n", prefix, text)
}

type limitedBuffer struct {
	buf strings.Builder
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.buf.Len() < l.max {
		l.buf.Write(p)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return l.buf.String() }

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func nopIfNil(w io.Writer) io.Writer {
	if w == nil {
		return nopWriter{}
	}
	return w
}

// Claude runs the Claude Code CLI.
type Claude struct{}

func (Claude) Name() string { return "claude" }

// Run executes `claude -p` with a pre-assigned session id so the session
// can be resumed by a human.
func (Claude) Run(ctx context.Context, o Options) (*Result, error) {
	res := &Result{SessionID: NewUUID()}
	name := o.Command
	if name == "" {
		name = "claude"
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--session-id", res.SessionID}
	if o.PermissionMode != "" {
		args = append(args, "--permission-mode", o.PermissionMode)
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprint(o.MaxTurns))
	}
	args = append(args, o.ExtraArgs...)
	prompt, err := loadPrompt(o)
	if err != nil {
		return res, err
	}
	err = runProcess(ctx, o, name, args, prompt, res)
	return res, err
}

// loadPrompt reads the prompt file the engine wrote for the session.
func loadPrompt(o Options) (string, error) {
	if o.PromptFile == "" {
		return "", errors.New("no prompt file for the session")
	}
	b, err := os.ReadFile(o.PromptFile)
	if err != nil {
		return "", fmt.Errorf("read prompt: %w", err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return "", fmt.Errorf("prompt file %s is empty", o.PromptFile)
	}
	return string(b), nil
}

// JoinCommand resumes the session interactively.
func (Claude) JoinCommand(workdir, sessionID string) string {
	return fmt.Sprintf("cd %s && claude --resume %s", shellQuote(workdir), sessionID)
}

// Cursor runs the Cursor agent CLI.
type Cursor struct{}

func (Cursor) Name() string { return "cursor" }

// Run creates a chat first so its id is known, then drives it headlessly.
//
// The prompt is never passed as a command-line argument: tickets with
// comment threads exceed the argument size limit. The prompt file is piped
// on stdin and a short positional instruction points the agent at the
// file, so it is available whichever of the two the CLI picks up.
func (Cursor) Run(ctx context.Context, o Options) (*Result, error) {
	name := o.Command
	if name == "" {
		name = "agent"
	}
	res := &Result{}
	create := exec.CommandContext(ctx, name, "create-chat")
	create.Dir = o.Workdir
	create.Env = SessionEnv(o.EnvPassthrough, o.Env)
	if out, err := create.Output(); err == nil {
		res.SessionID = strings.TrimSpace(string(out))
	}
	args := []string{"-p", "--force", "--output-format", "stream-json"}
	if res.SessionID != "" {
		args = append(args, "--resume", res.SessionID)
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	args = append(args, o.ExtraArgs...)
	prompt, err := loadPrompt(o)
	if err != nil {
		return res, err
	}
	args = append(args, fmt.Sprintf("Your complete instructions are in the file %s and are also provided on standard input. Read them fully and follow them exactly.", o.PromptFile))
	err = runProcess(ctx, o, name, args, prompt, res)
	return res, err
}

// JoinCommand resumes the chat interactively.
func (Cursor) JoinCommand(workdir, sessionID string) string {
	if sessionID == "" {
		return fmt.Sprintf("cd %s && agent", shellQuote(workdir))
	}
	return fmt.Sprintf("cd %s && agent --resume %s", shellQuote(workdir), sessionID)
}

func shellQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t'\"$`\\!*?[]{}()<>|&;") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
