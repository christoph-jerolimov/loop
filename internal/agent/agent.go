// Package agent runs coding-agent CLIs headlessly in a workdir. One
// generic Runner drives every harness; what differs between them is a
// small Profile: the command line, how the prompt travels, how the session
// id is learned and how a human resumes the session.
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
	"sort"
	"strings"
	"time"

	"github.com/christoph-jerolimov/loop/internal/term"
)

// Options configures one headless session.
type Options struct {
	Workdir string
	// PromptFile is the file the prompt was written to before the session
	// starts. The runner loads the prompt from there: the file is the
	// single source of what the agent was told, and the agent can read it
	// again.
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
	// Log receives the raw agent output (stream-json lines).
	Log io.Writer
	// Progress receives short human-readable lines: what the agent said and
	// which tools it called, with the command or file of each call.
	Progress io.Writer
	// Paint colours the progress lines; the zero value writes plain text.
	Paint term.Painter
	// Sandbox, when set, runs the harness in a container that sees the
	// workdir, RunDir and Mounts at their host paths.
	Sandbox *Sandbox
	RunDir  string
	Mounts  []Mount
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

// Prompt delivery modes.
const (
	// PromptStdin pipes the prompt file's content on standard input.
	PromptStdin = "stdin"
	// PromptArgs substitutes {prompt} in the arguments (or {prompt_file}
	// for CLIs that read a file). Long prompts become a one-line pointer
	// at the prompt file so the command line stays within limits.
	PromptArgs = "args"
	// PromptBoth pipes the content and substitutes {prompt} with the
	// pointer, for CLIs that read either.
	PromptBoth = "both"
)

// Session id modes: how the runner learns the id a human can resume.
const (
	// SessionUUID generates the id up front and substitutes {session}.
	SessionUUID = "uuid"
	// SessionNone means the harness has no resumable session.
	SessionNone = "none"
	// sessionRun ("run:<args>") runs the command with these arguments
	// first and takes its output as the id, then substitutes {session}.
	sessionRun = "run:"
	// sessionJSON ("json:<key>") reads the id from that key of any JSON
	// line the harness prints.
	sessionJSON = "json:"
)

// argMax keeps a prompt passed as an argument well below the per-argument
// limit of the operating system (128 KiB on Linux).
const argMax = 16 << 10

// Runner drives one harness. Built-in profiles live in profiles.go; a
// custom one comes entirely from loop.yaml.
type Runner struct {
	Name string
	// Command is the executable.
	Command string
	// Args is the argument template. Placeholders: {prompt}, {prompt_file},
	// {model}, {max_turns}, {permission_mode}, {session}, {workdir}. An
	// argument that is only a placeholder with no value is dropped together
	// with the flag before it.
	Args []string
	// PromptVia is one of stdin, args or both.
	PromptVia string
	// SessionID is uuid, none, run:<args> or json:<key>.
	SessionID string
	// Resume is the command a human runs in the workdir to continue the
	// session; {session} and {workdir} are substituted.
	Resume string
	// SettingsFile, when set, is where loop writes the permission rules
	// (Claude Code's settings.local.json), relative to the workdir.
	SettingsFile string
	// ExtraArgs are appended verbatim.
	ExtraArgs []string
}

// Spec is what loop.yaml (and the --runner override) say about the
// harness. Empty fields keep the profile's values; for a custom runner
// Command and Args are required.
type Spec struct {
	Runner    string
	Command   string
	Args      []string
	PromptVia string
	SessionID string
	Resume    string
	ExtraArgs []string
}

// Custom is the runner name for a harness defined entirely in loop.yaml.
const Custom = "custom"

// Names lists the built-in profiles in documentation order.
func Names() []string {
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return profileOrder[names[i]] < profileOrder[names[j]] })
	return names
}

