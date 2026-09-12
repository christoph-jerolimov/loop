package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/christoph-jerolimov/loop/internal/config"
)

// fakeAPI serves the subset of the GitHub issues API the source uses and
// records mutations.
type fakeAPI struct {
	private      bool
	labelsAdded  []string
	labelRemoved string
	comments     []string
	closed       bool
	listedLabels string
}

func (f *fakeAPI) handler(t *testing.T) http.Handler {
	issue := func(n int, title, body, state string, labels ...string) map[string]any {
		ls := []map[string]any{}
		for _, l := range labels {
			ls = append(ls, map[string]any{"name": l})
		}
		return map[string]any{"number": n, "node_id": "I_" + title, "title": title, "body": body, "state": state, "html_url": "https://github.com/o/r/issues/" + itoa(n), "labels": ls, "created_at": "2026-01-0" + itoa(n%9+1) + "T00:00:00Z", "user": map[string]any{"login": "ann"}}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case r.URL.Path == "/repos/o/r" && r.Method == http.MethodGet:
			write(map[string]any{"full_name": "o/r", "default_branch": "main", "private": f.private})
		case r.URL.Path == "/repos/o/r/issues" && r.Method == http.MethodGet:
			f.listedLabels = r.URL.Query().Get("labels")
			pr := issue(3, "A pull request", "", "open", "ready")
			pr["pull_request"] = map[string]any{"url": "x"}
			write([]any{issue(12, "Add auth", "Build login.\n\ndepends on: #7", "open", "ready"), issue(14, "Wip", "", "open", "ready", "loop:in-progress"), pr})
		case r.URL.Path == "/repos/o/r/issues/12" && r.Method == http.MethodGet:
			write(issue(12, "Add auth", "Build login.\n\ndepends on: #7\nmodel: claude-opus-5", "open", "ready"))
		case r.URL.Path == "/repos/o/r/issues/7" && r.Method == http.MethodGet:
			write(issue(7, "Dependency", "", "closed"))
		case r.URL.Path == "/repos/o/r/issues/12/comments" && r.Method == http.MethodGet:
			write([]any{
				map[string]any{"id": 1, "body": "please add SSO too", "user": map[string]any{"login": "bob"}, "created_at": "2026-01-03T00:00:00Z"},
				map[string]any{"id": 2, "body": claimMarker + " run-9\n\nloop picked this up", "user": map[string]any{"login": "loop-bot"}, "created_at": "2026-01-04T00:00:00Z"},
			})
		case r.URL.Path == "/repos/o/r/issues/12/comments" && r.Method == http.MethodPost:
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.comments = append(f.comments, in["body"])
			write(map[string]any{})
		case r.URL.Path == "/repos/o/r/issues/12/labels" && r.Method == http.MethodPost:
			var in map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.labelsAdded = append(f.labelsAdded, in["labels"]...)
			write([]any{})
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/issues/12/labels/") && r.Method == http.MethodDelete:
			f.labelRemoved = strings.TrimPrefix(r.URL.Path, "/repos/o/r/issues/12/labels/")
			write([]any{})
		case r.URL.Path == "/repos/o/r/issues/12" && r.Method == http.MethodPatch:
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.closed = in["state"] == "closed"
			write(map[string]any{})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
}

func itoa(n int) string { return strconv.Itoa(n) }

func newSource(t *testing.T) (*Source, *fakeAPI) {
	t.Helper()
	tr := true
	return newSourceWith(t, &tr, false)
}

// newSourceWith builds a source with an explicit or unset (nil) comments
// setting against a private or public fake repository.
func newSourceWith(t *testing.T, comments *bool, private bool) (*Source, *fakeAPI) {
	t.Helper()
	f := &fakeAPI{private: private}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_TOKEN", "x")
	cfg := config.SourceConfig{Name: "gh", Type: "github", Repo: "o/r", Labels: []string{"ready"}, Claim: true, ClaimLabel: "loop:in-progress"}
	cfg.Comments = comments
	return New(cfg, 1), f
}

func TestCommentsDefaultDependsOnVisibility(t *testing.T) {
	tr, fl := true, false
	cases := []struct {
		name     string
		comments *bool
		private  bool
		want     int
	}{
		{"unset on a public repository", nil, false, 0},
		{"unset on a private repository", nil, true, 1},
		{"explicit true on a public repository", &tr, false, 1},
		{"explicit false on a private repository", &fl, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newSourceWith(t, c.comments, c.private)
			it, err := s.Get(context.Background(), "12")
			if err != nil {
				t.Fatal(err)
			}
			if len(it.Comments) != c.want {
				t.Errorf("got %d comments, want %d (%s)", len(it.Comments), c.want, s.CommentsReason(context.Background()))
			}
		})
	}
}

