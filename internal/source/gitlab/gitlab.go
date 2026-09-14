// Package gitlab implements a backlog source backed by GitLab issues.
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/glapi"
	"github.com/christoph-jerolimov/loop/internal/host"
	"github.com/christoph-jerolimov/loop/internal/item"
)

// Source lists issues of one project.
type Source struct {
	cfg   config.SourceConfig
	index int
	api   *glapi.Client

	// perms caches member access lookups by user id for the writers
	// policy; permErr remembers a forbidden lookup so the API is not asked
	// again for every comment.
	perms   map[int64]int
	permErr error
}

// New creates the source; the client is created lazily so listing local
// sources works without a token.
func New(cfg config.SourceConfig, index int) *Source {
	return &Source{cfg: cfg, index: index}
}

func (s *Source) Name() string            { return s.cfg.Name }
func (s *Source) Type() string            { return "gitlab" }
func (s *Source) SupportsAutoClose() bool { return true }

func (s *Source) client() (*glapi.Client, error) {
	if s.api == nil {
		c, err := glapi.New(s.cfg.URL, s.cfg.Repo)
		if err != nil {
			return nil, err
		}
		s.api = c
	}
	return s.api, nil
}

func (s *Source) convert(ctx context.Context, is *glapi.Issue, withComments bool) (*item.Item, error) {
	native := strconv.Itoa(is.IID)
	it := &item.Item{
		ID:          item.MakeID(s.cfg.Name, native),
		NativeID:    native,
		Source:      s.cfg.Name,
		SourceType:  "gitlab",
		SourceIndex: s.index,
		Title:       is.Title,
		Body:        is.Description,
		URL:         is.WebURL,
		Created:     is.CreatedAt,
		Closed:      is.State == "closed",
		Labels:      is.Labels,
		Extra:       map[string]string{"id": strconv.FormatInt(is.ID, 10), "repo": s.cfg.Repo, "number": native},
	}
	for _, l := range is.Labels {
		if p := item.PriorityFromLabel(l); p != 0 && (it.Priority == 0 || p < it.Priority) {
			it.Priority = p
		}
	}
	if s.cfg.ClaimLabel != "" && it.HasLabel(s.cfg.ClaimLabel) {
		it.InProgress = true
	}
	if withComments {
		api, err := s.client()
		if err != nil {
			return nil, err
		}
		notes, err := api.ListNotes(ctx, glapi.Issues, is.IID)
		if err != nil {
			return nil, err
		}
		for _, n := range notes {
			if s.cfg.ClaimLabel != "" && strings.HasPrefix(n.Body, claimMarker) {
				it.ClaimedBy = strings.TrimSpace(strings.TrimPrefix(strings.SplitN(n.Body, "\n", 2)[0], claimMarker))
				continue
			}
			if s.policy() == config.CommentsWriters && !s.canPush(ctx, api, n.Author.ID) {
				continue
			}
			it.Comments = append(it.Comments, item.Comment{Author: n.Author.Username, Body: n.Body, Created: n.CreatedAt, URL: fmt.Sprintf("%s#note_%d", is.WebURL, n.ID)})
		}
	}
	it.ApplyBodyDirectives()
	return it, nil
}

const claimMarker = "<!-- loop:run -->"

// List returns open issues carrying every configured label.
func (s *Source) List(ctx context.Context) ([]*item.Item, error) {
	api, err := s.client()
	if err != nil {
		return nil, err
	}
	issues, err := api.ListIssues(ctx, s.cfg.Labels)
	if err != nil {
		return nil, err
	}
	var out []*item.Item
	for i := range issues {
		it, err := s.convert(ctx, &issues[i], false)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	item.Order(out)
	return out, nil
}

// Get fetches one issue with its comments.
func (s *Source) Get(ctx context.Context, native string) (*item.Item, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(native, "#"))
	if err != nil {
		return nil, fmt.Errorf("gitlab issue id must be a number, got %q", native)
	}
	api, err := s.client()
	if err != nil {
		return nil, err
	}
	is, err := api.GetIssue(ctx, n)
	if err != nil {
		return nil, err
	}
	return s.convert(ctx, is, s.policy().Loaded())
}

