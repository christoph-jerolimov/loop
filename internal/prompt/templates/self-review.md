You are reviewing the branch `{{ .Branch }}` in the repository at {{ .Workdir }} before it becomes a pull request for "{{ .Item.Title }}". Read the diff below against `{{ .Base }}` and the ticket, run the tests if that helps, but do not modify, create or delete any file except the findings file named below, and do not commit.

# Ticket

{{ .Item.Body }}

# Diff against `{{ .Base }}`

```diff
{{ .Diff }}
```

# What to write

Write your findings as a markdown list into `{{ .FindingsFile }}`: bugs, behaviour that does not match the ticket, missing tests, and anything a careful reviewer would send back. Be concrete: name the file and what is wrong. Do not list style preferences. If there is nothing worth sending back, write exactly `No findings.`
