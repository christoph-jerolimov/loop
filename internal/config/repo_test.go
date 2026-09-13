package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestRepoForkHelpers(t *testing.T) {
	plain := Repo{GitHub: "o/r"}
	if plain.PushRepo() != "o/r" || plain.HeadRef("loop/x") != "loop/x" || plain.ForkOwner() != "" {
		t.Errorf("without fork: push=%s head=%s owner=%q", plain.PushRepo(), plain.HeadRef("loop/x"), plain.ForkOwner())
	}
	forked := Repo{GitHub: "o/r", Fork: "me/r"}
	if forked.PushRepo() != "me/r" || forked.HeadRef("loop/x") != "me:loop/x" || forked.ForkOwner() != "me" {
		t.Errorf("with fork: push=%s head=%s owner=%q", forked.PushRepo(), forked.HeadRef("loop/x"), forked.ForkOwner())
	}
	cases := map[string]string{
		"git@github.com:o/r.git":     "git@github.com:me/r.git",
		"https://github.com/o/r.git": "https://github.com/me/r.git",
		"https://github.com/o/r":     "https://github.com/me/r",
		"/srv/git/r.git":             "",
	}
	for url, want := range cases {
		if got := deriveForkURL(url, "me/r"); got != want {
			t.Errorf("deriveForkURL(%s) = %q, want %q", url, got, want)
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