// Resolve builds the runner for a spec: a built-in profile with optional
// overrides, or a custom one.
func Resolve(s Spec) (*Runner, error) {
	var r Runner
	if s.Runner == Custom {
		r = Runner{Name: Custom, PromptVia: PromptStdin, SessionID: SessionNone}
		if s.Command == "" || len(s.Args) == 0 {
			return nil, errors.New("agent.runner custom needs agent.command and agent.args")
		}
	} else {
		p, ok := profiles[s.Runner]
		if !ok {
			return nil, fmt.Errorf("agent.runner must be one of %s or %s, got %q", strings.Join(Names(), ", "), Custom, s.Runner)
		}
		r = p
		r.Args = append([]string(nil), p.Args...)
	}
	if s.Command != "" {
		r.Command = s.Command
	}
	if len(s.Args) > 0 {
		r.Args = append([]string(nil), s.Args...)
	}
	if s.PromptVia != "" {
		r.PromptVia = s.PromptVia
	}
	if s.SessionID != "" {
		r.SessionID = s.SessionID
	}
	if s.Resume != "" {
		r.Resume = s.Resume
	}
	r.ExtraArgs = append([]string(nil), s.ExtraArgs...)
	switch r.PromptVia {
	case PromptStdin, PromptArgs, PromptBoth:
	default:
		return nil, fmt.Errorf("agent.prompt_via must be %s, %s or %s, got %q", PromptStdin, PromptArgs, PromptBoth, r.PromptVia)
	}
	switch {
	case r.SessionID == SessionUUID, r.SessionID == SessionNone:
	case strings.HasPrefix(r.SessionID, sessionRun) && len(r.SessionID) > len(sessionRun):
	case strings.HasPrefix(r.SessionID, sessionJSON) && len(r.SessionID) > len(sessionJSON):
	default:
		return nil, fmt.Errorf("agent.session_id must be %s, %s, run:<args> or json:<key>, got %q", SessionUUID, SessionNone, r.SessionID)
	}
	hasPrompt := false
	for _, a := range r.Args {
		hasPrompt = hasPrompt || strings.Contains(a, "{prompt}") || strings.Contains(a, "{prompt_file}")
	}
	if r.PromptVia != PromptStdin && !hasPrompt {
		return nil, fmt.Errorf("agent.args must contain {prompt} or {prompt_file} when prompt_via is %s", r.PromptVia)
	}
	return &r, nil
}

// Run executes the session and blocks until it ends.
func (r *Runner) Run(ctx context.Context, o Options) (*Result, error) {
	res := &Result{}
	prompt, err := loadPrompt(o)
	if err != nil {
		return res, err
	}
	vars := map[string]string{
		"prompt_file": o.PromptFile, "model": o.Model, "permission_mode": o.PermissionMode, "workdir": o.Workdir,
	}
	if o.MaxTurns > 0 {
		vars["max_turns"] = fmt.Sprint(o.MaxTurns)
	}
	jsonKey := ""
	switch {
	case r.SessionID == SessionUUID:
		res.SessionID = NewUUID()
	case strings.HasPrefix(r.SessionID, sessionRun):
		res.SessionID = r.sessionFromCommand(ctx, o, expand(strings.Fields(r.SessionID[len(sessionRun):]), vars))
	case strings.HasPrefix(r.SessionID, sessionJSON):
		jsonKey = r.SessionID[len(sessionJSON):]
	}
	vars["session"] = res.SessionID

	pointer := fmt.Sprintf("Your complete instructions are in the file %s. Read it fully and follow it exactly.", o.PromptFile)
	stdin := ""
	switch r.PromptVia {
	case PromptStdin:
		stdin = prompt
		vars["prompt"] = pointer
	case PromptBoth:
		stdin = prompt
		vars["prompt"] = pointer + " The same instructions are provided on standard input."
	case PromptArgs:
		vars["prompt"] = prompt
		if len(prompt) > argMax {
			vars["prompt"] = pointer
		}
	}
	args := append(expand(r.Args, vars), r.ExtraArgs...)
	err = runProcess(ctx, o, r.Command, args, stdin, jsonKey, res)
	return res, err
}

