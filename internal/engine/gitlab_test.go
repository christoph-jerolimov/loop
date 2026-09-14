package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/state"
)

// fakeGitLab is just enough of the REST API v4 for one merge request
// lifecycle in project g/r (id 5).
type fakeGitLab struct {
	mu       sync.Mutex
	remote   string
	mr       map[string]any
	merged   bool
	mergeIn  map[string]any
	titles   []string
	statuses []string
	jobs     []map[string]any
	reruns   int
	notes    []map[string]any
	// approvers and reviewerState drive the approvals and reviewers endpoints.
	approvers     []string
	reviewerState string
	discussions   []map[string]any
	replies       map[string]string
	resolved      []string
	deleted       []string
	labels        string
}

const glProject = "/api/v4/projects/g%2Fr"

func (f *fakeGitLab) headSHA() string {
	branch, _ := f.mr["source_branch"].(string)
	if branch == "" {
		return ""
	}
	out, err := exec.Command("git", "--git-dir", f.remote, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (f *fakeGitLab) handler(t *testing.T) http.Handler {
	user := func(id int, name string) map[string]any { return map[string]any{"id": id, "username": name} }
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if r.Header.Get("PRIVATE-TOKEN") != "glpat" {
			t.Errorf("missing token on %s", p)
		}
		switch {
		case p == "/api/v4/user":
			write(map[string]any{"id": 9, "username": "loop-bot", "name": "Loop Bot", "email": "loop@t"})
		case p == glProject && r.Method == http.MethodGet:
			write(map[string]any{"id": 5, "path_with_namespace": "g/r", "default_branch": "main", "visibility": "private", "permissions": map[string]any{"project_access": map[string]any{"access_level": 40}}})
		case p == glProject+"/merge_requests" && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if !strings.Contains(in["description"].(string), "Backlog item") {
				t.Errorf("MR description missing template content: %q", in["description"])
			}
			if in["target_branch"] != "main" {
				t.Errorf("target branch = %v", in["target_branch"])
			}
			title := in["title"].(string)
			f.titles = append(f.titles, title)
			f.mr = map[string]any{"id": 700, "iid": 7, "title": title, "description": in["description"], "state": "opened", "draft": strings.HasPrefix(title, "Draft: "),
				"source_branch": in["source_branch"], "target_branch": "main", "web_url": "https://gl/g/r/-/merge_requests/7", "has_conflicts": false, "detailed_merge_status": "mergeable"}
			f.mr["sha"] = f.headSHA()
			write(f.mr)
		case p == glProject+"/merge_requests" && r.Method == http.MethodGet:
			write([]any{})
		case p == glProject+"/merge_requests/7" && r.Method == http.MethodGet:
			f.mr["sha"] = f.headSHA()
			if f.merged {
				f.mr["state"] = "merged"
			}
			write(f.mr)
		case p == glProject+"/merge_requests/7" && r.Method == http.MethodPut:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if title, ok := in["title"].(string); ok {
				f.titles = append(f.titles, title)
				f.mr["title"] = title
				f.mr["draft"] = strings.HasPrefix(title, "Draft: ")
			}
			if l, ok := in["add_labels"].(string); ok {
				f.labels = l
			}
			write(f.mr)
		case p == glProject+"/merge_requests/7/commits":
			write([]any{})
		case p == glProject+"/merge_requests/7/approvals":
			var by []any
			for _, a := range f.approvers {
				by = append(by, map[string]any{"user": user(1, a)})
			}
			write(map[string]any{"approved_by": by})
		case p == glProject+"/merge_requests/7/reviewers":
			if f.reviewerState == "" {
				write([]any{})
			} else {
				write([]any{map[string]any{"user": user(1, "ann"), "state": f.reviewerState}})
			}
		case p == glProject+"/merge_requests/7/discussions":
			write(f.discussions)
		case p == glProject+"/merge_requests/7/notes" && r.Method == http.MethodGet:
			write(f.notes)
		case p == glProject+"/merge_requests/7/notes" && r.Method == http.MethodPost:
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.notes = append(f.notes, map[string]any{"id": len(f.notes) + 100, "body": in["body"], "author": user(9, "loop-bot"), "created_at": time.Now()})
			write(map[string]any{})
		case strings.HasPrefix(p, glProject+"/merge_requests/7/discussions/") && strings.HasSuffix(p, "/notes") && r.Method == http.MethodPost:
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			id := strings.TrimSuffix(strings.TrimPrefix(p, glProject+"/merge_requests/7/discussions/"), "/notes")
			if f.replies == nil {
				f.replies = map[string]string{}
			}
			f.replies[id] = in["body"]
			write(map[string]any{"id": 500})
		case strings.HasPrefix(p, glProject+"/merge_requests/7/discussions/") && r.Method == http.MethodPut:
			var in map[string]bool
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["resolved"] {
				f.resolved = append(f.resolved, strings.TrimPrefix(p, glProject+"/merge_requests/7/discussions/"))
			}
			write(map[string]any{})
		case strings.HasPrefix(p, glProject+"/repository/commits/") && strings.HasSuffix(p, "/statuses"):
			all := append([]map[string]any{}, f.jobs...)
			if n := len(f.statuses); n > 0 {
				st, desc, _ := strings.Cut(f.statuses[n-1], ": ")
				glState := map[string]string{"pending": "pending", "success": "success", "failure": "failed", "error": "failed"}[st]
				all = append(all, map[string]any{"id": 77, "name": "loop", "status": glState, "description": desc})
			}
			write(all)
		case strings.HasPrefix(p, glProject+"/statuses/") && r.Method == http.MethodPost:
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["name"] != "loop" || in["target_url"] == "" {
				t.Errorf("status = %v", in)
			}
			state := map[string]string{"pending": "pending", "success": "success", "failed": "failure"}[in["state"]]
			f.statuses = append(f.statuses, state+": "+in["description"])
			write(map[string]any{})
		case p == glProject+"/jobs/1/trace":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "\x1b[0KRunning with gitlab-runner\n$ go test ./...\n--- FAIL: TestThing (0.00s)\nFAIL\tpkg\n")
		case p == glProject+"/pipelines/9/retry":
			f.reruns++
			write(map[string]any{})
		case p == glProject+"/merge_requests/7/merge" && r.Method == http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&f.mergeIn)
			f.merged = true
			write(map[string]any{"state": "merged"})
		case strings.HasPrefix(p, glProject+"/repository/branches/") && r.Method == http.MethodDelete:
			f.deleted = append(f.deleted, "g/r:"+strings.TrimPrefix(p, glProject+"/repository/branches/"))
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	})
}

