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
	mu     sync.Mutex
	pr     map[string]any
	prHead string
	// issueComments are the comments on issue 12, the GitHub-sourced item.
	issueComments []map[string]any
	// remote and fork are the bare repositories behind the fake, so the
	// PR head is the real branch head like on GitHub.
	remote, fork string
	commits      []map[string]any
	// reruns counts rerun-failed-jobs calls; flaky turns the failed check
	// green on the re-run.
	reruns int
	flaky  bool
	// statuses records every commit status posted, as "state: description".
	statuses  []string
	checks    []map[string]any
	reviews   []map[string]any
	rcomments []map[string]any
	icomments []map[string]any
	merged    bool
	mergeable bool
	mstate    string
	readyCall int
	mergeCall int
	deleted   []string
	replies   map[int64]string
	resolved  []string
	perms     map[string]string
	reactions map[int64]string
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
				Query     string            `json:"query"`
				Variables map[string]string `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&q)
			if strings.Contains(q.Query, "markPullRequestReadyForReview") {
				f.readyCall++
				f.pr["draft"] = false
			}
			if strings.Contains(q.Query, "reviewThreads") {
				write(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{"nodes": []any{
					map[string]any{"id": "RT_2", "isResolved": false, "comments": map[string]any{"nodes": []any{map[string]any{"databaseId": 2}}}},
				}}}}}})
				return
			}
			if strings.Contains(q.Query, "resolveReviewThread") {
				f.resolved = append(f.resolved, q.Variables["id"])
			}
			write(map[string]any{"data": map[string]any{}})
		case p == "/repos/o/r/pulls" && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if !strings.Contains(in["body"].(string), "Backlog item") {
				t.Errorf("PR body missing template content: %q", in["body"])
			}
			f.prHead, _ = in["head"].(string)
			sha := f.headSHA()
			if sha == "" {
				sha = "sha1"
			}
			f.pr = map[string]any{"number": 7, "node_id": "PR_7", "title": in["title"], "body": in["body"], "state": "open",
				"draft": in["draft"], "merged": false, "mergeable": true, "mergeable_state": "clean", "html_url": "https://gh/o/r/pull/7",
				"head": map[string]any{"ref": in["head"], "sha": sha}, "base": map[string]any{"ref": "main"}}
			write(f.pr)
		case p == "/repos/o/r/pulls" && r.Method == http.MethodGet:
			write([]any{})
		case p == "/repos/o/r/issues" && r.Method == http.MethodGet:
			write([]any{fakeIssue()})
		case p == "/repos/o/r/issues/12" && r.Method == http.MethodGet:
			write(fakeIssue())
		case p == "/repos/o/r/issues/12" && r.Method == http.MethodPatch:
			write(fakeIssue())
		case p == "/repos/o/r/issues/12/comments" && r.Method == http.MethodGet:
			write(f.issueComments)
		case p == "/repos/o/r/issues/12/comments" && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.issueComments = append(f.issueComments, map[string]any{"id": len(f.issueComments) + 200, "body": in["body"], "user": map[string]any{"login": "loop-bot"}, "author_association": "OWNER", "created_at": time.Now()})
			write(map[string]any{})
		case strings.HasPrefix(p, "/repos/o/r/issues/12/labels"):
			write([]any{})
		case p == "/repos/o/r/pulls/7" && r.Method == http.MethodGet:
			f.pr["merged"] = f.merged
			f.pr["mergeable"] = f.mergeable
			if sha := f.headSHA(); sha != "" {
				f.pr["head"] = map[string]any{"ref": f.prHead, "sha": sha}
			}
			if f.mstate != "" {
				f.pr["mergeable_state"] = f.mstate
			}
			if f.merged {
				f.pr["state"] = "closed"
			}
			write(f.pr)
		case p == "/repos/o/r/pulls/7/commits":
			if f.commits == nil {
				write([]any{})
			} else {
				write(f.commits)
			}
		case p == "/repos/o/r/pulls/7/reviews":
			write(f.reviews)
		case p == "/repos/o/r/pulls/7/comments":
			write(f.rcomments)
		case strings.HasPrefix(p, "/repos/o/r/pulls/7/comments/") && strings.HasSuffix(p, "/replies"):
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			var id int64
			fmt.Sscanf(strings.TrimPrefix(p, "/repos/o/r/pulls/7/comments/"), "%d/replies", &id)
			if f.replies == nil {
				f.replies = map[int64]string{}
			}
			f.replies[id] = in["body"]
			write(map[string]any{"id": 500})
		case p == "/repos/o/r/issues/7/comments" && r.Method == http.MethodGet:
			write(f.icomments)
		case p == "/repos/o/r/issues/7/comments" && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.icomments = append(f.icomments, map[string]any{"id": len(f.icomments) + 100, "body": in["body"], "user": map[string]any{"login": "loop-bot"}, "created_at": time.Now()})
			write(map[string]any{})
		case strings.HasPrefix(p, "/repos/o/r/actions/runs/") && strings.HasSuffix(p, "/rerun-failed-jobs"):
			f.reruns++
			if f.flaky {
				for _, c := range f.checks {
					c["conclusion"] = "success"
				}
			}
			w.WriteHeader(201)
		case p == "/repos/o/r/actions/jobs/1/logs":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "2026-09-12T16:44:04.0000000Z ##[group]Run go test\n2026-09-12T16:44:05.0000000Z --- FAIL: TestThing (0.00s)\n2026-09-12T16:44:06.0000000Z FAIL\tpkg\n")
		case strings.HasPrefix(p, "/repos/o/r/commits/") && strings.HasSuffix(p, "/check-runs"):
			write(map[string]any{"total_count": len(f.checks), "check_runs": f.checks})
		case strings.HasPrefix(p, "/repos/o/r/commits/") && strings.HasSuffix(p, "/status"):
			var own []any
			if n := len(f.statuses); n > 0 {
				st, desc, _ := strings.Cut(f.statuses[n-1], ": ")
				own = append(own, map[string]any{"context": "loop", "state": st, "description": desc})
			}
			write(map[string]any{"state": "success", "statuses": own})
		case strings.HasPrefix(p, "/repos/o/r/statuses/") && r.Method == http.MethodPost:
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["context"] != "loop" || len(in["description"]) > 140 {
				t.Errorf("status = %v", in)
			}
			f.statuses = append(f.statuses, in["state"]+": "+in["description"])
			write(map[string]any{})
		case p == "/repos/o/r/pulls/7/merge":
			f.mergeCall++
			f.merged = true
			write(map[string]any{"merged": true})
		case strings.HasPrefix(p, "/repos/o/r/git/refs/heads/"):
			f.deleted = append(f.deleted, "o/r:"+strings.TrimPrefix(p, "/repos/o/r/git/refs/heads/"))
			w.WriteHeader(204)
		case strings.HasPrefix(p, "/repos/f/r/git/refs/heads/"):
			f.deleted = append(f.deleted, "f/r:"+strings.TrimPrefix(p, "/repos/f/r/git/refs/heads/"))
			w.WriteHeader(204)
		case p == "/repos/o/r/pulls/7/requested_reviewers", strings.HasPrefix(p, "/repos/o/r/issues/7/labels"):
			write(map[string]any{})
		case strings.HasPrefix(p, "/repos/o/r/collaborators/") && strings.HasSuffix(p, "/permission"):
			login := strings.TrimSuffix(strings.TrimPrefix(p, "/repos/o/r/collaborators/"), "/permission")
			perm := f.perms[login]
			if perm == "" {
				perm = "none"
			}
			write(map[string]any{"permission": perm})
		case strings.HasPrefix(p, "/repos/o/r/issues/comments/") && strings.HasSuffix(p, "/reactions"):
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			var id int64
			fmt.Sscanf(strings.TrimPrefix(p, "/repos/o/r/issues/comments/"), "%d/reactions", &id)
			if f.reactions == nil {
				f.reactions = map[int64]string{}
			}
			f.reactions[id] = in["content"]
			write(map[string]any{})
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	})
}

func fakeIssue() map[string]any {
	return map[string]any{"number": 12, "node_id": "I_12", "title": "Add search", "body": "Index things.", "state": "open",
		"html_url": "https://gh/o/r/issues/12", "labels": []any{}, "created_at": "2026-01-02T00:00:00Z", "user": map[string]any{"login": "ann"}}
}

// headSHA resolves the PR branch head in the bare repository it was pushed
// to (the fork for "owner:branch" heads).
func (f *fakeGitHub) headSHA() string {
	repo, branch := f.remote, f.prHead
	if owner, b, ok := strings.Cut(branch, ":"); ok {
		branch = b
		if owner != "o" {
			repo = f.fork
		}
	}
	if repo == "" || branch == "" {
		return ""
	}
	out, err := exec.Command("git", "--git-dir", repo, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
if [ -n "$LOOP_FINDINGS_FILE" ]; then
  if [ -n "$FAKE_CLEAN_REVIEW" ]; then echo "No findings." > "$LOOP_FINDINGS_FILE"; else printf -- '- feature.txt: missing the closing note\n' > "$LOOP_FINDINGS_FILE"; fi
  echo probe > review-scratch.txt
  echo "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"reviewed\",\"session_id\":\"$sid\",\"total_cost_usd\":0.5}"
  exit 0
fi
if [ -n "$LOOP_PLAN_FILE" ]; then
  printf '1. Goal: feature.txt exists.\n2. Estimate: S, one session.\n' > "$LOOP_PLAN_FILE"
  echo scratch > scratch.txt
  echo "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"planned\",\"session_id\":\"$sid\",\"total_cost_usd\":0.5}"
  exit 0
fi
if [ -n "$FAKE_NO_CHANGES" ]; then
  echo "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"nothing to do\",\"session_id\":\"$sid\",\"total_cost_usd\":0.25}"
  exit 0
fi
git config user.email a@t; git config user.name a
echo "$prompt" >> feature.txt
git add -A && git commit -qm "agent work"
printf '## Summary\nAgent did things.\n' >> "$LOOP_SUMMARY_FILE"
echo "$LOOP_PROMPT_FILE" >> "$LOOP_RUN_DIR/prompt-files"
if [ -n "$LOOP_REPLIES_FILE" ]; then printf '[{"id": 2, "reply": "Renamed as asked.", "resolved": true}]' > "$LOOP_REPLIES_FILE"; fi
echo "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"$sid\",\"total_cost_usd\":1.25,\"num_turns\":4}"
`