// sessionFromCommand runs a preparatory command (Cursor's create-chat) and
// returns its trimmed output; an empty id just means no resume flag.
func (r *Runner) sessionFromCommand(ctx context.Context, o Options, args []string) string {
	cmd := Command(ctx, o, r.Command, args, false)
	out, err := cmd.Output()
	cmd.Finish()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Cmd is a process in the workdir with the session environment: the
// command itself on the host, or the container runtime running it in the
// sandbox image. Call Finish once the process has ended.
type Cmd struct {
	*exec.Cmd
	ctx       context.Context
	sandbox   *Sandbox
	container string
}

// Command prepares name with args for the session or step described by o.
// stdin says whether the process reads standard input, which a container
// needs to know to attach it.
func Command(ctx context.Context, o Options, name string, args []string, stdin bool) *Cmd {
	env := SessionEnv(o.EnvPassthrough, o.Env)
	c := &Cmd{ctx: ctx, sandbox: o.Sandbox}
	if o.Sandbox != nil {
		c.container, args = o.Sandbox.runArgs(o, name, args, env, stdin)
		name, env = o.Sandbox.Runtime, o.Sandbox.clientEnv(env)
	}
	c.Cmd = exec.CommandContext(ctx, name, args...)
	c.Dir = o.Workdir
	c.Env = env
	return c
}

// Finish removes the sandbox container when the context ended the process:
// killing the runtime client, which is what a cancelled context does,
// leaves the container running.
func (c *Cmd) Finish() {
	if c.container != "" && c.ctx.Err() != nil {
		c.sandbox.kill(c.container)
	}
}

// JoinCommand is what a human types to continue the session.
func (r *Runner) JoinCommand(workdir, sessionID string) string {
	cmd := "cd " + shellQuote(workdir)
	if r.Resume == "" {
		return cmd
	}
	resume := expand(strings.Fields(r.Resume), map[string]string{"session": sessionID, "workdir": workdir})
	if len(resume) == 0 {
		return cmd
	}
	return cmd + " && " + strings.Join(resume, " ")
}

// expand substitutes {name} placeholders. An argument that consists of a
// single placeholder without a value disappears, and so does a flag
// directly before it, so "--model {model}" vanishes when no model is set.
func expand(args []string, vars map[string]string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if name, ok := strings.CutPrefix(a, "{"); ok && strings.HasSuffix(name, "}") && !strings.Contains(name, "{") {
			name = strings.TrimSuffix(name, "}")
			if v, known := vars[name]; known && v == "" || !known && isPlaceholder(name) {
				if n := len(out); n > 0 && strings.HasPrefix(out[n-1], "-") && !strings.Contains(out[n-1], "=") {
					out = out[:n-1]
				}
				continue
			}
		}
		for name, v := range vars {
			a = strings.ReplaceAll(a, "{"+name+"}", v)
		}
		out = append(out, a)
	}
	return out
}

func isPlaceholder(name string) bool {
	switch name {
	case "prompt", "prompt_file", "model", "max_turns", "permission_mode", "session", "workdir":
		return true
	}
	return false
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

// streamEvent is the union of the JSON line shapes the harnesses print:
// Claude Code and Cursor emit "assistant" messages and a final "result";
// Codex emits items: messages with a text, command executions with the
// command, file changes with the changed paths.
type streamEvent struct {
	Type     string  `json:"type"`
	Subtype  string  `json:"subtype"`
	IsError  bool    `json:"is_error"`
	Result   string  `json:"result"`
	NumTurns int     `json:"num_turns"`
	CostUSD  float64 `json:"total_cost_usd"`
	Message  *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Item *struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Command string `json:"command"`
		Changes []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
	} `json:"item"`
}

