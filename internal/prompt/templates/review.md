You are working in the repository at {{ .Workdir }} on branch `{{ .Branch }}`.
Pull request {{ .PR.URL }} for "{{ .Item.Title }}" received review feedback. Address every point below, then commit. Do not push.
{{ if .Reviews }}
## Reviews
{{ range .Reviews }}
**{{ .Author }}** ({{ .State }}){{ if .URL }} — {{ .URL }}{{ end }}
{{ if .Body }}{{ quote .Body }}{{ end }}
{{ end }}{{ end }}
{{- if .ReviewComments }}
## Inline comments
{{ range .ReviewComments }}
**{{ .Author }}** on `{{ .Path }}`{{ if .Line }} line {{ .Line }}{{ end }}{{ if .URL }} — {{ .URL }}{{ end }}
{{ if .DiffHunk }}```diff
{{ trunc 1500 .DiffHunk }}
```
{{ end }}{{ quote .Body }}
{{ end }}{{ end }}
{{- if .PRComments }}
## Conversation
{{ range .PRComments }}
**{{ .Author }}** ({{ .Created.Format "2006-01-02 15:04" }}):
{{ quote .Body }}
{{ end }}{{ end }}
## Rules

- Apply the requested changes. If a request is wrong or impossible, explain why in `{{ .SummaryFile }}` instead of silently skipping it.
- Keep the change focused; do not rewrite unrelated code.
- Run the relevant tests and commit with a message that references the review.
- Append a short "Review round {{ .Round }}" section to `{{ .SummaryFile }}` listing what you changed.
