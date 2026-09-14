package agent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// baseEnv lists the environment variables an agent session always inherits:
// what the shell, git, the language toolchains and the agent CLIs need to
// function. Everything else, in particular loop's own GitHub, GitLab and
// Jira credentials, is withheld unless listed in agent.env_passthrough.
var baseEnv = []string{
	// process basics
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "LANG", "LANGUAGE", "LC_*", "TZ",
	"TMPDIR", "TMP", "TEMP", "XDG_*", "SSH_AUTH_SOCK", "SSL_CERT_FILE", "SSL_CERT_DIR",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	// git identity and helpers, without credential overrides
	"GIT_AUTHOR_*", "GIT_COMMITTER_*", "GIT_EDITOR", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_CONFIG_GLOBAL",
	// toolchains
	"GOPATH", "GOROOT", "GOFLAGS", "GOCACHE", "GOMODCACHE", "GOTOOLCHAIN", "GOPROXY", "GOPRIVATE", "GONOSUMDB", "GONOSUMCHECK",
	"NODE_*", "NPM_CONFIG_*", "NVM_*", "PNPM_HOME", "YARN_*", "BUN_INSTALL",
	"CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME", "MAVEN_*", "GRADLE_*", "PYTHONPATH", "VIRTUAL_ENV", "PIP_*", "UV_*",
	"DOCKER_HOST", "COMPOSE_*",
	// the agent CLIs' own configuration and credentials
	"ANTHROPIC_*", "CLAUDE_*", "CLAUDE_CODE_*", "CURSOR_*", "OPENAI_*", "CODEX_*", "GEMINI_*", "GOOGLE_*",
	"AIDER_*", "OPENCODE_*", "COPILOT_*", "AMP_*",
	// windows
	"SYSTEMROOT", "SystemRoot", "WINDIR", "APPDATA", "LOCALAPPDATA", "USERPROFILE", "PROGRAMFILES", "PROGRAMFILES(X86)",
	"PROGRAMDATA", "COMSPEC", "PATHEXT", "HOMEDRIVE", "HOMEPATH", "SYSTEMDRIVE",
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
