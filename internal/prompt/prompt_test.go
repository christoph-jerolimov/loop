package prompt

import (
	"strings"
	"testing"
	"time"

	"github.com/christoph-jerolimov/loop/internal/item"
)

func TestDefaultTemplatesRender(t *testing.T) {
	d := &Data{
		Project: "p", Branch: "loop/x", Base: "main", Workdir: "/w", RunID: "r1", SummaryFile: "/s.md", Attempt: 2, Round: 1,
		Item:           &item.Item{ID: "gh:1", Title: "Add login", Body: "Do it", Source: "gh", Comments: []item.Comment{{Author: "bob", Body: "please", Created: time.Now()}}},
		PR:             &PRRef{Number: 3, URL: "https://x/pull/3"},
		Reviews:        []Review{{Author: "ann", State: "CHANGES_REQUESTED", Body: "fix"}},
		ReviewComments: []ReviewComment{{Author: "ann", Path: "a.go", Line: 3, Body: "rename", DiffHunk: "@@ -1 +1 @@"}},
		Checks:         []Check{{Name: "test", Conclusion: "failure", Summary: "boom"}},
		Conflicts:      []string{"a.go"}, VerifyStep: "go test", VerifyOutput: "FAIL", Summary: "did stuff",
	}
	for _, name := range []string{TplSession, TplReview, TplCI, TplConflict, TplVerify, TplPRBody} {
		out, err := RenderFile(name, "", d)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(out, "<no value>") || strings.TrimSpace(out) == "" {
			t.Errorf("%s rendered badly:\n%s", name, out)
		}
	}
	out, _ := RenderFile(TplSession, "", d)
	if !strings.Contains(out, "**bob**") || !strings.Contains(out, "attempt 2") {
		t.Errorf("session template missing comments or attempt:\n%s", out)
	}
	d.Item.Comments = nil
	out, _ = RenderFile(TplSession, "", d)
	if strings.Contains(out, "Discussion on the ticket") {
		t.Error("comments section rendered without comments")
	}
}

func TestDefaultTemplateText(t *testing.T) {
	if !strings.Contains(Default(TplSession), "{{") {
		t.Error("the embedded session template must contain template actions")
	}
	if Default("no-such-template") != "" {
		t.Error("unknown templates must be empty")
	}
}