// policy returns the comments policy, writers when loop.yaml left it unset.
func (s *Source) policy() config.CommentsPolicy {
	if s.cfg.Comments == "" {
		return config.CommentsWriters
	}
	return s.cfg.Comments
}

// canPush reports whether a note author may steer the agent under the
// writers policy: a member (direct or inherited) with developer access or
// more. The lookup needs at least reporter access for loop's own token.
func (s *Source) canPush(ctx context.Context, api *glapi.Client, userID int64) bool {
	if lvl, ok := s.perms[userID]; ok {
		return host.CanPush(host.AccessName(lvl))
	}
	if s.permErr != nil {
		return false
	}
	lvl, err := api.MemberAccessLevel(ctx, userID)
	if err != nil {
		var ge *glapi.Error
		if errors.As(err, &ge) && (ge.Status == 403 || ge.Status == 401) {
			s.permErr = err
		}
		return false
	}
	if s.perms == nil {
		s.perms = map[int64]int{}
	}
	s.perms[userID] = lvl
	return host.CanPush(host.AccessName(lvl))
}

// CommentsReason explains the effective comments setting for loop doctor.
func (s *Source) CommentsReason(ctx context.Context) string {
	switch s.policy() {
	case config.CommentsNone:
		return "not loaded (comments: none)"
	case config.CommentsAll:
		return "loaded from everyone (comments: all)"
	}
	if api, err := s.client(); err == nil {
		if p, err := api.GetProject(ctx); err == nil && p.AccessLevel() < glapi.AccessReporter {
			return "not loaded (comments: writers, but the token is no member of the project, so member access cannot be looked up)"
		}
	}
	return "loaded from members with developer access or more (comments: writers)"
}

var (
	shortRe = regexp.MustCompile(`^#(\d+)$`)
	fullRe  = regexp.MustCompile(`^([\w.-]+(?:/[\w.-]+)+)#(\d+)$`)
	urlRe   = regexp.MustCompile(`^https?://[^/]+/(.+?)/-/(?:issues|merge_requests)/(\d+)`)
	glRe    = regexp.MustCompile(`^(?i:gl-)(\d+)$`)
)

// Resolve accepts "#12", "GL-12", "group/project#12" and issue URLs of
// this project.
func (s *Source) Resolve(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if m := shortRe.FindStringSubmatch(ref); m != nil {
		return m[1], true
	}
	if m := glRe.FindStringSubmatch(ref); m != nil {
		return m[1], true
	}
	if m := fullRe.FindStringSubmatch(ref); m != nil && strings.EqualFold(m[1], s.cfg.Repo) {
		return m[2], true
	}
	if m := urlRe.FindStringSubmatch(ref); m != nil && strings.EqualFold(m[1], s.cfg.Repo) {
		return m[2], true
	}
	return "", false
}

func number(it *item.Item) int {
	n, _ := strconv.Atoi(it.NativeID)
	return n
}

// Claim adds the claim label and a marker note with the run id.
func (s *Source) Claim(ctx context.Context, it *item.Item, runID string) error {
	if !s.cfg.Claim {
		return nil
	}
	api, err := s.client()
	if err != nil {
		return err
	}
	if err := api.AddLabels(ctx, number(it), []string{s.cfg.ClaimLabel}); err != nil {
		return err
	}
	return api.CreateNote(ctx, glapi.Issues, number(it), fmt.Sprintf("%s %s\n\nloop picked this up in run `%s`.", claimMarker, runID, runID))
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
	return api.RemoveLabel(ctx, number(it), s.cfg.ClaimLabel)
}

// Close closes the issue and drops the claim label.
func (s *Source) Close(ctx context.Context, it *item.Item, message string) error {
	api, err := s.client()
	if err != nil {
		return err
	}
	if message != "" {
		if err := api.CreateNote(ctx, glapi.Issues, number(it), message); err != nil {
			return err
		}
	}
	if s.cfg.ClaimLabel != "" {
		_ = api.RemoveLabel(ctx, number(it), s.cfg.ClaimLabel)
	}
	return api.CloseIssue(ctx, number(it))
}

// Comment posts a note on the issue.
func (s *Source) Comment(ctx context.Context, it *item.Item, body string) error {
	api, err := s.client()
	if err != nil {
		return err
	}
	return api.CreateNote(ctx, glapi.Issues, number(it), body)
}
