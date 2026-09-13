package agent

// profiles are the built-in harnesses. Every field can be overridden in
// loop.yaml (agent.command, args, prompt_via, session_id, resume), which
// is how a flag that a CLI renamed gets fixed without a new loop release.
//
// Claude Code and Cursor are exercised by loop's own tests against fake
// CLIs that speak their protocols. The other profiles follow the headless
// modes those CLIs document; check `loop doctor` and the first transcript
// after switching to one.
var profiles = map[string]Runner{
	"claude": {
		Name:    "claude",
		Command: "claude",
		Args: []string{"-p", "--output-format", "stream-json", "--verbose", "--session-id", "{session}",
			"--permission-mode", "{permission_mode}", "--model", "{model}", "--max-turns", "{max_turns}"},
		PromptVia:    PromptStdin,
		SessionID:    SessionUUID,
		Resume:       "claude --resume {session}",
		SettingsFile: ".claude/settings.local.json",
	},
	"cursor": {
		Name:      "cursor",
		Command:   "agent",
		Args:      []string{"-p", "--force", "--output-format", "stream-json", "--resume", "{session}", "--model", "{model}", "{prompt}"},
		PromptVia: PromptBoth,
		SessionID: "run:create-chat",
		Resume:    "agent --resume {session}",
	},
	"codex": {
		Name:      "codex",
		Command:   "codex",
		Args:      []string{"exec", "--json", "--full-auto", "--model", "{model}", "{prompt}"},
		PromptVia: PromptArgs,
		SessionID: "json:thread_id",
		Resume:    "codex resume {session}",
	},
	"gemini": {
		Name:      "gemini",
		Command:   "gemini",
		Args:      []string{"--output-format", "stream-json", "--yolo", "--model", "{model}", "-p", "{prompt}"},
		PromptVia: PromptArgs,
		SessionID: "json:session_id",
		Resume:    "gemini --resume {session}",
	},
	"aider": {
		Name:      "aider",
		Command:   "aider",
		Args:      []string{"--message-file", "{prompt_file}", "--yes-always", "--no-check-update", "--model", "{model}"},
		PromptVia: PromptArgs,
		SessionID: SessionNone,
		Resume:    "aider",
	},
	"opencode": {
		Name:      "opencode",
		Command:   "opencode",
		Args:      []string{"run", "--format", "json", "--model", "{model}", "{prompt}"},
		PromptVia: PromptArgs,
		SessionID: "json:sessionID",
		Resume:    "opencode --session {session}",
	},
	"copilot": {
		Name:      "copilot",
		Command:   "copilot",
		Args:      []string{"--allow-all-tools", "--model", "{model}", "-p", "{prompt}"},
		PromptVia: PromptArgs,
		SessionID: SessionNone,
		Resume:    "copilot --resume",
	},
	"amp": {
		Name:      "amp",
		Command:   "amp",
		Args:      []string{"-x", "--stream-json", "--dangerously-allow-all"},
		PromptVia: PromptStdin,
		SessionID: SessionNone,
		Resume:    "amp threads continue",
	},
}

// profileOrder is the order profiles are listed in messages and docs.
var profileOrder = map[string]int{"claude": 0, "cursor": 1, "codex": 2, "gemini": 3, "aider": 4, "opencode": 5, "copilot": 6, "amp": 7}
