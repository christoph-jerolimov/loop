package httpx

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRetryableNetErr(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&net.DNSError{Err: "no such host", IsTemporary: true}, true},
		{io.ErrUnexpectedEOF, true},
		{errors.New("read tcp: connection reset by peer"), true},
		{errors.New("permission denied"), false},
	}
	for _, c := range cases {
		if got := retryableNetErr(c.err); got != c.want {
			t.Errorf("retryableNetErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestRateLimitErrorMessage(t *testing.T) {
	err := &RateLimitError{Status: 403, ResetAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	if msg := err.Error(); !strings.Contains(msg, "rate limited (HTTP 403)") || !strings.Contains(msg, "2026-01-02T03:04:05Z") {
		t.Errorf("message = %q", msg)
	}
}
