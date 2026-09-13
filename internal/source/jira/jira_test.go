package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/jiraapi"
)

type fakeJira struct {
	legacy      bool // no /search/jql: an older Jira Server
	jql         string
	transitions []string
	labelOps    []string
	comments    []string
}

func (f *fakeJira) handler(t *testing.T) http.Handler {
	issue := func(key, summary, status, category string, links ...map[string]any) map[string]any {
		if links == nil {
			links = []map[string]any{}
		}
		return map[string]any{"id": "1", "key": key, "fields": map[string]any{
			"summary": summary, "description": "Body of " + key + "\n\nDepends on: ABC-1", "labels": []string{"agent"}, "created": "2026-01-05T10:00:00.000+0000",
			"status":     map[string]any{"name": status, "statusCategory": map[string]any{"key": category}},
			"issuelinks": links,
			"comment": map[string]any{"comments": []any{
				map[string]any{"id": "10", "body": "ping", "author": map[string]any{"displayName": "Ann"}, "created": "2026-01-06T10:00:00.000+0000"},
				map[string]any{"id": "11", "body": claimPrefix + " run-3", "author": map[string]any{"displayName": "loop"}, "created": "2026-01-07T10:00:00.000+0000"},
			}},
		}}
	}
	blocked := map[string]any{"type": map[string]any{"name": "Blocks", "inward": "is blocked by", "outward": "blocks"}, "inwardIssue": map[string]any{"key": "ABC-2"}}
	outward := map[string]any{"type": map[string]any{"name": "Blocks", "inward": "is blocked by", "outward": "blocks"}, "outwardIssue": map[string]any{"key": "ABC-9"}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case r.URL.Path == "/rest/api/2/myself":
			write(map[string]any{"displayName": "Ann Admin", "name": "ann"})
		case r.URL.Path == "/rest/api/2/search/jql" && f.legacy:
			w.WriteHeader(404)
			write(map[string]any{"errorMessages": []string{"no such endpoint"}})
		case r.URL.Path == "/rest/api/2/search" && f.legacy:
			f.jql = r.URL.Query().Get("jql")
			// Two pages of one issue each.
			if r.URL.Query().Get("startAt") == "0" {
				write(map[string]any{"issues": []any{issue("ABC-7", "Rate limit", "To Do", "new")}, "total": 2, "startAt": 0})
			} else {
				write(map[string]any{"issues": []any{issue("ABC-8", "Second", "To Do", "new")}, "total": 2, "startAt": 1})
			}
		case r.URL.Path == "/rest/api/2/search/jql":
			f.jql = r.URL.Query().Get("jql")
			write(map[string]any{"issues": []any{issue("ABC-7", "Rate limit", "To Do", "new", blocked, outward), issue("ABC-8", "Done thing", "Done", "done")}, "isLast": true})
		case r.URL.Path == "/rest/api/2/issue/ABC-7" && r.Method == http.MethodGet:
			write(issue("ABC-7", "Rate limit", "To Do", "new", blocked))
		case r.URL.Path == "/rest/api/2/issue/ABC-7" && r.Method == http.MethodPut:
			var in struct {
				Update struct {
					Labels []map[string]string `json:"labels"`
				} `json:"update"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			for _, op := range in.Update.Labels {
				for k, v := range op {
					f.labelOps = append(f.labelOps, k+":"+v)
				}
			}
			w.WriteHeader(204)
		case r.URL.Path == "/rest/api/2/issue/ABC-7/comment":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.comments = append(f.comments, in["body"])
			write(map[string]any{})
		case r.URL.Path == "/rest/api/2/issue/ABC-7/transitions" && r.Method == http.MethodGet:
			write(map[string]any{"transitions": []any{
				map[string]any{"id": "21", "name": "Start progress", "to": map[string]any{"name": "In Progress"}},
				map[string]any{"id": "31", "name": "Done", "to": map[string]any{"name": "Done"}},
			}})
		case r.URL.Path == "/rest/api/2/issue/ABC-7/transitions" && r.Method == http.MethodPost:
			var in struct {
				Transition map[string]string `json:"transition"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.transitions = append(f.transitions, in.Transition["id"])
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
}

func newSource(t *testing.T) (*Source, *fakeJira) {
	t.Helper()
	f := &fakeJira{}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	t.Setenv("JIRA_EMAIL", "me@example.com")
	t.Setenv("JIRA_API_TOKEN", "x")
	cfg := config.SourceConfig{Name: "jira", Type: "jira", URL: srv.URL, JQL: "project = ABC", Labels: []string{"agent"}, Claim: true, ClaimLabel: "loop-in-progress",
		Transitions: map[string]string{"in_progress": "In Progress", "done": "Done"}}
	cfg.Comments = config.CommentsAll
	return New(cfg, 2), f
}

func TestListAndConvert(t *testing.T) {
	s, f := newSource(t)
	items, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.jql, `labels = "agent"`) {
		t.Errorf("labels not appended to JQL: %q", f.jql)
	}
	if len(items) != 1 || items[0].ID != "jira:ABC-7" {
		t.Fatalf("items = %+v (done issues must be skipped)", items)
	}
	it := items[0]
	if it.Title != "Rate limit" || it.SourceIndex != 2 || !strings.HasSuffix(it.URL, "/browse/ABC-7") || it.Created.IsZero() {
		t.Errorf("item = %+v", it)
	}
	if strings.Join(it.DependsOn, ",") != "ABC-2,ABC-1" {
		t.Errorf("depends on = %v (inward blocked-by link plus body line, outward links ignored)", it.DependsOn)
	}
	if len(it.Comments) != 1 || it.Comments[0].Author != "Ann" || it.ClaimedBy != "run-3" {
		t.Errorf("comments = %+v claimed by %q", it.Comments, it.ClaimedBy)
	}
}

func TestResolve(t *testing.T) {
	s, _ := newSource(t)
	srvURL := s.cfg.URL
	for ref, want := range map[string]string{"ABC-7": "ABC-7", "abc-7": "ABC-7", srvURL + "/browse/ABC-7": "ABC-7"} {
		if got, ok := s.Resolve(ref); !ok || got != want {
			t.Errorf("Resolve(%q) = %q,%v want %q", ref, got, ok, want)
		}
	}
	for _, ref := range []string{"#12", "auth.md", "https://other.atlassian.net/browse/ABC-7", "7"} {
		if _, ok := s.Resolve(ref); ok {
			t.Errorf("Resolve(%q) should not match", ref)
		}
	}
}

func TestClaimAndClose(t *testing.T) {
	s, f := newSource(t)
	it, err := s.Get(context.Background(), "abc-7")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(context.Background(), it, "run-1"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.labelOps, ",") != "add:loop-in-progress" || len(f.comments) != 1 || f.comments[0] != claimPrefix+" run-1" || strings.Join(f.transitions, ",") != "21" {
		t.Errorf("claim: labels=%v comments=%v transitions=%v", f.labelOps, f.comments, f.transitions)
	}
	if err := s.Close(context.Background(), it, "merged"); err != nil {
		t.Fatal(err)
	}
	if f.labelOps[len(f.labelOps)-1] != "remove:loop-in-progress" || f.transitions[len(f.transitions)-1] != "31" || f.comments[len(f.comments)-1] != "merged" {
		t.Errorf("close: labels=%v comments=%v transitions=%v", f.labelOps, f.comments, f.transitions)
	}
}

func TestReleaseAndComment(t *testing.T) {
	s, f := newSource(t)
	it, err := s.Get(context.Background(), "ABC-7")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Release(context.Background(), it); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.labelOps, ",") != "remove:loop-in-progress" {
		t.Errorf("release label ops = %v", f.labelOps)
	}
	if err := s.Comment(context.Background(), it, "opened PR"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.comments, ",") != "opened PR" {
		t.Errorf("comments = %v", f.comments)
	}
	// Jira tickets are closed by loop through a transition, never by GitHub.
	if s.Name() != "jira" || s.Type() != "jira" || s.SupportsAutoClose() {
		t.Errorf("identity: %s %s %v", s.Name(), s.Type(), s.SupportsAutoClose())
	}

	// Without claiming, Release touches nothing.
	s2, f2 := newSource(t)
	s2.cfg.Claim = false
	if err := s2.Release(context.Background(), it); err != nil || len(f2.labelOps) != 0 {
		t.Errorf("release without claim: err=%v ops=%v", err, f2.labelOps)
	}
}

func TestLegacySearchPagesThroughOlderServers(t *testing.T) {
	s, f := newSource(t)
	f.legacy = true
	items, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].NativeID != "ABC-7" || items[1].NativeID != "ABC-8" || !strings.Contains(f.jql, "project = ABC") {
		t.Errorf("legacy search: %d items, jql %q", len(items), f.jql)
	}
}

func TestClientMyselfAndBrowseURL(t *testing.T) {
	s, _ := newSource(t)
	c, err := jiraapi.New(s.cfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	if who, err := c.Myself(context.Background()); err != nil || who != "Ann Admin" {
		t.Errorf("Myself = %q, %v", who, err)
	}
	if got := c.BrowseURL("ABC-7"); got != s.cfg.URL+"/browse/ABC-7" {
		t.Errorf("BrowseURL = %s", got)
	}
}
