// Package schedule parses the every: value of a recurring item and
// computes when the item is due next.
//
// Two kinds of schedule exist. An interval ("7d", "36h", "weekly") makes
// the item due once that much time has passed since its previous run
// started. A calendar schedule fires at set times: a five-field cron
// expression ("0 6 * * 1"), one of the cron aliases ("@daily", "@weekly",
// "@monthly", "@hourly"), or a day-and-time form ("mon 06:00",
// "mon,thu 06:00", "daily 03:00", "weekdays 07:30", "06:00"). Calendar
// times are local time of the process running loop. An item is due when
// a scheduled time has passed since its previous run started, so a
// backlog of missed times collapses into one run.
package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/christoph-jerolimov/loop/internal/item"
)

// Schedule is a parsed every: value.
type Schedule struct {
	raw      string
	interval time.Duration
	cron     *spec
}

// Parse accepts an interval (see item.ParseInterval), a cron alias, a
// five-field cron expression, or a day-and-time form.
func Parse(s string) (Schedule, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Schedule{}, fmt.Errorf("empty schedule")
	}
	if d, err := item.ParseInterval(raw); err == nil {
		return Schedule{raw: raw, interval: d}, nil
	}
	if expr, ok := aliases[strings.ToLower(raw)]; ok {
		sp, err := parseCron(expr)
		if err != nil {
			return Schedule{}, err
		}
		return Schedule{raw: raw, cron: sp}, nil
	}
	if expr, ok := parseDayTime(raw); ok {
		sp, err := parseCron(expr)
		if err != nil {
			return Schedule{}, fmt.Errorf("cannot parse %q: %w", raw, err)
		}
		return Schedule{raw: raw, cron: sp}, nil
	}
	if len(strings.Fields(raw)) == 5 {
		sp, err := parseCron(raw)
		if err != nil {
			return Schedule{}, fmt.Errorf("cannot parse %q: %w", raw, err)
		}
		return Schedule{raw: raw, cron: sp}, nil
	}
	return Schedule{}, fmt.Errorf("cannot parse %q: use an interval such as 7d, 2w or weekly, a time such as \"mon 06:00\", \"weekdays 07:30\" or \"daily 03:00\", or a cron expression such as \"0 6 * * 1\"", raw)
}

var aliases = map[string]string{
	"@hourly":  "0 * * * *",
	"@daily":   "0 0 * * *",
	"@weekly":  "0 0 * * 0",
	"@monthly": "0 0 1 * *",
}

// String is the value as the author wrote it.
func (s Schedule) String() string { return s.raw }

// Interval returns the interval of an interval schedule, 0 for a
// calendar schedule.
func (s Schedule) Interval() time.Duration { return s.interval }

// Calendar reports whether the schedule fires at set times rather than
// an interval after the previous run.
func (s Schedule) Calendar() bool { return s.cron != nil }

// Next is the first time the schedule fires after the given time: the
// time plus the interval, or the next matching calendar minute. ok is
// false for a calendar schedule that never matches (a 30th of February).
func (s Schedule) Next(after time.Time) (next time.Time, ok bool) {
	if s.cron == nil {
		if s.interval <= 0 {
			return time.Time{}, false
		}
		return after.Add(s.interval), true
	}
	return s.cron.next(after.In(time.Local))
}

// spec is a parsed five-field cron expression.
type spec struct {
	minute, hour, dom, month, dow [64]bool
	// anyDom and anyDow record a "*" in the day fields: when only one of
	// them restricts, that one decides; when both do, either may match,
	// as in cron.
	anyDom, anyDow bool
}

var (
	monthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	dayNames   = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
)