// newGitLabLifecycle points the lifecycle fixture at a fake GitLab
// instead of the fake GitHub.
func newGitLabLifecycle(t *testing.T, tweak func(cfg *config.Config)) (*lifecycle, *fakeGitLab) {
	t.Helper()
	gl := &fakeGitLab{}
	lc := newLifecycleWith(t, func(cfg *config.Config, lc *lifecycle) {
		gl.remote = lc.remote
		srv := httptest.NewServer(gl.handler(t))
		t.Cleanup(srv.Close)
		t.Setenv("GITLAB_TOKEN", "glpat")
		cfg.Repo = config.Repo{URL: lc.remote, GitLab: "g/r", GitLabURL: srv.URL}
		if tweak != nil {
			tweak(cfg)
		}
	})
	return lc, gl
}

func TestGitLabLifecycle(t *testing.T) {
	lc, gl := newGitLabLifecycle(t, func(cfg *config.Config) {
		cfg.PR.Labels = []string{"loop"}
	})
	root, remote, proj, r := lc.root, lc.remote, lc.proj, lc.run
	drive := func(want state.Phase) { t.Helper(); lc.drive(t, want) }

	// checkout → session → verify → MR → monitor
	drive(state.PhaseMonitor)
	if r.PR == nil || r.PR.Number != 7 || r.PR.URL != "https://gl/g/r/-/merge_requests/7" || r.PR.NodeID != "700" || !r.PR.Draft {
		t.Fatalf("MR not recorded: %+v", r.PR)
	}
	if len(gl.titles) != 1 || !strings.HasPrefix(gl.titles[0], "Draft: ") || gl.labels != "loop" {
		t.Errorf("MR should open as a draft with the configured labels: titles=%v labels=%q", gl.titles, gl.labels)
	}
	// No checks at all counts as green → the draft is marked ready by dropping the prefix.
	drive(state.PhaseMonitor)
	if len(gl.titles) != 2 || strings.HasPrefix(gl.titles[1], "Draft") || r.PR.Draft {
		t.Errorf("draft MR should have been marked ready: %v", gl.titles)
	}

	// A failed job → the pipeline is retried once → still red → CI fix round with the job trace.
	gl.mu.Lock()
	gl.jobs = []map[string]any{{"id": 1, "name": "test", "status": "failed", "target_url": "https://gl/g/r/-/jobs/1", "pipeline_id": 9}}
	gl.mu.Unlock()
	drive(state.PhaseMonitor)
	if gl.reruns != 1 || r.FixRounds != 0 {
		t.Fatalf("the first red poll must retry the pipeline, not start a fix round: reruns=%d rounds=%d\n%s", gl.reruns, r.FixRounds, lc.out.String())
	}
	drive(state.PhaseMonitor)
	if r.FixRounds != 1 || run(t, root, "git", "--git-dir", remote, "rev-list", "--count", r.Branch) != "3" {
		t.Errorf("expected one pushed CI fix round, got %d", r.FixRounds)
	}
	ciPrompts, _ := filepath.Glob(filepath.Join(r.Dir(), "session-*-ci.prompt.md"))
	if len(ciPrompts) != 1 {
		t.Fatalf("ci prompt not written: %v", ciPrompts)
	}
	cp, _ := os.ReadFile(ciPrompts[0])
	if !strings.Contains(string(cp), "--- FAIL: TestThing") || strings.Contains(string(cp), "\x1b") {
		t.Errorf("ci prompt should contain the job trace without colour codes:\n%s", cp)
	}
	// The pipeline of the new head is running → wait.
	gl.mu.Lock()
	gl.jobs[0]["status"] = "running"
	gl.mu.Unlock()
	drive(state.PhaseMonitor)
	if r.FixRounds != 1 {
		t.Errorf("a running job must not start another round")
	}

	// Green again; a reviewer requests changes with an inline note.
	gl.mu.Lock()
	gl.jobs[0]["status"] = "success"
	gl.reviewerState = "requested_changes"
	gl.discussions = []map[string]any{{"id": "d2", "notes": []any{map[string]any{"id": 2, "body": "rename me", "author": map[string]any{"id": 1, "username": "ann"}, "created_at": time.Now(),
		"resolvable": true, "resolved": false, "position": map[string]any{"new_path": "feature.txt", "new_line": 1}}}}}
	gl.mu.Unlock()
	drive(state.PhaseMonitor)
	if r.FixRounds != 2 {
		t.Fatalf("expected a review fix round, rounds=%d\n%s", r.FixRounds, lc.out.String())
	}
	prompts, _ := filepath.Glob(filepath.Join(r.Dir(), "session-*-review.prompt.md"))
	if len(prompts) != 1 {
		t.Fatalf("review prompt not written: %v", prompts)
	}
	pb, _ := os.ReadFile(prompts[0])
	if !strings.Contains(string(pb), "rename me") || !strings.Contains(string(pb), "feature.txt") || !strings.Contains(string(pb), "CHANGES_REQUESTED") {
		t.Errorf("review prompt missing feedback:\n%s", pb)
	}
	if got := gl.replies["d2"]; !strings.HasPrefix(got, "Renamed as asked. (round 2, ") {
		t.Errorf("discussion d2 was not answered with the agent's note: %q", got)
	}
	if len(gl.resolved) != 1 || gl.resolved[0] != "d2" {
		t.Errorf("discussion d2 should be resolved, got %v", gl.resolved)
	}
	drive(state.PhaseMonitor)
	if r.FixRounds != 2 {
		t.Errorf("review feedback handled twice")
	}

	// Approval → merge (squash) → close → cleanup → done.
	gl.mu.Lock()
	gl.reviewerState = "approved"
	gl.approvers = []string{"ann"}
	gl.mu.Unlock()
	drive(state.PhaseDone)
	if gl.mergeIn == nil || gl.mergeIn["squash"] != true || gl.mergeIn["merge_when_pipeline_succeeds"] != nil {
		t.Errorf("merge call = %v", gl.mergeIn)
	}
	if got := run(t, filepath.Join(proj, "backlog"), "cat", "auth.md"); !strings.Contains(got, "status: closed") {
		t.Errorf("item not closed:\n%s", got)
	}
	if _, err := os.Stat(r.Workdir); !os.IsNotExist(err) {
		t.Errorf("workdir not cleaned up")
	}
	if len(gl.deleted) != 1 || gl.deleted[0] != "g/r:"+url.PathEscape(r.Branch) {
		t.Errorf("remote branch not deleted (the name must be URL-encoded): %v", gl.deleted)
	}
	joined := strings.Join(gl.statuses, "\n")
	for _, want := range []string{"pending: monitoring; fix rounds 0/3", "pending: fix round 1/3 (ci)", "pending: fix round 2/3 (review)", "success: merged and ticket closed; fix rounds 2/3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("commit statuses lack %q:\n%s", want, joined)
		}
	}
	if len(r.Sessions) != 3 {
		t.Errorf("expected 3 agent sessions, got %d", len(r.Sessions))
	}
}

func TestGitLabMergeRejectedThenBlocked(t *testing.T) {
	lc, gl := newGitLabLifecycle(t, func(cfg *config.Config) { cfg.Workflow.Merge = config.MergeWhenGreen })
	drive := func(want state.Phase) { t.Helper(); lc.drive(t, want) }
	drive(state.PhaseMonitor)
	gl.mu.Lock()
	gl.mr["detailed_merge_status"] = "not_approved"
	gl.mu.Unlock()
	driveUntil(t, lc, lc.run, state.PhaseBlocked)
	if !strings.Contains(lc.run.Error, "merge blocked by branch protection") {
		t.Errorf("a not_approved merge request must park the run: %q\n%s", lc.run.Error, lc.out.String())
	}
	if len(gl.notes) == 0 || !strings.Contains(gl.notes[len(gl.notes)-1]["body"].(string), "/loop resume") {
		t.Errorf("the note on the MR must say how to resume: %v", gl.notes)
	}
}
