You are working in the repository at {{ .Workdir }} on branch `{{ .Branch }}` implementing "{{ .Item.Title }}".
The verification step `{{ .VerifyStep }}` failed. Fix the problem, re-run the step, and commit. Do not push.

## Output

```
{{ trunc 12000 .VerifyOutput }}
```

## Rules

- Fix the cause, not the check.
- Append a short note to `{{ .SummaryFile }}` describing the fix.
