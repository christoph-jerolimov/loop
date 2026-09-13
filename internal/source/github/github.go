// Package github implements a backlog source backed by GitHub issues.
package github

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/christoph-jerolimov/loop/internal/config"
	"github.com/christoph-jerolimov/loop/internal/ghapi"
	"github.com/christoph-jerolimov/loop/internal/item"
)

// Source lists issues of one repository.
type Source struct {
	cfg   config.SourceConfig
	index int
	api   *ghapi.Client

	// perms caches collaborator permission lookups by login for the
	// writers policy; permErr remembers a forbidden lookup so the API is
	// not asked again for every comment.
	perms   map[string]string
	permErr error
}

// New creates the source; the client is created lazily so listing local
// sources works without a token.
func New(cfg config.SourceConfig, index int) *Source {
	return &Source{cfg: cfg, index: index}
}

func (s *Source) Name() string            { return s.cfg.Name }
func (s *Source) Type() string            { return "github" }
func (s *Source) SupportsAutoClose() bool { return true }

func (s *Source) client() (*ghapi.Client, error) {
	if s.api == nil {
		c, err := ghapi.New(s.cfg.Repo)
		if err != nil {
			return nil, err
		}
		s.api = c
	}
	return s.api, nil
}

func (s *Source) convert(ctx context.Context, is *ghapi.Issue, withComments bool) (*item.Item, error) {
	native := strconv.Itoa(is.Number)
	it := &item.Item{
		ID:          item.MakeID(s.cfg.Name, native),
		NativeID:    native,
		Source:      s.cfg.Name,
		SourceType:  "github",
		SourceIndex: s.index,
		Title:       is.Title,
		Body:        is.Body,
		URL:         is.HTMLURL,
		Created:     is.CreatedAt,
		Closed:      is.State == "closed",
		Extra:       map[string]string{"node_id": is.NodeID, "repo": s.cfg.Repo, "number": native},
	}
	for _, l := range is.Labels {
		it.Labels = append(it.Labels, l.Name)
	}
	if s.cfg.ClaimLabel != "" && it.HasLabel(s.cfg.ClaimLabel) {
		it.InProgress = true
	}
	if withComments {
		api, err := s.client()
		if err != nil {
			return nil, err
		}
		cms, err := api.ListIssueComments(ctx, is.Number)
		if err != nil {
			return nil, err
		}
		for _, cm := range cms {
			if s.cfg.ClaimLabel != "" && strings.HasPrefix(cm.Body, claimMarker) {
				it.ClaimedBy = strings.TrimSpace(strings.TrimPrefix(strings.SplitN(cm.Body, "\n", 2)[0], claimMarker))
				continue
			}
			if s.policy() == config.CommentsWriters && !s.trusted(ctx, api, cm) {
				continue
			}
			it.Comments = append(it.Comments, item.Comment{Author: cm.User.Login, Body: cm.Body, Created: cm.CreatedAt, URL: cm.HTMLURL})
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
		return nil, fmt.Errorf("github issue id must be a number, got %q", native)
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

// trusted reports whether a comment author may steer the agent under the
// writers policy: the repository owner, or a member or collaborator whose
// permission on the repository allows pushing. The author association on
// the comment settles the clear cases; only members and collaborators need
// a permission lookup, which requires push access for loop's own token.
func (s *Source) trusted(ctx context.Context, api *ghapi.Client, cm ghapi.Comment) bool {
	switch cm.AuthorAssociation {
	case "OWNER":
		return true
	case "MEMBER", "COLLABORATOR", "":
		return s.canPush(ctx, api, cm.User.Login)
	default: // CONTRIBUTOR, FIRST_TIME_CONTRIBUTOR, FIRST_TIMER, MANNEQUIN, NONE
		return false
	}
}

func (s *Source) canPush(ctx context.Context, api *ghapi.Client, login string) bool {
	if p, ok := s.perms[login]; ok {
		return ghapi.CanPush(p)
	}
	if s.permErr != nil {
		return false
	}
	p, err := api.CollaboratorPermission(ctx, login)
	if err != nil {
		// A 403 means the token cannot look up permissions at all (no push
		// access); remember it. Anything else may be transient, so the next
		// comment tries again.
		var ge *ghapi.Error
		if errors.As(err, &ge) && ge.Status == 403 {
			s.permErr = err
		}
		return false
	}
	if s.perms == nil {
		s.perms = map[string]string{}
	}
	s.perms[login] = p
	return ghapi.CanPush(p)
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
		if repo, err := api.GetRepository(ctx); err == nil && !repo.Permissions.Push {
			return "loaded from the repository owner only (comments: writers, but the token has no push access, so collaborator permissions cannot be looked up)"
		}
	}
	return "loaded from the repository owner and collaborators with write access (comments: writers)"
}

var (
	shortRe = regexp.MustCompile(`^#(\d+)$`)
	fullRe  = regexp.MustCompile(`^([\w.-]+/[\w.-]+)#(\d+)$`)
	urlRe   = regexp.MustCompile(`^https?://[^/]+/([\w.-]+/[\w.-]+)/(?:issues|pull)/(\d+)`)
	ghRe    = regexp.MustCompile(`^(?i:gh-)(\d+)$`)
)

// Resolve accepts "#12", "GH-12", "owner/repo#12" and issue URLs of this repo.
func (s *Source) Resolve(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if m := shortRe.FindStringSubmatch(ref); m != nil {
		return m[1], true
	}
	if m := ghRe.FindStringSubmatch(ref); m != nil {
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

// Claim adds the claim label and a marker comment with the run id.
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
	return api.CreateComment(ctx, number(it), fmt.Sprintf("%s %s\n\nloop picked this up in run `%s`.", claimMarker, runID, runID))
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
		if err := api.CreateComment(ctx, number(it), message); err != nil {
			return err
		}
	}
	if s.cfg.ClaimLabel != "" {
		_ = api.RemoveLabel(ctx, number(it), s.cfg.ClaimLabel)
	}
	return api.CloseIssue(ctx, number(it))
}

// Comment posts an issue comment.
func (s *Source) Comment(ctx context.Context, it *item.Item, body string) error {
	api, err := s.client()
	if err != nil {
		return err
	}
	return api.CreateComment(ctx, number(it), body)
}
