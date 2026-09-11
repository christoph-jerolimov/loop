package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/source"
	_ "github.com/christoph-jerolimov/loop/internal/source/all"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// fakeGitHub is just enough of the REST API for one PR lifecycle.
type fakeGitHub struct {
	mu        sync.Mutex
	pr        map[string]any
	checks    []map[string]any
	reviews   []map[string]any
	rcomments []map[string]any
	icomments []map[string]any
	merged    bool
	mergeable bool
	readyCall int
	mergeCall int
	deleted   []string
}

func (f *fakeGitHub) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case p == "/user":
			write(map[string]any{"login": "loop-bot"})
		case p == "/graphql":
			var q struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&q)
			if strings.Contains(q.Query, "markPullRequestReadyForReview") {
				f.readyCall++
				f.pr["draft"] = false
			}
			write(map[string]any{"data": map[string]any{}})
		case p == "/repos/o/r/pulls" && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if !strings.Contains(in["body"].(string), "Backlog item") {
				t.Errorf("PR body missing template content: %q", in["body"])
			}
			f.pr = map[string]any{"number": 7, "node_id": "PR_7", "title": in["title"], "body": in["body"], "state": "open",
				"draft": in["draft"], "merged": false, "mergeable": true, "mergeable_state": "clean", "html_url": "https://gh/o/r/pull/7",
				"head": map[string]any{"ref": in["head"], "sha": "sha1"}, "base": map[string]any{"ref": "main"}}
			write(f.pr)
		case p == "/repos/o/r/pulls" && r.Method == http.MethodGet:
			write([]any{})
		case p == "/repos/o/r/pulls/7" && r.Method == http.MethodGet:
			f.pr["merged"] = f.merged
			f.pr["mergeable"] = f.mergeable
			if f.merged {
				f.pr["state"] = "closed"
			}
			write(f.pr)
		case p == "/repos/o/r/pulls/7/reviews":
			write(f.reviews)
		case p == "/repos/o/r/pulls/7/comments":
			write(f.rcomments)
		case p == "/repos/o/r/issues/7/comments" && r.Method == http.MethodGet:
			write(f.icomments)
		case p == "/repos/o/r/issues/7/comments" && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.icomments = append(f.icomments, map[string]any{"id": len(f.icomments) + 100, "body": in["body"], "user": map[string]any{"login": "loop-bot"}, "created_at": time.Now()})
			write(map[string]any{})
		case strings.HasPrefix(p, "/repos/o/r/commits/") && strings.HasSuffix(p, "/check-runs"):
			write(map[string]any{"total_count": len(f.checks), "check_runs": f.checks})
		case strings.HasPrefix(p, "/repos/o/r/commits/") && strings.HasSuffix(p, "/status"):
			write(map[string]any{"state": "success", "statuses": []any{}})
		case p == "/repos/o/r/pulls/7/merge":
			f.mergeCall++
			f.merged = true
			write(map[string]any{"merged": true})
		case strings.HasPrefix(p, "/repos/o/r/git/refs/heads/"):
			f.deleted = append(f.deleted, strings.TrimPrefix(p, "/repos/o/r/git/refs/heads/"))
			w.WriteHeader(204)
		case p == "/repos/o/r/pulls/7/requested_reviewers", strings.HasPrefix(p, "/repos/o/r/issues/7/labels"):
			write(map[string]any{})
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	})
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

const fakeAgent = `#!/bin/sh
prompt=$(cat)
sid=""
while [ $# -gt 0 ]; do case "$1" in --session-id) sid="$2"; shift;; esac; shift; done
git config user.email a@t; git config user.name a
echo "$prompt" >> feature.txt
git add -A && git commit -qm "agent work"
printf '## Summary\nAgent did things.\n' >> "$LOOP_SUMMARY_FILE"
echo "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"$sid\"}"
`

func TestFullLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	run(t, root, "git", "init", "-q", "--bare", remote)
	seed := filepath.Join(root, "seed")
	run(t, root, "git", "clone", "-q", remote, seed)
	run(t, seed, "git", "config", "user.email", "t@t")
	run(t, seed, "git", "config", "user.name", "t")
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("hi\n"), 0o644)
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-qm", "init")
	run(t, seed, "git", "branch", "-M", "main")
	run(t, seed, "git", "push", "-q", "origin", "main")

	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeAgent), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	gh := &fakeGitHub{mergeable: true}
	srv := httptest.NewServer(gh.handler(t))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_TOKEN", "x")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))

	proj := filepath.Join(root, "proj")
	os.MkdirAll(filepath.Join(proj, "backlog"), 0o755)
	os.WriteFile(filepath.Join(proj, "backlog", "auth.md"), []byte("---\ntitle: Add auth\n---\nBuild login.\n"), 0o644)
	cfg := &config.Config{
		Dir:      proj,
		Repo:     config.Repo{URL: remote, GitHub: "o/r"},
		Sources:  []config.SourceConfig{{Name: "backlog", Type: "markdown", Path: "backlog", Claim: true}},
		Steps:    config.Steps{Verify: []config.Step{{Name: "has-feature", Run: "test -f feature.txt"}}},
		Workflow: config.Workflow{Merge: config.MergeWhenGreenApprove, PollInterval: config.Duration(time.Millisecond), Gates: []string{}},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srcs, err := source.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	eng, err := New(cfg, srcs, &out)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	it, err := srcs.Resolve(ctx, "auth.md")
	if err != nil {
		t.Fatal(err)
	}
	r, err := eng.Start(ctx, it, false)
	if err != nil {
		t.Fatal(err)
	}
	drive := func(want state.Phase) {
		t.Helper()
		if err := eng.Drive(ctx, r); err != nil {
			t.Fatalf("drive: %v\n%s", err, out.String())
		}
		if r.Phase != want {
			t.Fatalf("phase = %s, want %s (error=%q)\n%s", r.Phase, want, r.Error, out.String())
		}
		time.Sleep(2 * time.Millisecond)
	}

	// checkout → session → verify → PR → monitor (no checks yet: green but not approved)
	drive(state.PhaseMonitor)
	if r.PR == nil || r.PR.Number != 7 {
		t.Fatalf("PR not recorded: %+v", r.PR)
	}
	if got := run(t, filepath.Join(proj, "backlog"), "cat", "auth.md"); !strings.Contains(got, "in-progress") {
		t.Errorf("item not claimed:\n%s", got)
	}
	// First poll: no checks at all counts as green → draft becomes ready, but no approval yet.
	drive(state.PhaseMonitor)
	if gh.readyCall != 1 {
		t.Errorf("draft PR should have been marked ready once CI was green, got %d calls", gh.readyCall)
	}
	pushes := run(t, root, "git", "--git-dir", remote, "rev-list", "--count", r.Branch)
	if pushes != "2" {
		t.Errorf("expected 2 commits on remote branch, got %s", pushes)
	}

	// Red CI → fix round → push.
	gh.mu.Lock()
	gh.checks = []map[string]any{{"id": 1, "name": "test", "status": "completed", "conclusion": "failure", "html_url": "u", "output": map[string]any{"summary": "boom"}}}
	gh.mu.Unlock()
	drive(state.PhaseMonitor)
	if r.FixRounds != 1 || r.LastCIFixSHA == "" {
		t.Errorf("expected one CI fix round, got %d (%q)", r.FixRounds, r.LastCIFixSHA)
	}
	if run(t, root, "git", "--git-dir", remote, "rev-list", "--count", r.Branch) != "3" {
		t.Error("CI fix was not pushed")
	}
	// Same head still red → nothing happens.
	drive(state.PhaseMonitor)
	if r.FixRounds != 1 {
		t.Errorf("CI fix retried on same head")
	}

	// Green again, reviewer requests changes with an inline comment.
	gh.mu.Lock()
	gh.checks[0]["conclusion"] = "success"
	gh.reviews = []map[string]any{{"id": 1, "user": map[string]any{"login": "ann"}, "state": "CHANGES_REQUESTED", "body": "please rename", "submitted_at": time.Now()}}
	gh.rcomments = []map[string]any{{"id": 2, "user": map[string]any{"login": "ann"}, "body": "rename me", "path": "feature.txt", "line": 1, "created_at": time.Now()}}
	gh.mu.Unlock()
	drive(state.PhaseMonitor)
	if r.FixRounds != 2 {
		t.Errorf("expected review fix round, rounds=%d", r.FixRounds)
	}
	prompts, _ := filepath.Glob(filepath.Join(r.Dir(), "session-*-review.prompt.md"))
	if len(prompts) != 1 {
		t.Fatalf("review prompt not written: %v", prompts)
	}
	pb, _ := os.ReadFile(prompts[0])
	if !strings.Contains(string(pb), "rename me") || !strings.Contains(string(pb), "please rename") {
		t.Errorf("review prompt missing feedback:\n%s", pb)
	}
	// Feedback handled: another poll must not start a new round.
	drive(state.PhaseMonitor)
	if r.FixRounds != 2 {
		t.Errorf("review feedback handled twice")
	}

	// Approval → merge → close → cleanup → done.
	gh.mu.Lock()
	gh.reviews = append(gh.reviews, map[string]any{"id": 3, "user": map[string]any{"login": "ann"}, "state": "APPROVED", "body": "", "submitted_at": time.Now()})
	gh.mu.Unlock()
	drive(state.PhaseDone)
	if gh.mergeCall != 1 {
		t.Errorf("merge called %d times", gh.mergeCall)
	}
	if got := run(t, filepath.Join(proj, "backlog"), "cat", "auth.md"); !strings.Contains(got, "status: closed") {
		t.Errorf("item not closed:\n%s", got)
	}
	if _, err := os.Stat(r.Workdir); !os.IsNotExist(err) {
		t.Errorf("workdir not cleaned up")
	}
	if len(gh.deleted) != 1 || gh.deleted[0] != r.Branch {
		t.Errorf("remote branch not deleted: %v", gh.deleted)
	}
	if len(r.Sessions) != 3 {
		t.Errorf("expected 3 agent sessions, got %d", len(r.Sessions))
	}
	fmt.Fprint(&out, "")
}
