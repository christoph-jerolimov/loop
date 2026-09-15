package agent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The environment an agent session inherits is an allowlist in groups:
// what the shell, git, the language toolchains and the agent CLIs need to
// function. Everything else, in particular loop's own GitHub, GitLab and
// Jira credentials, is withheld unless listed in agent.env_passthrough.
var (
	// processEnv are shell, locale and proxy basics.
	processEnv = []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "LANG", "LANGUAGE", "LC_*", "TZ",
		"TMPDIR", "TMP", "TEMP", "XDG_*", "SSH_AUTH_SOCK", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	}
	// gitEnv is the git identity and its helpers, without credential overrides.
	gitEnv = []string{"GIT_AUTHOR_*", "GIT_COMMITTER_*", "GIT_EDITOR", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_CONFIG_GLOBAL"}
	// toolchainEnv configures Go, Node, Rust, Java, Python and Docker clients.
	toolchainEnv = []string{
		"GOPATH", "GOROOT", "GOFLAGS", "GOCACHE", "GOMODCACHE", "GOTOOLCHAIN", "GOPROXY", "GOPRIVATE", "GONOSUMDB", "GONOSUMCHECK",
		"NODE_*", "NPM_CONFIG_*", "NVM_*", "PNPM_HOME", "YARN_*", "BUN_INSTALL",
		"CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME", "MAVEN_*", "GRADLE_*", "PYTHONPATH", "VIRTUAL_ENV", "PIP_*", "UV_*",
		"DOCKER_HOST", "COMPOSE_*",
	}
	// agentEnv is the agent CLIs' own configuration and credentials.
	agentEnv = []string{
		"ANTHROPIC_*", "CLAUDE_*", "CLAUDE_CODE_*", "CURSOR_*", "OPENAI_*", "CODEX_*", "GEMINI_*", "GOOGLE_*",
		"AIDER_*", "OPENCODE_*", "COPILOT_*", "AMP_*",
	}
	windowsEnv = []string{
		"SYSTEMROOT", "SystemRoot", "WINDIR", "APPDATA", "LOCALAPPDATA", "USERPROFILE", "PROGRAMFILES", "PROGRAMFILES(X86)",
		"PROGRAMDATA", "COMSPEC", "PATHEXT", "HOMEDRIVE", "HOMEPATH", "SYSTEMDRIVE",
	}
	// baseEnv lists everything a session on the host inherits.
	baseEnv = concat(processEnv, gitEnv, toolchainEnv, agentEnv, windowsEnv)
)

// hostPathEnv are allowlisted variables whose values are paths or sockets
// of the host. They mean nothing inside a sandbox container, which has its
// own PATH, home and toolchain, so a sandboxed session does not get them.
var hostPathEnv = concat([]string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TMP", "TEMP", "XDG_*", "SSH_AUTH_SOCK", "SSL_CERT_FILE", "SSL_CERT_DIR",
	"GIT_EDITOR", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_CONFIG_GLOBAL",
	"GOPATH", "GOROOT", "GOCACHE", "GOMODCACHE", "NODE_PATH", "NVM_*", "PNPM_HOME", "BUN_INSTALL",
	"CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME", "PYTHONPATH", "VIRTUAL_ENV", "DOCKER_HOST",
	"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_CLI_HOME",
}, windowsEnv)

func concat(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// SessionEnv builds the environment for an agent session from the current
// process environment: the base allowlist, every name matching one of the
// passthrough patterns (shell globs such as "DATABASE_URL" or "MY_APP_*"),
// and the explicit extra values, which win over inherited ones.
func SessionEnv(passthrough []string, extra map[string]string) []string {
	patterns := append(append([]string{}, baseEnv...), passthrough...)
	out := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if matchesAny(k, patterns) {
			out[k] = v
		}
	}
	for k, v := range extra {
		out[k] = v
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+out[k])
	}
	return env
}

func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if p == name {
			return true
		}
		if strings.ContainsAny(p, "*?[") {
			if ok, _ := filepath.Match(p, name); ok {
				return true
			}
		}
	}
	return false
}
