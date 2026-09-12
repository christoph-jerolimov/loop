{{ .Summary }}

---
{{ if .Item.URL }}Backlog item: {{ .Item.URL }}{{ else }}Backlog item: {{ .Item.ID }}{{ end }}
Run: `{{ .RunID }}`