func TestListFiltersAndOrders(t *testing.T) {
	s, f := newSource(t)
	items, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.listedLabels != "ready" {
		t.Errorf("labels filter not sent, got %q", f.listedLabels)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (the pull request must be skipped)", len(items))
	}
	if items[0].ID != "gh:12" || items[0].Title != "Add auth" || items[0].SourceIndex != 1 {
		t.Errorf("first item = %+v", items[0])
	}
	if items[0].DependsOn[0] != "#7" {
		t.Errorf("depends on not parsed: %v", items[0].DependsOn)
	}
	if !items[1].InProgress {
		t.Errorf("claim label should mark the item in progress")
	}
	if items[0].Comments != nil {
		t.Errorf("List must not load comments")
	}
}

func TestGetWithComments(t *testing.T) {
	s, _ := newSource(t)
	it, err := s.Get(context.Background(), "12")
	if err != nil {
		t.Fatal(err)
	}
	if it.Model != "claude-opus-5" || it.URL != "https://github.com/o/r/issues/12" {
		t.Errorf("item = %+v", it)
	}
	if len(it.Comments) != 1 || it.Comments[0].Author != "bob" {
		t.Errorf("comments = %+v (claim marker must be filtered out)", it.Comments)
	}
	if it.ClaimedBy != "run-9" {
		t.Errorf("claimed by = %q", it.ClaimedBy)
	}
}

func TestResolve(t *testing.T) {
	s, _ := newSource(t)
	for ref, want := range map[string]string{
		"#12": "12", "GH-12": "12", "o/r#12": "12", "https://github.com/o/r/issues/12": "12", "https://github.com/o/r/pull/9": "9",
	} {
		if got, ok := s.Resolve(ref); !ok || got != want {
			t.Errorf("Resolve(%q) = %q,%v want %q", ref, got, ok, want)
		}
	}
	for _, ref := range []string{"PROJ-1", "other/repo#12", "auth.md", "12"} {
		if _, ok := s.Resolve(ref); ok {
			t.Errorf("Resolve(%q) should not match", ref)
		}
	}
}

func TestClaimReleaseClose(t *testing.T) {
	s, f := newSource(t)
	it, _ := s.Get(context.Background(), "12")
	if err := s.Claim(context.Background(), it, "run-1"); err != nil {
		t.Fatal(err)
	}
	if len(f.labelsAdded) != 1 || f.labelsAdded[0] != "loop:in-progress" || len(f.comments) != 1 || !strings.Contains(f.comments[0], claimMarker+" run-1") {
		t.Errorf("claim: labels=%v comments=%v", f.labelsAdded, f.comments)
	}
	if err := s.Release(context.Background(), it); err != nil {
		t.Fatal(err)
	}
	if f.labelRemoved != "loop:in-progress" {
		t.Errorf("release removed %q", f.labelRemoved)
	}
	if err := s.Close(context.Background(), it, "merged"); err != nil {
		t.Fatal(err)
	}
	if !f.closed || f.comments[len(f.comments)-1] != "merged" {
		t.Errorf("close: closed=%v comments=%v", f.closed, f.comments)
	}
}
