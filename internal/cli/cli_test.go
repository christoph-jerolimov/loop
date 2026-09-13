package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// project is a throwaway loop project: a bare git remote with a main
// branch, a fake claude binary on PATH, a fake GitHub API and a loop.yaml
// with a markdown and a GitHub source.
type project struct {
	dir  string
	gh   *fakeGitHub
	yaml string
}

type fakeGitHub struct {
	push bool
}

func (g *fakeGitHub) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case r.URL.Path == "/user":
			write(map[string]any{"login": "me"})
		case r.URL.Path == "/repos/o/r":
			write(map[string]any{"full_name": "o/r", "default_branch": "main", "private": false, "permissions": map[string]any{"push": g.push}})
		case r.URL.Path == "/repos/o/r/issues":
			write([]any{map[string]any{
				"number": 7, "node_id": "I_7", "title": "Fix typo", "body": "In the README.", "state": "open",
				"html_url": "https://github.com/o/r/issues/7", "labels": []any{map[string]any{"name": "ready"}},
				"created_at": "2026-02-01T00:00:00Z", "user": map[string]any{"login": "ann"},
			}})
		default:
			t.Logf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			write(map[string]any{"message": "Not Found"})
		}
	})
}

func newProject(t *testing.T) *project {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	git(t, root, "init", "-q", "--bare", remote)
	seed := filepath.Join(root, "seed")
	git(t, root, "clone", "-q", remote, seed)
	git(t, seed, "config", "user.email", "t@t")
	git(t, seed, "config", "user.name", "t")
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("hi\n"), 0o644)
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-qm", "init")
	git(t, seed, "branch", "-M", "main")
	git(t, seed, "push", "-q", "origin", "main")

	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho 'claude 9.9.9 (fake)'\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	gh := &fakeGitHub{push: true}
	srv := httptest.NewServer(gh.handler(t))
	t.Cleanup(srv.Close)
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_TOKEN", "x")

	proj := filepath.Join(root, "proj")
	os.MkdirAll(filepath.Join(proj, "backlog"), 0o755)
	os.WriteFile(filepath.Join(proj, "backlog", "auth.md"), []byte("---\ntitle: Add auth\ncreated: 2026-01-02\n---\nBuild login.\n"), 0o644)
	os.WriteFile(filepath.Join(proj, "backlog", "search.md"), []byte("---\ntitle: Add search\ncreated: 2026-01-03\n---\nDepends on: auth.md\n\nIndex things.\n"), 0o644)
	p := &project{dir: proj, gh: gh}
	p.yaml = "name: demo\nrepo:\n  url: " + remote + "\n  github: o/r\nsources:\n  - name: backlog\n    type: markdown\n  - name: gh\n    type: github\n    labels: [ready]\nagent:\n  allow: [\"Bash(go test:*)\"]\n"
	p.writeYAML(t, p.yaml)
	return p
}

