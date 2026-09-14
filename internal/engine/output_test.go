package engine

import (
	"bytes"
	"strings"
	"testing"

	"github.com/christoph-jerolimov/loop/internal/item"
	"github.com/christoph-jerolimov/loop/internal/state"
)

func TestLogLinesCarryTheRunPrefixAndColours(t *testing.T) {
	store := state.NewStore(t.TempDir())
	r, err := store.Create(&item.Item{ID: "backlog:auth", Source: "backlog", NativeID: "auth"}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	e := &Engine{Out: &out, Store: store}
	before := len(r.Events)
	e.logf(r, "starting %s", "session")
	e.failf(r, "failed: %v", "boom")
	e.notef(r, "waiting at gate %s", "before-code")
	e.okf(r, "done")
	want := "[backlog:auth] starting session\n[backlog:auth] failed: boom\n[backlog:auth] waiting at gate before-code\n[backlog:auth] done\n"
	if out.String() != want {
		t.Errorf("plain output =\n%q\nwant\n%q", out.String(), want)
	}
	if len(r.Events) != before+4 || r.Events[before+1].Message != "failed: boom" {
		t.Errorf("every line must be recorded in the run log: %+v", r.Events)
	}

	out.Reset()
	e.Paint = true
	e.logf(r, "starting")
	e.failf(r, "failed")
	e.notef(r, "gate")
	e.okf(r, "done")
	want = "\x1b[2m[backlog:auth]\x1b[0m starting\n" +
		"\x1b[2m[backlog:auth]\x1b[0m \x1b[31mfailed\x1b[0m\n" +
		"\x1b[2m[backlog:auth]\x1b[0m \x1b[33mgate\x1b[0m\n" +
		"\x1b[2m[backlog:auth]\x1b[0m \x1b[32mdone\x1b[0m\n"
	if out.String() != want {
		t.Errorf("coloured output =\n%q\nwant\n%q", out.String(), want)
	}
	for _, ev := range r.Events {
		if strings.Contains(ev.Message, "\x1b") {
			t.Errorf("the run log must stay free of escape codes: %q", ev.Message)
		}
	}
}
