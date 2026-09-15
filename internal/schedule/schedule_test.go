package schedule

import (
	"strings"
	"testing"
	"time"
)

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.Local)
}

func TestParseForms(t *testing.T) {
	cases := map[string]string{
		"7d":                "interval",
		"weekly":            "interval",
		"@daily":            "calendar",
		"@monthly":          "calendar",
		"0 6 * * 1":         "calendar",
		"*/15 9-17 * * 1-5": "calendar",
		"0 0 1 jan,jul *":   "calendar",
		"mon 06:00":         "calendar",
		"Mon,Thu 6:30":      "calendar",
		"weekdays 07:30":    "calendar",
		"weekends 10:00":    "calendar",
		"daily 03:00":       "calendar",
		"06:00":             "calendar",
		"monday":            "calendar",
	}
	for in, want := range cases {
		s, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if got := map[bool]string{true: "calendar", false: "interval"}[s.Calendar()]; got != want {
			t.Errorf("Parse(%q) is %s, want %s", in, got, want)
		}
		if s.String() != strings.TrimSpace(in) {
			t.Errorf("String() = %q", s.String())
		}
	}
	for _, in := range []string{"", "soon", "0 6 * *", "60 6 * * 1", "0 25 * * *", "0 6 * * 8", "0 6 32 * *", "0 6 * foo *", "mon 6", "mon 24:00", "mon 6:5", "fun 06:00", "5-1 * * * *", "*/0 * * * *"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) must fail", in)
		}
	}
	if _, err := Parse("0 6 * * 8"); err == nil || !strings.Contains(err.Error(), "day of week") {
		t.Errorf("error should name the field: %v", err)
	}
}

func TestNextInterval(t *testing.T) {
	s, _ := Parse("7d")
	start := at(2026, 9, 15, 10, 0)
	next, ok := s.Next(start)
	if !ok || !next.Equal(start.Add(7*24*time.Hour)) || s.Interval() != 7*24*time.Hour {
		t.Errorf("Next = %v, %v", next, ok)
	}
}

func TestNextCalendar(t *testing.T) {
	// 2026-09-15 is a Tuesday.
	tue := at(2026, 9, 15, 10, 0)
	cases := []struct {
		spec  string
		after time.Time
		want  time.Time
	}{
		{"mon 06:00", tue, at(2026, 9, 21, 6, 0)},
		{"0 6 * * 1", tue, at(2026, 9, 21, 6, 0)},
		{"0 6 * * 1", at(2026, 9, 21, 6, 0), at(2026, 9, 28, 6, 0)},       // strictly after
		{"0 6 * * 1", at(2026, 9, 21, 5, 59), at(2026, 9, 21, 6, 0)},      // the same minute counts
		{"weekdays 07:30", at(2026, 9, 18, 8, 0), at(2026, 9, 21, 7, 30)}, // Friday after -> Monday
		{"weekends 10:00", tue, at(2026, 9, 19, 10, 0)},
		{"daily 03:00", tue, at(2026, 9, 16, 3, 0)},
		{"03:00", tue, at(2026, 9, 16, 3, 0)},
		{"@hourly", at(2026, 9, 15, 10, 20), at(2026, 9, 15, 11, 0)},
		{"@daily", tue, at(2026, 9, 16, 0, 0)},
		{"@weekly", tue, at(2026, 9, 20, 0, 0)},
		{"@monthly", tue, at(2026, 10, 1, 0, 0)},
		{"*/15 9-17 * * 1-5", at(2026, 9, 15, 17, 46), at(2026, 9, 16, 9, 0)},
		{"*/15 9-17 * * 1-5", at(2026, 9, 15, 10, 1), at(2026, 9, 15, 10, 15)},
		{"0 0 1 jan,jul *", tue, at(2027, 1, 1, 0, 0)},
		{"30 4 15 * *", tue, at(2026, 10, 15, 4, 30)},
		{"0 9 13 * fri", tue, at(2026, 9, 18, 9, 0)},                     // dom OR dow: the next Friday
		{"0 9 13 * fri", at(2026, 10, 10, 0, 0), at(2026, 10, 13, 9, 0)}, // ... or the 13th
		{"0 9 * 2 *", at(2026, 2, 28, 10, 0), at(2027, 2, 1, 9, 0)},
		{"0 0 * * 7", tue, at(2026, 9, 20, 0, 0)}, // 7 is Sunday
		{"0 0 * * sun", tue, at(2026, 9, 20, 0, 0)},
		{"monday", tue, at(2026, 9, 21, 0, 0)},
	}
	for _, c := range cases {
		s, err := Parse(c.spec)
		if err != nil {
			t.Errorf("%q: %v", c.spec, err)
			continue
		}
		got, ok := s.Next(c.after)
		if !ok || !got.Equal(c.want) {
			t.Errorf("%q after %v: got %v (%v), want %v", c.spec, c.after.Format("Mon 2006-01-02 15:04"), got.Format("Mon 2006-01-02 15:04"), ok, c.want.Format("Mon 2006-01-02 15:04"))
		}
	}
	never, _ := Parse("0 0 30 2 *")
	if _, ok := never.Next(tue); ok {
		t.Error("the 30th of February never comes")
	}
}