func (p *project) writeYAML(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p.dir, config.FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// execute runs the loop command line against the project and returns what
// it printed on stdout. Flags are package globals, so they are reset to
// their defaults before every call.
func execute(t *testing.T, p *project, args ...string) (string, error) {
	t.Helper()
	return executeCtx(t, context.Background(), p, args...)
}

func executeCtx(t *testing.T, ctx context.Context, p *project, args ...string) (string, error) {
	t.Helper()
	resetFlags(root)
	root.SetArgs(append([]string{"-C", p.dir}, args...))

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	runErr := root.ExecuteContext(ctx)
	os.Stdout = old
	w.Close()
	<-done
	return buf.String(), runErr
}

func resetFlags(cmd *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if f.Changed {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
	}
	cmd.Flags().VisitAll(reset)
	cmd.PersistentFlags().VisitAll(reset)
	for _, c := range cmd.Commands() {
		resetFlags(c)
	}
}

func TestListShowsPickUpOrderAndStatus(t *testing.T) {
	p := newProject(t)
	out, err := execute(t, p, "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "#") {
		t.Fatalf("want a header and three rows, got:\n%s", out)
	}
	// Source order from loop.yaml first (markdown before github), then oldest first.
	for i, want := range []string{"backlog:auth", "backlog:search", "gh:7"} {
		if !strings.Contains(lines[i+1], want) {
			t.Errorf("row %d = %q, want it to list %s", i+1, lines[i+1], want)
		}
	}
	if !strings.Contains(lines[1], "ready") || !strings.Contains(lines[2], "blocked by") || !strings.Contains(lines[2], "auth.md") {
		t.Errorf("statuses wrong:\n%s", out)
	}
}

func TestListFlags(t *testing.T) {
	p := newProject(t)
	out, err := execute(t, p, "list", "--ready", "--source", "backlog")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "search") || strings.Contains(out, "gh:7") || !strings.Contains(out, "backlog:auth") {
		t.Errorf("--ready --source backlog must leave only the ready markdown item:\n%s", out)
	}

	out, err = execute(t, p, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Order  int    `json:"order"`
		Status string `json:"status"`
		Item   struct {
			ID string `json:"id"`
		} `json:"item"`
		Readiness struct {
			Ready bool `json:"Ready"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(rows) != 3 || rows[0].Order != 1 || rows[0].Item.ID != "backlog:auth" || !rows[0].Readiness.Ready || rows[1].Readiness.Ready || !strings.HasPrefix(rows[1].Status, "blocked by") {
		t.Errorf("json rows = %+v", rows)
	}
}

func TestListReportsRunningItems(t *testing.T) {
	p := newProject(t)
	cfg := loadConfig(t, p)
	it := &item.Item{ID: "backlog:auth", NativeID: "auth", Source: "backlog", Title: "Add auth"}
	r, err := state.NewStore(cfg.StatePath()).Create(it, "claude")
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = state.PhaseSession
	if err := state.NewStore(cfg.StatePath()).Save(r); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, p, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "running (session)") {
		t.Errorf("active run must show in STATUS:\n%s", out)
	}
}

func TestShow(t *testing.T) {
	p := newProject(t)
	out, err := execute(t, p, "show", "auth.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"backlog:auth  Add auth", "status:     ready", "comments:   0", "Build login."} {
		if !strings.Contains(out, want) {
			t.Errorf("show output lacks %q:\n%s", want, out)
		}
	}
	out, err = execute(t, p, "show", "search.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "depends on: auth.md") || !strings.Contains(out, "status:     blocked by") {
		t.Errorf("dependency not shown:\n%s", out)
	}
	if _, err := execute(t, p, "show", "nope.md"); err == nil {
		t.Error("unknown item must fail")
	}
}

func TestShowPromptRendersSessionTemplate(t *testing.T) {
	p := newProject(t)
	out, err := execute(t, p, "show", "--prompt", "auth.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Add auth") || !strings.Contains(out, "Build login.") || !strings.Contains(out, "<summary.md>") {
		t.Errorf("rendered prompt lacks item or placeholders:\n%s", out)
	}
	if strings.Contains(out, "status:") {
		t.Error("--prompt must print only the rendered template")
	}
}

func TestLogs(t *testing.T) {
	p := newProject(t)
	cfg := loadConfig(t, p)
	store := state.NewStore(cfg.StatePath())
	it := &item.Item{ID: "backlog:auth", NativeID: "auth", Source: "backlog", Title: "Add auth"}
	r, err := store.Create(it, "claude")
	if err != nil {
		t.Fatal(err)
	}
	r.Log("first line")
	r.Log("second line")
	r.Log("third line")
	for i, name := range []string{"1-session", "2-review"} {
		log := filepath.Join(r.Dir(), name+".log")
		os.WriteFile(log, []byte("transcript "+name+"\n"), 0o644)
		os.WriteFile(strings.TrimSuffix(log, ".log")+".prompt.md", []byte("prompt "+name+"\n"), 0o644)
		r.Sessions = append(r.Sessions, state.Session{ID: name, Kind: strings.Split(name, "-")[1], LogFile: log})
		_ = i
	}
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want string
		not  string
	}{
		{"run log", []string{"logs", r.ID}, "first line", "transcript"},
		{"run log by prefix", []string{"logs", r.ID[:8]}, "third line", ""},
		{"run log by item", []string{"logs", "backlog:auth"}, "second line", ""},
		{"last lines", []string{"logs", "-n", "1", r.ID}, "third line", "first line"},
		{"latest session", []string{"logs", "--session", r.ID}, "transcript 2-review", "1-session"},
		{"numbered session", []string{"logs", "--session=1", r.ID}, "transcript 1-session", "review"},
		{"latest prompt", []string{"logs", "--prompt", r.ID}, "prompt 2-review", "transcript"},
		{"numbered prompt", []string{"logs", "--session=1", "--prompt", r.ID}, "prompt 1-session", "review"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := execute(t, p, c.args...)
			if err != nil {
				t.Fatalf("%v: %v", c.args, err)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("output lacks %q:\n%s", c.want, out)
			}
			if c.not != "" && strings.Contains(out, c.not) {
				t.Errorf("output must not contain %q:\n%s", c.not, out)
			}
		})
	}

	errCases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown run", []string{"logs", "nope"}, "no run matches"},
		{"session out of range", []string{"logs", "--session=3", r.ID}, "has 2 session(s), no session 3"},
	}
	for _, c := range errCases {
		t.Run(c.name, func(t *testing.T) {
			_, err := execute(t, p, c.args...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%v: err = %v, want %q", c.args, err, c.want)
			}
		})
	}
}

func TestLogsWithoutSessions(t *testing.T) {
	p := newProject(t)
	cfg := loadConfig(t, p)
	it := &item.Item{ID: "backlog:auth", NativeID: "auth", Source: "backlog"}
	r, err := state.NewStore(cfg.StatePath()).Create(it, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, p, "logs", "--session", r.ID); err == nil || !strings.Contains(err.Error(), "no agent sessions yet") {
		t.Errorf("err = %v", err)
	}
	if _, err := execute(t, p, "logs", "--prompt", r.ID); err == nil || !strings.Contains(err.Error(), "no agent sessions yet") {
		t.Errorf("err = %v", err)
	}
}

func TestDoctorPasses(t *testing.T) {
	p := newProject(t)
	out, err := execute(t, p, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, want := range []string{
		"is valid (project \"demo\")",
		"claude runner: claude 9.9.9 (fake)",
		"agent.allow has 1 project rule(s)",
		"has branch main",
		"github: me can push to o/r",
		"source backlog (markdown): 2 open item(s)",
		"source gh (github): 1 open item(s)",
		"ticket comments loaded from the repository owner and collaborators with write access",
		"all checks passed (0 warning(s))",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FAIL") || strings.Contains(out, "warn ") {
		t.Errorf("unexpected failure or warning:\n%s", out)
	}
}

func TestDoctorReportsFailures(t *testing.T) {
	p := newProject(t)
	p.gh.push = false
	p.writeYAML(t, strings.Replace(p.yaml, "  github: o/r\n", "  github: o/r\n  base: nope\n", 1))
	out, err := execute(t, p, "doctor")
	if err == nil || !strings.Contains(err.Error(), "2 check(s) failed") {
		t.Errorf("err = %v, want two failed checks\n%s", err, out)
	}
	for _, want := range []string{
		"FAIL  base branch \"nope\" does not exist",
		"FAIL  github: me has no push access to o/r",
		"warn  github: default branch is main, loop.yaml uses base nope",
		"ticket comments loaded from the repository owner only",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
}

func TestDoctorWarnsAboutDefaultPermissions(t *testing.T) {
	p := newProject(t)
	p.writeYAML(t, strings.Replace(p.yaml, "agent:\n  allow: [\"Bash(go test:*)\"]\n", "", 1))
	out, err := execute(t, p, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warn  agent.allow has only the default git rules") || !strings.Contains(out, "all checks passed (1 warning(s))") {
		t.Errorf("expected the permissions warning:\n%s", out)
	}
}

func TestDoctorFailsOnMissingFiles(t *testing.T) {
	p := newProject(t)
	p.writeYAML(t, p.yaml+"prompts:\n  session: prompts/missing.md\nsteps:\n  verify:\n    - script: hooks/nope.sh\n")
	out, err := execute(t, p, "doctor")
	if err == nil {
		t.Fatalf("doctor must fail\n%s", out)
	}
	if !strings.Contains(out, "FAIL  prompts.session:") || !strings.Contains(out, "FAIL  steps.verify script hooks/nope.sh") {
		t.Errorf("missing files not reported:\n%s", out)
	}
}

func TestDoctorWithoutProject(t *testing.T) {
	p := &project{dir: t.TempDir()}
	out, err := execute(t, p, "doctor")
	if err == nil || !strings.Contains(out, "FAIL  no loop.yaml found") {
		t.Errorf("err = %v\n%s", err, out)
	}
}

func loadConfig(t *testing.T, p *project) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(p.dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