// lifecycle wires a fake remote, a fake agent CLI, a fake GitHub API and
// an engine for a project with one markdown item.
type lifecycle struct {
	root, remote, proj string
	fork               string
	gh                 *fakeGitHub
	eng                *Engine
	run                *state.Run
	out                strings.Builder
}

func newLifecycle(t *testing.T, tweak func(cfg *config.Config)) *lifecycle {
	return newLifecycleWith(t, func(cfg *config.Config, _ *lifecycle) {
		if tweak != nil {
			tweak(cfg)
		}
	})
}

func newLifecycleWith(t *testing.T, tweak func(cfg *config.Config, lc *lifecycle)) *lifecycle {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	lc := &lifecycle{}
	root := t.TempDir()
	lc.root = root
	remote := filepath.Join(root, "remote.git")
	lc.remote = remote
	run(t, root, "git", "init", "-q", "--bare", remote)
	fork := filepath.Join(root, "fork.git")
	run(t, root, "git", "init", "-q", "--bare", fork)
	lc.fork = fork
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

	gh := &fakeGitHub{mergeable: true, remote: remote, fork: fork}
	lc.gh = gh
	srv := httptest.NewServer(gh.handler(t))
	t.Cleanup(srv.Close)
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_TOKEN", "x")

	proj := filepath.Join(root, "proj")
	lc.proj = proj
	os.MkdirAll(filepath.Join(proj, "backlog"), 0o755)
	os.WriteFile(filepath.Join(proj, "backlog", "auth.md"), []byte("---\ntitle: Add auth\n---\nBuild login.\n"), 0o644)
	os.MkdirAll(filepath.Join(proj, "hooks"), 0o755)
	os.WriteFile(filepath.Join(proj, "hooks", "setup.sh"), []byte("#!/bin/sh\necho \"$LOOP_BRANCH\" > \"$LOOP_RUN_DIR/setup-ran\"\n"), 0o755)
	cfg := &config.Config{
		Dir:     proj,
		Repo:    config.Repo{URL: remote, GitHub: "o/r"},
		Sources: []config.SourceConfig{{Name: "backlog", Type: "markdown", Path: "backlog", Claim: true}},
		Steps: config.Steps{
			Setup:  []config.Step{{Script: "hooks/setup.sh"}},
			Verify: []config.Step{{Name: "has-feature", Run: `echo run >> "$LOOP_RUN_DIR/verify-runs" && test -f feature.txt`}},
		},
		Workflow: config.Workflow{Merge: config.MergeWhenGreenApprove, PollInterval: config.Duration(time.Millisecond), Gates: []string{}},
	}
	if tweak != nil {
		tweak(cfg, lc)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srcs, err := source.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := New(cfg, srcs, &lc.out)
	if err != nil {
		t.Fatal(err)
	}
	lc.eng = eng
	ctx := context.Background()
	it, err := srcs.Resolve(ctx, "auth.md")
	if err != nil {
		t.Fatal(err)
	}
	r, err := eng.Start(ctx, it, false)
	if err != nil {
		t.Fatal(err)
	}
	lc.run = r
	return lc
}

// drive advances the run and asserts the phase it parks in.
func (lc *lifecycle) drive(t *testing.T, want state.Phase) {
	t.Helper()
	if err := lc.eng.Drive(context.Background(), lc.run); err != nil {
		t.Fatalf("drive: %v\n%s", err, lc.out.String())
	}
	if lc.run.Phase != want {
		t.Fatalf("phase = %s, want %s (error=%q)\n%s", lc.run.Phase, want, lc.run.Error, lc.out.String())
	}
	time.Sleep(2 * time.Millisecond)
}

func TestFullLifecycle(t *testing.T) {
	lc := newLifecycle(t, nil)
	root, remote, proj, gh, r := lc.root, lc.remote, lc.proj, lc.gh, lc.run
	drive := func(want state.Phase) { t.Helper(); lc.drive(t, want) }
	out := &lc.out
	// checkout → session → verify → PR → monitor (no checks yet: green but not approved)
	drive(state.PhaseMonitor)
	if r.PR == nil || r.PR.Number != 7 {
		t.Fatalf("PR not recorded: %+v", r.PR)
	}
	if b, err := os.ReadFile(filepath.Join(r.Dir(), "setup-ran")); err != nil || strings.TrimSpace(string(b)) != r.Branch {
		t.Errorf("setup script did not run in the workdir with loop env: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "run.yaml")); err != nil {
		t.Errorf("run.yaml not written: %v", err)
	}
	settings, err := os.ReadFile(filepath.Join(r.Workdir, ".claude", "settings.local.json"))
	if err != nil || !strings.Contains(string(settings), `"Bash(git commit:*)"`) || !strings.Contains(string(settings), `"Bash(git push:*)"`) || !strings.Contains(string(settings), `"defaultMode": "acceptEdits"`) {
		t.Errorf("permission rules not written to the workdir: %v\n%s", err, settings)
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

	// Red CI → the failed jobs are re-run once → still red → fix round → push.
	gh.mu.Lock()
	gh.checks = []map[string]any{{"id": 1, "name": "test", "status": "completed", "conclusion": "failure", "html_url": "https://gh/o/r/actions/runs/9/job/1", "output": map[string]any{"summary": "boom"}}}
	gh.mu.Unlock()
	drive(state.PhaseMonitor)
	if gh.reruns != 1 || r.FixRounds != 0 || r.CIRerunSHA != r.PR.HeadSHA {
		t.Fatalf("the first red poll must re-run the jobs, not start a fix round: reruns=%d rounds=%d", gh.reruns, r.FixRounds)
	}
	drive(state.PhaseMonitor)
	if r.FixRounds != 1 || r.LastCIFixSHA == "" {
		t.Errorf("expected one CI fix round, got %d (%q)", r.FixRounds, r.LastCIFixSHA)
	}
	if run(t, root, "git", "--git-dir", remote, "rev-list", "--count", r.Branch) != "3" {
		t.Error("CI fix was not pushed")
	}
	ciPrompts, _ := filepath.Glob(filepath.Join(r.Dir(), "session-*-ci.prompt.md"))
	if len(ciPrompts) != 1 {
		t.Fatalf("ci prompt not written: %v", ciPrompts)
	}
	cp, _ := os.ReadFile(ciPrompts[0])
	if !strings.Contains(string(cp), "--- FAIL: TestThing") || strings.Contains(string(cp), "2026-09-12T16:44:04") {
		t.Errorf("ci prompt should contain the job log tail without timestamps:\n%s", cp)
	}
	// The fix pushed a new head whose checks are still running → wait.
	gh.mu.Lock()
	gh.checks[0]["status"] = "in_progress"
	gh.mu.Unlock()
	drive(state.PhaseMonitor)
	if r.FixRounds != 1 {
		t.Errorf("a pending check must not start another round")
	}

	// Green again, reviewer requests changes with an inline comment.
	gh.mu.Lock()
	gh.checks[0]["status"] = "completed"
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
	if got := gh.replies[2]; !strings.HasPrefix(got, "Renamed as asked. (round 2, ") {
		t.Errorf("inline comment 2 was not answered with the agent's note: %q", got)
	}
	if len(gh.resolved) != 1 || gh.resolved[0] != "RT_2" {
		t.Errorf("thread of comment 2 should be resolved, got %v", gh.resolved)
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
	if len(gh.deleted) != 1 || gh.deleted[0] != "o/r:"+r.Branch {
		t.Errorf("remote branch not deleted: %v", gh.deleted)
	}
	if len(r.Sessions) != 3 {
		t.Errorf("expected 3 agent sessions, got %d", len(r.Sessions))
	}
	joined := strings.Join(gh.statuses, "\n")
	for _, want := range []string{"pending: monitoring; fix rounds 0/3", "pending: fix round 1/3 (ci)", "pending: fix round 2/3 (review)", "pending: merging; fix rounds 2/3", "success: merged and ticket closed; fix rounds 2/3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("PR statuses lack %q:\n%s", want, joined)
		}
	}
	if len(gh.statuses) > 12 {
		t.Errorf("unchanged state must not be posted again every poll, got %d statuses:\n%s", len(gh.statuses), joined)
	}
	promptFiles, _ := os.ReadFile(filepath.Join(r.Dir(), "prompt-files"))
	for i, pf := range strings.Split(strings.TrimSpace(string(promptFiles)), "\n") {
		if want := fmt.Sprintf("session-%02d-", i+1); !strings.HasPrefix(filepath.Base(pf), want) || !strings.HasSuffix(pf, ".prompt.md") {
			t.Errorf("session %d got LOOP_PROMPT_FILE=%q", i+1, pf)
		}
		if b, err := os.ReadFile(pf); err != nil || len(b) == 0 {
			t.Errorf("prompt file %s must exist with the prompt the session received: %v", pf, err)
		}
	}
	verifyRuns, _ := os.ReadFile(filepath.Join(r.Dir(), "verify-runs"))
	if n := strings.Count(string(verifyRuns), "run"); n != 3 {
		t.Errorf("verify steps should run after the session and after each of the two fix rounds, ran %d times", n)
	}
	_ = out
}

func TestMergeBlockedByBranchProtection(t *testing.T) {
	lc := newLifecycle(t, func(cfg *config.Config) {
		cfg.Workflow.Merge = config.MergeWhenGreen
		cfg.Steps.Blocked = []config.Step{{Name: "notify", Run: `printf '%s|%s|%s' "$LOOP_RUN_PHASE" "$LOOP_PR_URL" "$LOOP_RUN_ERROR" > "$LOOP_RUN_DIR/blocked-hook"`}}
	})
	lc.drive(t, state.PhaseMonitor) // PR opened
	lc.gh.mu.Lock()
	lc.gh.checks = []map[string]any{{"id": 2, "name": "test", "status": "completed", "conclusion": "success", "html_url": "u"}}
	lc.gh.mstate = "blocked"
	lc.gh.mu.Unlock()
	lc.drive(t, state.PhaseBlocked)
	if lc.gh.mergeCall != 0 {
		t.Errorf("merge must not be attempted while branch protection blocks it, got %d calls", lc.gh.mergeCall)
	}
	if !strings.Contains(lc.run.Error, "branch protection") {
		t.Errorf("run error = %q", lc.run.Error)
	}
	var noted bool
	for _, c := range lc.gh.icomments {
		if strings.Contains(c["body"].(string), "branch protection") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("expected one note on the PR, comments: %v", lc.gh.icomments)
	}
	hook, err := os.ReadFile(filepath.Join(lc.run.Dir(), "blocked-hook"))
	if err != nil || !strings.HasPrefix(string(hook), "blocked|https://gh/o/r/pull/7|merge blocked by branch protection") {
		t.Errorf("steps.blocked did not run with the run environment: %v %q", err, hook)
	}
	// Once a human unblocks it, resume continues to the merge.
	lc.gh.mu.Lock()
	lc.gh.mstate = "clean"
	lc.gh.mu.Unlock()
	if err := lc.eng.Resume(lc.run); err != nil {
		t.Fatal(err)
	}
	lc.drive(t, state.PhaseDone)
	if lc.gh.mergeCall != 1 {
		t.Errorf("merge calls after resume = %d", lc.gh.mergeCall)
	}
}

func TestGateReleasedFromPRComment(t *testing.T) {
	lc := newLifecycle(t, func(cfg *config.Config) {
		cfg.Workflow.Merge = config.MergeWhenGreen
		cfg.Workflow.Gates = []string{config.GateBeforeMerge}
	})
	lc.gh.perms = map[string]string{"ann": "write", "eve": "read"}
	lc.drive(t, state.PhaseMonitor) // PR opened
	lc.gh.mu.Lock()
	lc.gh.checks = []map[string]any{{"id": 3, "name": "test", "status": "completed", "conclusion": "success", "html_url": "u"}}
	lc.gh.mu.Unlock()
	lc.drive(t, state.PhaseMerge) // green → merge phase, parks at the gate
	if lc.run.Gate != config.GateBeforeMerge {
		t.Fatalf("expected the run to wait at the merge gate, gate=%q phase=%s", lc.run.Gate, lc.run.Phase)
	}
	var noted bool
	for _, c := range lc.gh.icomments {
		noted = noted || strings.Contains(c["body"].(string), "/loop approve")
	}
	if !noted {
		t.Error("expected a note on the PR explaining /loop approve")
	}

	// A reader's command is ignored, a writer's releases the gate.
	lc.gh.mu.Lock()
	lc.gh.icomments = append(lc.gh.icomments,
		map[string]any{"id": 900, "body": "/loop approve", "user": map[string]any{"login": "eve"}, "created_at": time.Now()},
		map[string]any{"id": 901, "body": "/LOOP approve\nlooks good", "user": map[string]any{"login": "ann"}, "created_at": time.Now()},
	)
	lc.gh.mu.Unlock()
	lc.run.NextPoll = time.Time{}
	lc.drive(t, state.PhaseDone)
	if lc.gh.mergeCall != 1 {
		t.Errorf("merge calls = %d, want 1", lc.gh.mergeCall)
	}
	if lc.gh.reactions[900] != "confused" || lc.gh.reactions[901] != "+1" {
		t.Errorf("reactions = %v, want eve:confused ann:+1", lc.gh.reactions)
	}
}

func TestLoopApproveReleasesMergeGate(t *testing.T) {
	lc := newLifecycle(t, func(cfg *config.Config) {
		cfg.Workflow.Merge = config.MergeWhenGreen
		cfg.Workflow.Gates = []string{config.GateBeforeMerge}
		f := false
		cfg.Workflow.PRCommands = &f
	})
	lc.drive(t, state.PhaseMonitor)
	lc.gh.mu.Lock()
	lc.gh.checks = []map[string]any{{"id": 3, "name": "test", "status": "completed", "conclusion": "success", "html_url": "u"}}
	lc.gh.mu.Unlock()
	lc.drive(t, state.PhaseMerge)
	if lc.run.Gate != config.GateBeforeMerge {
		t.Fatalf("expected to wait at the merge gate, got gate=%q", lc.run.Gate)
	}
	// What `loop approve` does.
	lc.run.GateApproved = lc.run.Gate
	lc.drive(t, state.PhaseDone)
	if lc.gh.mergeCall != 1 {
		t.Errorf("merge calls = %d, want 1", lc.gh.mergeCall)
	}
}

func TestForkWorkflow(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		cfg.Repo.Fork = "f/r"
		cfg.Repo.PushURL = lc.fork
		cfg.Workflow.Merge = config.MergeWhenGreen
	})
	lc.drive(t, state.PhaseMonitor)
	if lc.gh.prHead != "f:"+lc.run.Branch {
		t.Errorf("PR head = %q, want the fork owner prefix", lc.gh.prHead)
	}
	if out := run(t, lc.root, "git", "--git-dir", lc.fork, "branch", "--list", lc.run.Branch); !strings.Contains(out, lc.run.Branch) {
		t.Errorf("branch was not pushed to the fork: %q", out)
	}
	if out := run(t, lc.root, "git", "--git-dir", lc.remote, "branch", "--list", lc.run.Branch); strings.Contains(out, lc.run.Branch) {
		t.Errorf("branch must not be pushed to the upstream repository: %q", out)
	}
	lc.gh.mu.Lock()
	lc.gh.checks = []map[string]any{{"id": 3, "name": "test", "status": "completed", "conclusion": "success", "html_url": "u"}}
	lc.gh.mu.Unlock()
	lc.drive(t, state.PhaseDone)
	if len(lc.gh.deleted) != 1 || lc.gh.deleted[0] != "f/r:"+lc.run.Branch {
		t.Errorf("branch should be deleted on the fork: %v", lc.gh.deleted)
	}
}

// fakePodman logs every invocation to the run folder (the client carries
// LOOP_RUN_DIR) and executes whatever follows the image, like a container
// would: the harness, a step's sh -c, or a mounted script.
const fakePodman = `#!/bin/sh
printf '%s\n' "$@" >> "$LOOP_RUN_DIR/podman-calls"
echo --- >> "$LOOP_RUN_DIR/podman-calls"
while [ $# -gt 0 ]; do a=$1; shift; [ "$a" = "example.test/harness:1" ] && break; done
[ $# -gt 0 ] && exec "$@"
`

func TestSandboxRunsSessionsAndStepsInContainer(t *testing.T) {
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		os.WriteFile(filepath.Join(lc.root, "bin", "podman"), []byte(fakePodman), 0o755)
		cfg.Repo.Workdir = "clone"
		cfg.Agent.Sandbox = config.Sandbox{Image: "example.test/harness:1"}
		cfg.Steps.BeforePR = []config.Step{{Name: "notify", Run: `echo host > "$LOOP_RUN_DIR/host-ran"`, Host: true}}
	})
	lc.drive(t, state.PhaseMonitor)
	r := lc.run
	calls, err := os.ReadFile(filepath.Join(r.Dir(), "podman-calls"))
	if err != nil {
		t.Fatalf("podman was not used: %v\n%s", err, lc.out.String())
	}
	script := filepath.Join(lc.proj, "hooks", "setup.sh")
	for _, want := range []string{
		"-v\n" + r.Workdir + ":" + r.Workdir + ":Z\n", "-v\n" + r.Dir() + ":" + r.Dir() + ":Z\n", "-v\nloop-proj-home:/home/agent\n",
		"-v\n" + script + ":" + script + ":ro,z\n", "example.test/harness:1\n" + script + "\n---", // the setup script, mounted and run
		"example.test/harness:1\nsh\n-c\necho run >>", // the verify step
		"example.test/harness:1\nclaude\n-p\n",        // the session
		"-e\nLOOP_RUN_DIR\n",
	} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("podman calls missing %q:\n%s", want, calls)
		}
	}
	if strings.Contains(string(calls), "host-ran") {
		t.Errorf("a host: true step must not run in the container:\n%s", calls)
	}
	for _, f := range []string{"setup-ran", "host-ran", "verify-runs"} {
		if _, err := os.Stat(filepath.Join(r.Dir(), f)); err != nil {
			t.Errorf("%s: %v\n%s", f, err, lc.out.String())
		}
	}
	if !strings.Contains(lc.out.String(), "in podman image example.test/harness:1") {
		t.Errorf("session log does not name the sandbox:\n%s", lc.out.String())
	}
}
