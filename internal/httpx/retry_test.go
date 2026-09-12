package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fastPolicy() Policy {
	return Policy{Attempts: 4, Base: time.Millisecond, MaxWait: 50 * time.Millisecond}
}

func get(t *testing.T, p Policy, srv *httptest.Server) (*http.Response, error) {
	t.Helper()
	return p.Do(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
}

func TestRetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(502)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	resp, err := get(t, fastPolicy(), srv)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || calls != 3 {
		t.Errorf("status %d after %d calls", resp.StatusCode, calls)
	}
}

func TestGivesUpAfterAttempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	if _, err := get(t, fastPolicy(), srv); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 4 {
		t.Errorf("calls = %d, want 4", calls)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(404)
	}))
	defer srv.Close()
	resp, err := get(t, fastPolicy(), srv)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 || calls != 1 {
		t.Errorf("status %d after %d calls", resp.StatusCode, calls)
	}
}

func TestLongRateLimitReturnsResetTime(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
		w.WriteHeader(403)
		fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
	}))
	defer srv.Close()
	_, err := get(t, fastPolicy(), srv)
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("expected RateLimitError, got %v", err)
	}
	if rl.ResetAt.Before(reset.Add(-time.Minute)) || rl.Status != 403 {
		t.Errorf("reset at %s, status %d", rl.ResetAt, rl.Status)
	}
}

func TestShortRetryAfterIsHonoured(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	resp, err := get(t, fastPolicy(), srv)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}
