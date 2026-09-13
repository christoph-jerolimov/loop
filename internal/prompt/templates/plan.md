You are working in the repository checked out at {{ .Workdir }} on branch `{{ .Branch }}` (based on `{{ .Base }}`).
Before any code is written, produce a short implementation plan for the following backlog item. Explore the repository as much as you need, but do not modify, create or delete any file except the plan file named below, and do not commit.

# {{ .Item.Title }}

Source: {{ .Item.Source }} ({{ .Item.ID }}){{ if .Item.URL }} — {{ .Item.URL }}{{ end }}

{{ .Item.Body }}
{{ if .Item.Comments }}
## Discussion on the ticket
{{ range .Item.Comments }}
**{{ .Author }}** ({{ .Created.Format "2006-01-02" }}):
{{ quote .Body }}
{{ end }}{{ end }}
## What to write

Write the plan as markdown into `{{ .PlanFile }}`, at most one screen long:

1. **Goal**: one sentence on what will be true when this is done.
2. **Approach**: the design in a few sentences, including alternatives you rejected and why.
3. **Changes**: the files or packages you expect to touch, and the tests you will add.
4. **Risks and assumptions**: anything unclear in the ticket and how you will decide it.
5. **Estimate**: S, M or L, and the number of sessions you expect.

The plan is posted on the ticket for a human to read before the implementation starts, so write it for them.
