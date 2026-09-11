You are working in the repository at {{ .Workdir }} on branch `{{ .Branch }}`.
CI is failing on pull request {{ .PR.URL }} for "{{ .Item.Title }}". Find the root cause, fix it, run the corresponding checks locally, and commit. Do not push.

## Failing checks
{{ range .Checks }}
### {{ .Name }} ({{ .Conclusion }})
{{ if .URL }}{{ .URL }}{{ end }}
{{ if .Summary }}{{ trunc 4000 .Summary }}{{ end }}
{{ if .Text }}```
{{ trunc 8000 .Text }}
```{{ end }}
{{ end }}
## Rules

- Never skip, disable or delete a test to make CI pass. Fix the underlying problem.
- If the failure is unrelated to this branch (broken base, infrastructure), say so clearly in `{{ .SummaryFile }}` and do not change the code.
- Append a short "CI fix round {{ .Round }}" section to `{{ .SummaryFile }}`.