// runProcess starts the command, streams its output to the log and
// progress writers, and picks the session id and result out of JSON lines.
func runProcess(ctx context.Context, o Options, name string, args []string, stdin, jsonKey string, res *Result) error {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	cmd := Command(ctx, o, name, args, stdin != "")
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
		if jsonKey != "" && res.SessionID == "" {
			var raw map[string]json.RawMessage
			if json.Unmarshal([]byte(line), &raw) == nil {
				var id string
				if json.Unmarshal(raw[jsonKey], &id) == nil && id != "" {
					res.SessionID = id
				}
			}
		}
		switch ev.Type {
		case "assistant":
			for _, b := range extractBlocks(ev.Message) {
				if b.Tool != "" {
					progressTool(o, b.Tool, b.Detail)
					continue
				}
				lastText.Reset()
				lastText.WriteString(b.Text)
				progress(o, "agent", b.Text)
			}
		case "result":
			res.Output = ev.Result
			res.IsError = ev.IsError || (ev.Subtype != "" && ev.Subtype != "success")
			res.Turns = ev.NumTurns
			res.CostUSD = ev.CostUSD
		default:
			if ev.Item == nil {
				continue
			}
			switch {
			case strings.Contains(ev.Item.Type, "message") && strings.TrimSpace(ev.Item.Text) != "":
				lastText.Reset()
				lastText.WriteString(strings.TrimSpace(ev.Item.Text))
				progress(o, "agent", ev.Item.Text)
			case ev.Item.Type == "command_execution" && ev.Type == "item.started":
				// Codex announces a command when it starts and again when it
				// ends; one line per command is enough.
				progressTool(o, "shell", ev.Item.Command)
			case ev.Item.Type == "file_change" && ev.Type == "item.completed":
				for _, c := range ev.Item.Changes {
					progressTool(o, c.Kind, c.Path)
				}
			}
		}
	}
	werr := cmd.Wait()
	cmd.Finish()
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

// block is one part of an assistant message: text the agent wrote, or a
// tool call with the argument that identifies it.
type block struct {
	Text string
	// Tool is the tool name; empty for text.
	Tool string
	// Detail is the command, file, pattern or description of the call.
	Detail string
}

// detailKeys are the tool inputs that identify a call best, in order of
// preference: the shell command, the file, a search or fetch target, then
// a free-text description. Claude Code's Bash, Read, Write, Edit,
// NotebookEdit, Glob, Grep, WebFetch and Agent tools all use one of them.
var detailKeys = []string{"command", "file_path", "notebook_path", "pattern", "path", "query", "url", "description", "skill", "prompt"}

func extractBlocks(m *struct {
	Content json.RawMessage `json:"content"`
}) []block {
	if m == nil || len(m.Content) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return []block{{Text: s}}
	}
	var blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []block
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, block{Text: strings.TrimSpace(b.Text)})
			}
		case "tool_use":
			out = append(out, block{Tool: b.Name, Detail: toolDetail(b.Input)})
		}
	}
	return out
}

// toolDetail picks the input value that says what a tool call does.
func toolDetail(input json.RawMessage) string {
	var in map[string]any
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return ""
	}
	for _, k := range detailKeys {
		if s, ok := in[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// progress writes one line of what the agent said.
func progress(o Options, prefix, text string) {
	if o.Progress == nil {
		return
	}
	fmt.Fprintf(o.Progress, "  %s %s\n", o.Paint.Paint(prefix+":", term.Cyan), oneLine(text))
}

// progressTool writes one line for a tool call: the tool name and its
// command or file, the latter relative to the workdir.
func progressTool(o Options, name, detail string) {
	if o.Progress == nil {
		return
	}
	line := o.Paint.Paint(name, term.Bold)
	if detail = oneLine(relative(detail, o.Workdir)); detail != "" {
		line += " " + detail
	}
	fmt.Fprintf(o.Progress, "  %s %s\n", o.Paint.Paint("tool:", term.Magenta), line)
}

// oneLine collapses text to a single line of at most 200 characters.
func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}

// relative strips the workdir from a path inside it.
func relative(path, workdir string) string {
	if workdir == "" {
		return path
	}
	if rel, ok := strings.CutPrefix(path, strings.TrimRight(workdir, "/")+"/"); ok && rel != "" {
		return rel
	}
	return path
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

func shellQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t'\"$`\\!*?[]{}()<>|&;") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
