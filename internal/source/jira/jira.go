// Package jira implements a backlog source backed by a JQL query.
package jira

import (
	"context"
	"regexp"
	"strings"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/jiraapi"
)

// Source lists Jira issues.
type Source struct {
	cfg   config.SourceConfig
	index int
	api   *jiraapi.Client
}

// New creates the source.
func New(cfg config.SourceConfig, index int) *Source {
	return &Source{cfg: cfg, index: index}
}

func (s *Source) Name() string            { return s.cfg.Name }
func (s *Source) Type() string            { return "jira" }
func (s *Source) SupportsAutoClose() bool { return false }

func (s *Source) client() (*jiraapi.Client, error) {
	if s.api == nil {
		c, err := jiraapi.New(s.cfg.URL)
		if err != nil {
			return nil, err
		}
		s.api = c
	}
	return s.api, nil
}

func (s *Source) convert(is *jiraapi.Issue) *item.Item {
	it := &item.Item{
		ID:          item.MakeID(s.cfg.Name, is.Key),
		NativeID:    is.Key,
		Source:      s.cfg.Name,
		SourceType:  "jira",
		SourceIndex: s.index,
		Title:       is.Fields.Summary,
		Body:        is.Fields.Description,
		URL:         strings.TrimRight(s.cfg.URL, "/") + "/browse/" + is.Key,
		Labels:      is.Fields.Labels,
		Created:     jiraapi.ParseTime(is.Fields.Created),
		Closed:      is.Fields.Status.StatusCategory.Key == "done",
		Extra:       map[string]string{"status": is.Fields.Status.Name},
	}
	if s.cfg.ClaimLabel != "" && it.HasLabel(s.cfg.ClaimLabel) {
		it.InProgress = true
	}
	if ip := s.cfg.Transitions["in_progress"]; ip != "" && strings.EqualFold(is.Fields.Status.Name, ip) {
		it.InProgress = true
	}
	// Native links: this issue "is blocked by" / "depends on" another.
	for _, l := range is.Fields.IssueLinks {
		if l.InwardIssue == nil {
			continue
		}
		name := strings.ToLower(l.Type.Name + " " + l.Type.Inward)
		if strings.Contains(name, "block") || strings.Contains(name, "depend") {
			it.DependsOn = append(it.DependsOn, l.InwardIssue.Key)
		}
	}
	if s.cfg.Comments == nil || *s.cfg.Comments {
		for _, cm := range is.Fields.Comment.Comments {
			if strings.HasPrefix(cm.Body, claimPrefix) {
				it.ClaimedBy = strings.TrimSpace(strings.TrimPrefix(strings.SplitN(cm.Body, "\n", 2)[0], claimPrefix))
				continue
			}
			it.Comments = append(it.Comments, item.Comment{Author: cm.Author.DisplayName, Body: cm.Body, Created: jiraapi.ParseTime(cm.Created)})
		}
	}
	it.ApplyBodyDirectives()
	return it
}

const claimPrefix = "loop run:"

// List runs the JQL query and returns non-done issues carrying every label.
func (s *Source) List(ctx context.Context) ([]*item.Item, error) {
	api, err := s.client()
	if err != nil {
		return nil, err
	}
	jql := s.cfg.JQL
	for _, l := range s.cfg.Labels {
		jql = "(" + jql + ") AND labels = \"" + l + "\""
	}
	issues, err := api.Search(ctx, jql)
	if err != nil {
		return nil, err
	}
	var out []*item.Item
	for i := range issues {
		it := s.convert(&issues[i])
		if !it.Closed {
			out = append(out, it)
		}
	}
	item.Order(out)
	return out, nil
}

// Get fetches one issue by key.
func (s *Source) Get(ctx context.Context, native string) (*item.Item, error) {
	api, err := s.client()
	if err != nil {
		return nil, err
	}
	is, err := api.GetIssue(ctx, strings.ToUpper(native))
	if err != nil {
		return nil, err
	}
	return s.convert(is), nil
}

var keyRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*-\d+)$`)
var browseRe = regexp.MustCompile(`/browse/([A-Za-z][A-Za-z0-9_]*-\d+)`)

// Resolve accepts "PROJ-12" and browse URLs of this site.
func (s *Source) Resolve(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if m := keyRe.FindStringSubmatch(ref); m != nil {
		return strings.ToUpper(m[1]), true
	}
	if strings.HasPrefix(strings.ToLower(ref), strings.ToLower(strings.TrimRight(s.cfg.URL, "/"))) {
		if m := browseRe.FindStringSubmatch(ref); m != nil {
			return strings.ToUpper(m[1]), true
		}
	}
	return "", false
}

// Claim adds the claim label, a marker comment, and runs the in_progress
// transition when configured.
func (s *Source) Claim(ctx context.Context, it *item.Item, runID string) error {
	if !s.cfg.Claim {
		return nil
	}
	api, err := s.client()
	if err != nil {
		return err
	}
	if err := api.AddLabel(ctx, it.NativeID, s.cfg.ClaimLabel); err != nil {
		return err
	}
	if err := api.AddComment(ctx, it.NativeID, claimPrefix+" "+runID); err != nil {
		return err
	}
	if t := s.cfg.Transitions["in_progress"]; t != "" {
		return api.DoTransition(ctx, it.NativeID, t)
	}
	return nil
}

// Release removes the claim label.
func (s *Source) Release(ctx context.Context, it *item.Item) error {
	if !s.cfg.Claim {
		return nil
	}
	api, err := s.client()
	if err != nil {
		return err
	}
	return api.RemoveLabel(ctx, it.NativeID, s.cfg.ClaimLabel)
}

// Close runs the done transition (default "Done").
func (s *Source) Close(ctx context.Context, it *item.Item, message string) error {
	api, err := s.client()
	if err != nil {
		return err
	}
	if message != "" {
		if err := api.AddComment(ctx, it.NativeID, message); err != nil {
			return err
		}
	}
	if s.cfg.ClaimLabel != "" {
		_ = api.RemoveLabel(ctx, it.NativeID, s.cfg.ClaimLabel)
	}
	t := s.cfg.Transitions["done"]
	if t == "" {
		t = "Done"
	}
	return api.DoTransition(ctx, it.NativeID, t)
}

// Comment posts a comment.
func (s *Source) Comment(ctx context.Context, it *item.Item, body string) error {
	api, err := s.client()
	if err != nil {
		return err
	}
	return api.AddComment(ctx, it.NativeID, body)
}
