package source

import (
	"errors"
	"strings"
	"testing"
)

func TestOpenDependencies(t *testing.T) {
	deps := []Dependency{
		{Ref: "auth.md", Closed: true},
		{Ref: "#7", Closed: false},
		{Ref: "ABC-9", Err: errors.New("not found")},
	}
	if got := strings.Join(OpenDependencies(deps), ","); got != "#7,ABC-9" {
		t.Errorf("open = %q, want the open and the unresolved one", got)
	}
	if OpenDependencies(nil) != nil {
		t.Error("no dependencies must give nil")
	}
}
