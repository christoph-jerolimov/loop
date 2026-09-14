package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestRepoForkHelpers(t *testing.T) {
	plain := Repo{GitHub: "o/r"}
	if plain.PushRepo() != "o/r" || plain.ForkOwner() != "" || plain.Host() != HostGitHub || plain.Project() != "o/r" {
		t.Errorf("without fork: push=%s owner=%q host=%s project=%s", plain.PushRepo(), plain.ForkOwner(), plain.Host(), plain.Project())
	}
	forked := Repo{GitHub: "o/r", Fork: "me/r"}
	if forked.PushRepo() != "me/r" || forked.ForkOwner() != "me" {
		t.Errorf("with fork: push=%s owner=%q", forked.PushRepo(), forked.ForkOwner())
	}
	gl := Repo{GitLab: "group/sub/r", Fork: "me/r"}
	if gl.Host() != HostGitLab || gl.Project() != "group/sub/r" || gl.PushRepo() != "me/r" {
		t.Errorf("gitlab: host=%s project=%s push=%s", gl.Host(), gl.Project(), gl.PushRepo())
	}
	cases := map[string]string{
		"git@github.com:o/r.git":               "git@github.com:me/r.git",
		"https://github.com/o/r.git":           "https://github.com/me/r.git",
		"https://github.com/o/r":               "https://github.com/me/r",
		"git@gitlab.com:group/sub/r.git":       "git@gitlab.com:me/r.git",
		"https://gitlab.example.com/g/r.git":   "https://gitlab.example.com/me/r.git",
		"ssh://git@gitlab.example.com/g/r.git": "ssh://git@gitlab.example.com/me/r.git",
		"/srv/git/r.git":                       "",
	}
	for url, want := range cases {
		if got := deriveForkURL(url, "me/r"); got != want {
			t.Errorf("deriveForkURL(%s) = %q, want %q", url, got, want)
		}
	}
	hosts := map[string]string{
		"git@github.com:o/r.git":               "github.com",
		"https://gitlab.example.com/g/r.git":   "gitlab.example.com",
		"ssh://git@gitlab.example.com/g/r.git": "gitlab.example.com",
		"/srv/git/r.git":                       "",
	}
	for url, want := range hosts {
		if got := hostOf(url); got != want {
			t.Errorf("hostOf(%s) = %q, want %q", url, got, want)
		}
	}
}

func TestStepKind(t *testing.T) {
	cases := map[string]Step{"run": {Run: "make test"}, "script": {Script: "hooks/x.sh"}, "agent": {Agent: "prompts/x.md"}, "": {}}
	for want, st := range cases {
		if got := st.Kind(); got != want {
			t.Errorf("Kind(%+v) = %q, want %q", st, got, want)
		}
	}
}

func TestDurationYAMLRoundTrip(t *testing.T) {
	type doc struct {
		Timeout Duration `yaml:"timeout"`
	}
	var d doc
	if err := yaml.Unmarshal([]byte("timeout: 90s\n"), &d); err != nil || d.Timeout.D() != 90*time.Second {
		t.Fatalf("unmarshal: %v %v", d.Timeout.D(), err)
	}
	out, err := yaml.Marshal(d)
	if err != nil || strings.TrimSpace(string(out)) != "timeout: 1m30s" {
		t.Errorf("marshal = %q, %v", out, err)
	}
	if err := yaml.Unmarshal([]byte("timeout: soon\n"), &d); err == nil || !strings.Contains(err.Error(), "invalid duration") {
		t.Errorf("invalid duration: %v", err)
	}
}
