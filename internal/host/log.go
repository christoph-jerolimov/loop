package host

import (
	"regexp"
	"strings"
)

var (
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	timestampRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z\s?`)
)

// TailLog returns the last n lines of a CI job log with the leading
// timestamps and ANSI colour codes removed.
func TailLog(log string, n int) string {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		l = timestampRe.ReplaceAllString(l, "")
		lines[i] = strings.TrimRight(ansiRe.ReplaceAllString(l, ""), " \r")
	}
	return strings.Join(lines, "\n")
}
