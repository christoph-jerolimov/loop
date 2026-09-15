You are working in the repository checked out at {{ .Workdir }} on branch `{{ .Branch }}` (based on `{{ .Base }}`).
Implement the following backlog item completely. Work autonomously: do not ask questions, make reasonable assumptions and note them in the summary.

# {{ .Item.Title }}

Source: {{ .Item.Source }} ({{ .Item.ID }}){{ if .Item.URL }} — {{ .Item.URL }}{{ end }}
{{- if .Item.Labels }}
Labels: {{ join .Item.Labels ", " }}
{{- end }}

{{ .Item.Body }}
{{ if .Item.Comments }}
## Discussion on the ticket
{{ range .Item.Comments }}
**{{ .Author }}** ({{ .Created.Format "2006-01-02" }}):
{{ quote .Body }}
{{ end }}{{ end }}{{ if .Plan }}
## Plan

This plan was written before the implementation and approved; follow it unless the code proves it wrong, and say so in the summary if you deviate.

{{ .Plan }}
{{ end }}{{ if .Item.Every }}
## Recurring task

This task runs every {{ .Item.Every }}; each occurrence is a fresh run on a fresh branch. {{ if .Previous }}The previous occurrence (run `{{ .Previous.RunID }}`, {{ .Previous.Started.Format "2006-01-02" }}) ended with: {{ .Previous.Result }}.{{ if .Previous.Summary }} Its summary:

{{ quote .Previous.Summary }}{{ end }}{{ else }}This is the first occurrence.{{ end }}

If there is nothing to change this time, change nothing and leave the working tree clean: a run without commits ends as "no changes", not as a failure.
{{ end }}
## Rules

- Follow the conventions of the repository (CLAUDE.md, AGENTS.md, .cursor/rules, contributing docs).
- Add or update tests for what you change and run the relevant test suite.
- Commit your work in small, well described commits. Leave the working tree clean.
- Do not push, do not open a pull request, do not switch branches.
- When you are done, write a short summary for the pull request description into `{{ .SummaryFile }}` (markdown: what changed, why, how it was tested, any assumptions or follow-ups).
{{- if gt .Attempt 1 }}

This is attempt {{ .Attempt }}. The previous attempt did not produce a usable result; start from the current state of the branch and finish the work.
{{- end }}
