package item

import (
	"testing"
	"time"
)

func TestParseBodyDirectives(t *testing.T) {
	body := "Some idea.\n\nDepends on: #12, PROJ-4 and backlog/auth.md\ndepends-on: https://github.com/o/r/issues/9\nModel: claude-opus-5\n"
	deps, model := ParseBodyDirectives(body)
	want := []string{"#12", "PROJ-4", "backlog/auth.md", "https://github.com/o/r/issues/9"}
	if len(deps) != len(want) {
		t.Fatalf("deps = %v, want %v", deps, want)
	}
	for i := range want {
		if deps[i] != want[i] {
			t.Errorf("dep %d = %q, want %q", i, deps[i], want[i])
		}
	}
	if model != "claude-opus-5" {
		t.Errorf("model = %q", model)
	}
}

func TestOrder(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []*Item{
		{NativeID: "b", SourceIndex: 1, Created: t0},
		{NativeID: "a", SourceIndex: 0, Created: t0.Add(time.Hour)},
		{NativeID: "c", SourceIndex: 0, Created: t0},
		{NativeID: "p", SourceIndex: 2, Created: t0.Add(2 * time.Hour), Priority: 2},
		{NativeID: "q", SourceIndex: 1, Created: t0.Add(3 * time.Hour), Priority: 1},
	}
	Order(items)
	var got string
	for _, it := range items {
		got += it.NativeID
	}
	if got != "qpcab" {
		t.Errorf("order = %q, want qpcab (priority first, then source, then age)", got)
	}
}

func TestParsePriority(t *testing.T) {
	cases := map[string]int{"highest": 1, "Critical": 1, "high": 2, "medium": 3, "Normal": 3, "low": 4, "lowest": 5, "P0": 1, "p2": 3, "1": 1, "7": 7, "": 0, "soon": 0, "P": 0, "10": 0}
	for in, want := range cases {
		if got := ParsePriority(in); got != want {
			t.Errorf("ParsePriority(%q) = %d, want %d", in, got, want)
		}
	}
	labels := map[string]int{"priority: high": 2, "priority/high": 2, "prio-2": 2, "Priority=Low": 4, "P1": 2, "bug": 0, "ready-for-agent": 0}
	for in, want := range labels {
		if got := PriorityFromLabel(in); got != want {
			t.Errorf("PriorityFromLabel(%q) = %d, want %d", in, got, want)
		}
	}
	if PriorityName(0) != "" || PriorityName(2) != "high" || PriorityName(7) != "7" {
		t.Error("PriorityName")
	}
	it := &Item{Body: "Do it.\n\nPriority: urgent\n"}
	it.ApplyBodyDirectives()
	if it.Priority != 1 {
		t.Errorf("body directive priority = %d", it.Priority)
	}
	it = &Item{Body: "prio: low", Priority: 2}
	it.ApplyBodyDirectives()
	if it.Priority != 2 {
		t.Error("an explicit priority wins over the body line")
	}
}

func TestSlug(t *testing.T) {
	if s := Slug("Add OAuth2 login (Google)!", 16); s != "add-oauth2-login" {
		t.Errorf("slug = %q", s)
	}
}
