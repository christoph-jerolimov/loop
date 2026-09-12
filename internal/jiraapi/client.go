// Package jiraapi is a minimal Jira Cloud/Server REST client (API v2 for
// plain-text descriptions).
package jiraapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client talks to one Jira site.
type Client struct {
	BaseURL string
	email   string
	token   string
	HTTP    *http.Client
}

// New creates a client. Credentials come from JIRA_EMAIL + JIRA_API_TOKEN
// (basic auth, Jira Cloud) or JIRA_TOKEN (bearer, Jira Server/DC).
func New(baseURL string) (*Client, error) {
	c := &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 60 * time.Second}}
	c.email = os.Getenv("JIRA_EMAIL")
	c.token = os.Getenv("JIRA_API_TOKEN")
	if c.token == "" {
		c.token = os.Getenv("JIRA_TOKEN")
	}
	if c.token == "" {
		return nil, errors.New("no Jira credentials: set JIRA_EMAIL and JIRA_API_TOKEN (cloud) or JIRA_TOKEN (server)")
	}
	return c, nil
}

// Error is a non-2xx response.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string { return fmt.Sprintf("jira: HTTP %d: %s", e.Status, e.Body) }

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	if c.email != "" {
		req.SetBasicAuth(c.email, c.token)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Issue is the subset of Jira issue fields loop uses.
type Issue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Self   string `json:"self"`
	Fields struct {
		Summary     string   `json:"summary"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
		Created     string   `json:"created"`
		Status      struct {
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"` // new, indeterminate, done
			} `json:"statusCategory"`
		} `json:"status"`
		IssueLinks []struct {
			Type struct {
				Name    string `json:"name"`
				Inward  string `json:"inward"`
				Outward string `json:"outward"`
			} `json:"type"`
			InwardIssue *struct {
				Key string `json:"key"`
			} `json:"inwardIssue"`
			OutwardIssue *struct {
				Key string `json:"key"`
			} `json:"outwardIssue"`
		} `json:"issuelinks"`
		Comment struct {
			Comments []Comment `json:"comments"`
		} `json:"comment"`
	} `json:"fields"`
}

// Comment is a Jira comment.
type Comment struct {
	ID     string `json:"id"`
	Body   string `json:"body"`
	Author struct {
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Created string `json:"created"`
}

// Created parses the Jira timestamp.
func ParseTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05.000-0700", time.RFC3339, "2006-01-02T15:04:05.000Z0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

const fields = "summary,description,labels,created,status,issuelinks,comment"

// Search runs a JQL query and returns every matching issue.
func (c *Client) Search(ctx context.Context, jql string) ([]Issue, error) {
	var out []Issue
	next := ""
	for i := 0; i < 50; i++ {
		q := url.Values{"jql": {jql}, "fields": {fields}, "maxResults": {"100"}}
		if next != "" {
			q.Set("nextPageToken", next)
		}
		var resp struct {
			Issues        []Issue `json:"issues"`
			NextPageToken string  `json:"nextPageToken"`
			IsLast        bool    `json:"isLast"`
			// legacy /search response
			Total   int `json:"total"`
			StartAt int `json:"startAt"`
		}
		err := c.do(ctx, http.MethodGet, "/rest/api/2/search/jql?"+q.Encode(), nil, &resp)
		var je *Error
		if errors.As(err, &je) && je.Status == 404 {
			// Older Jira Server without /search/jql.
			return c.searchLegacy(ctx, jql)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, resp.Issues...)
		if resp.IsLast || resp.NextPageToken == "" {
			break
		}
		next = resp.NextPageToken
	}
	return out, nil
}

func (c *Client) searchLegacy(ctx context.Context, jql string) ([]Issue, error) {
	var out []Issue
	for start := 0; start < 5000; {
		q := url.Values{"jql": {jql}, "fields": {fields}, "maxResults": {"100"}, "startAt": {fmt.Sprint(start)}}
		var resp struct {
			Issues []Issue `json:"issues"`
			Total  int     `json:"total"`
		}
		if err := c.do(ctx, http.MethodGet, "/rest/api/2/search?"+q.Encode(), nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Issues...)
		start += len(resp.Issues)
		if len(resp.Issues) == 0 || start >= resp.Total {
			break
		}
	}
	return out, nil
}

// GetIssue fetches one issue by key.
func (c *Client) GetIssue(ctx context.Context, key string) (*Issue, error) {
	var is Issue
	if err := c.do(ctx, http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(key)+"?fields="+fields, nil, &is); err != nil {
		return nil, err
	}
	return &is, nil
}

// AddComment posts a comment.
func (c *Client) AddComment(ctx context.Context, key, body string) error {
	return c.do(ctx, http.MethodPost, "/rest/api/2/issue/"+url.PathEscape(key)+"/comment", map[string]string{"body": body}, nil)
}

// AddLabel adds a label.
func (c *Client) AddLabel(ctx context.Context, key, label string) error {
	in := map[string]any{"update": map[string]any{"labels": []map[string]string{{"add": label}}}}
	return c.do(ctx, http.MethodPut, "/rest/api/2/issue/"+url.PathEscape(key), in, nil)
}

// RemoveLabel removes a label.
func (c *Client) RemoveLabel(ctx context.Context, key, label string) error {
	in := map[string]any{"update": map[string]any{"labels": []map[string]string{{"remove": label}}}}
	return c.do(ctx, http.MethodPut, "/rest/api/2/issue/"+url.PathEscape(key), in, nil)
}

// Transition is an available workflow transition.
type Transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   struct {
		Name string `json:"name"`
	} `json:"to"`
}

// Transitions lists the transitions currently available for an issue.
func (c *Client) Transitions(ctx context.Context, key string) ([]Transition, error) {
	var resp struct {
		Transitions []Transition `json:"transitions"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(key)+"/transitions", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Transitions, nil
}

// DoTransition moves the issue via a transition name or target status name.
func (c *Client) DoTransition(ctx context.Context, key, name string) error {
	ts, err := c.Transitions(ctx, key)
	if err != nil {
		return err
	}
	for _, t := range ts {
		if strings.EqualFold(t.Name, name) || strings.EqualFold(t.To.Name, name) {
			in := map[string]any{"transition": map[string]string{"id": t.ID}}
			return c.do(ctx, http.MethodPost, "/rest/api/2/issue/"+url.PathEscape(key)+"/transitions", in, nil)
		}
	}
	names := make([]string, 0, len(ts))
	for _, t := range ts {
		names = append(names, t.Name)
	}
	return fmt.Errorf("jira: no transition %q available for %s (available: %s)", name, key, strings.Join(names, ", "))
}

// BrowseURL is the human URL of an issue.
func (c *Client) BrowseURL(key string) string { return c.BaseURL + "/browse/" + key }
