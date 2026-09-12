// Package httpx adds retries with backoff and rate-limit awareness to the
// plain net/http calls of the GitHub and Jira clients.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Policy configures Do.
type Policy struct {
	// Attempts is the total number of tries, including the first.
	Attempts int
	// Base is the first backoff delay; it doubles per attempt, with jitter.
	Base time.Duration
	// MaxWait caps a single wait. A rate limit that asks for more returns a
	// *RateLimitError instead of blocking.
	MaxWait time.Duration
}

// Default is the policy the API clients use.
var Default = Policy{Attempts: 4, Base: time.Second, MaxWait: 2 * time.Minute}

// RateLimitError is returned when the server asks the client to wait
// longer than the policy allows. ResetAt says when to try again.
type RateLimitError struct {
	Status  int
	ResetAt time.Time
	Body    string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited (HTTP %d) until %s", e.Status, e.ResetAt.Format(time.RFC3339))
}

// Do performs the request built by build, retrying network errors, 5xx,
// 429 and rate-limited 403 responses. Responses with other statuses are
// returned untouched for the caller to interpret. build runs once per
// attempt so bodies are fresh.
func (p Policy) Do(ctx context.Context, client *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
	if p.Attempts < 1 {
		p.Attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < p.Attempts; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			if !retryableNetErr(err) || attempt == p.Attempts-1 {
				return nil, err
			}
			lastErr = err
			if werr := sleep(ctx, p.backoff(attempt)); werr != nil {
				return nil, werr
			}
			continue
		}
		wait, retry, limited := p.classify(resp, attempt)
		if !retry {
			return resp, nil
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if limited && wait > p.MaxWait {
			return nil, &RateLimitError{Status: resp.StatusCode, ResetAt: time.Now().Add(wait), Body: strings.TrimSpace(string(body))}
		}
		if attempt == p.Attempts-1 {
			if limited {
				return nil, &RateLimitError{Status: resp.StatusCode, ResetAt: time.Now().Add(wait), Body: strings.TrimSpace(string(body))}
			}
			return nil, fmt.Errorf("HTTP %d after %d attempts: %s", resp.StatusCode, p.Attempts, strings.TrimSpace(string(body)))
		}
		lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		if werr := sleep(ctx, wait); werr != nil {
			return nil, werr
		}
	}
	return nil, lastErr
}

// classify decides whether to retry a response, how long to wait, and
// whether the wait comes from a rate limit rather than a transient error.
func (p Policy) classify(resp *http.Response, attempt int) (wait time.Duration, retry, limited bool) {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		limited = true
	case resp.StatusCode == http.StatusForbidden && isRateLimited(resp):
		limited = true
	case resp.StatusCode >= 500:
	default:
		return 0, false, false
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil {
			return time.Duration(secs) * time.Second, true, limited
		}
		if t, err := http.ParseTime(ra); err == nil {
			return time.Until(t), true, limited
		}
	}
	if limited {
		if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
			if secs, err := strconv.ParseInt(strings.TrimSpace(reset), 10, 64); err == nil {
				w := time.Until(time.Unix(secs, 0)) + time.Second
				if w > p.backoff(attempt) {
					return w, true, true
				}
			}
		}
	}
	return p.backoff(attempt), true, limited
}

func isRateLimited(resp *http.Response) bool {
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	return resp.Header.Get("Retry-After") != ""
}

func (p Policy) backoff(attempt int) time.Duration {
	d := p.Base << uint(attempt)
	if d > p.MaxWait {
		d = p.MaxWait
	}
	// Full jitter keeps concurrent runs from retrying in lockstep.
	return time.Duration(rand.Int63n(int64(d)/2+1)) + d/2
}

func retryableNetErr(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "connection reset")
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