func parseCron(expr string) (*spec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("a cron expression has five fields (minute hour day-of-month month day-of-week), got %d", len(fields))
	}
	sp := &spec{}
	var err error
	if sp.minute, _, err = parseField(fields[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if sp.hour, _, err = parseField(fields[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if sp.dom, sp.anyDom, err = parseField(fields[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if sp.month, _, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if sp.dow, sp.anyDow, err = parseField(fields[4], 0, 7, dayNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	if sp.dow[7] { // 7 is Sunday too
		sp.dow[0] = true
	}
	return sp, nil
}

// parseField parses one cron field: "*", "*/n", "a", "a-b", "a-b/n",
// names from the list, and comma-separated combinations of those.
func parseField(f string, lo, hi int, names []string) (set [64]bool, any bool, err error) {
	for _, part := range strings.Split(f, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			return set, false, fmt.Errorf("empty entry in %q", f)
		}
		step := 1
		if rng, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n < 1 {
				return set, false, fmt.Errorf("bad step in %q", part)
			}
			step, part = n, rng
		}
		start, end := lo, hi
		switch {
		case part == "*":
			if step == 1 {
				any = true
			}
		default:
			a, b, isRange := strings.Cut(part, "-")
			if start, err = value(a, lo, hi, names); err != nil {
				return set, false, err
			}
			if isRange {
				if end, err = value(b, lo, hi, names); err != nil {
					return set, false, err
				}
				if end < start {
					return set, false, fmt.Errorf("range %q runs backwards", part)
				}
			} else if step == 1 {
				end = start
			} else {
				end = hi // "5/2" means from 5 in steps of 2
			}
		}
		for v := start; v <= end; v += step {
			set[v] = true
		}
	}
	return set, any, nil
}

func value(s string, lo, hi int, names []string) (int, error) {
	for i, n := range names {
		if s == n {
			return lo + i, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number%s", s, nameHint(names))
	}
	if v < lo || v > hi {
		return 0, fmt.Errorf("%d is outside %d-%d", v, lo, hi)
	}
	return v, nil
}

func nameHint(names []string) string {
	if names == nil {
		return ""
	}
	return " or one of " + strings.Join(names, ", ")
}

// next finds the first matching minute after t. It walks the calendar,
// skipping whole months, days and hours that cannot match, and gives up
// after five years.
func (sp *spec) next(t time.Time) (time.Time, bool) {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !sp.month[int(t.Month())] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !sp.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !sp.hour[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !sp.minute[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

func (sp *spec) dayMatches(t time.Time) bool {
	dom := sp.dom[t.Day()]
	dow := sp.dow[int(t.Weekday())]
	switch {
	case sp.anyDom && sp.anyDow:
		return true
	case sp.anyDom:
		return dow
	case sp.anyDow:
		return dom
	}
	return dom || dow
}

// parseDayTime turns "mon 06:00", "mon,thu 6:30", "daily 03:00",
// "weekdays 07:30", "weekends 10:00" or a bare "06:00" into a cron
// expression. Day names without a time mean midnight.
func parseDayTime(s string) (string, bool) {
	fields := strings.Fields(strings.ToLower(s))
	if len(fields) == 0 || len(fields) > 2 {
		return "", false
	}
	days, clock := "*", "00:00"
	switch len(fields) {
	case 1:
		if strings.Contains(fields[0], ":") {
			clock = fields[0]
		} else {
			days = fields[0]
		}
	case 2:
		days, clock = fields[0], fields[1]
	}
	hh, mm, ok := strings.Cut(clock, ":")
	if !ok {
		return "", false
	}
	h, err1 := strconv.Atoi(hh)
	m, err2 := strconv.Atoi(mm)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 || len(mm) != 2 {
		return "", false
	}
	dow, ok := dayField(days)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d %d * * %s", m, h, dow), true
}

func dayField(days string) (string, bool) {
	switch days {
	case "*", "daily", "every-day", "everyday":
		return "*", true
	case "weekdays":
		return "1-5", true
	case "weekends":
		return "0,6", true
	}
	var out []int
	for _, d := range strings.Split(days, ",") {
		d = strings.TrimSpace(d)
		found := false
		for i, n := range dayNames {
			if strings.HasPrefix(d, n) && (len(d) == 3 || fullDay(i) == d) {
				out = append(out, i)
				found = true
				break
			}
		}
		if !found {
			return "", false
		}
	}
	if len(out) == 0 {
		return "", false
	}
	sort.Ints(out)
	parts := make([]string, len(out))
	for i, v := range out {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ","), true
}

func fullDay(i int) string {
	return []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}[i]
}
