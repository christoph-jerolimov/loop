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
	}
	Order(items)
	got := items[0].NativeID + items[1].NativeID + items[2].NativeID
	if got != "cab" {
		t.Errorf("order = %q, want cab", got)
	}
}

func TestSlug(t *testing.T) {
	if s := Slug("Add OAuth2 login (Google)!", 16); s != "add-oauth2-login" {
		t.Errorf("slug = %q", s)
	}
}
