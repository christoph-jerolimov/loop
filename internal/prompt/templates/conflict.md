You are working in the repository at {{ .Workdir }} on branch `{{ .Branch }}`.
A merge of `origin/{{ .Base }}` into this branch is in progress and has conflicts. Resolve them so the branch keeps its intent ("{{ .Item.Title }}") and incorporates the latest base changes.

## Conflicting files
{{ range .Conflicts }}
- `{{ . }}`
{{- end }}

## Rules

- Inspect both sides of every conflict; do not blindly pick one side.
- Regenerate lock files or generated code with the project's tooling rather than editing them by hand.
- Run the relevant tests, then complete the merge with `git commit` (a merge commit is expected). Do not push.
