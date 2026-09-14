package term

import (
	"bytes"
	"testing"
)

func TestPaint(t *testing.T) {
	var off Painter
	if got := off.Paint("x", Red, Bold); got != "x" {
		t.Errorf("disabled painter must leave text alone, got %q", got)
	}
	on := Painter(true)
	if got := on.Paint("x", Red, Bold); got != "\x1b[31m\x1b[1mx\x1b[0m" {
		t.Errorf("Paint = %q", got)
	}
	if got := on.Paint("x"); got != "x" {
		t.Errorf("no styles must add no codes, got %q", got)
	}
	if got := on.Paint("", Red); got != "" {
		t.Errorf("empty text must stay empty, got %q", got)
	}
}

func TestDetect(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("TERM", "xterm-256color")
	if Detect(&bytes.Buffer{}) {
		t.Error("a buffer is not a terminal")
	}
	if Detect(nil) {
		t.Error("nil writer gets no colours")
	}
	t.Setenv("CLICOLOR_FORCE", "1")
	if !Detect(&bytes.Buffer{}) {
		t.Error("CLICOLOR_FORCE=1 forces colours on")
	}
	t.Setenv("NO_COLOR", "1")
	if Detect(&bytes.Buffer{}) {
		t.Error("NO_COLOR wins over CLICOLOR_FORCE")
	}
}
