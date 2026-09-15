You are working in the repository checked out at {{ .Workdir }} on branch `{{ .Branch }}` (based on `{{ .Base }}`).
This repository is specified with OpenSpec: `openspec/specs/` holds the current requirements, and every change to the code goes through a change folder under `openspec/changes/`. Take the following backlog item through the whole OpenSpec cycle in this session: propose the change, implement it, and archive it, so that the pull request carries the code, the updated specs and the archived change together. Work autonomously: do not ask questions, make reasonable assumptions and note them in the summary.

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
{{ end }}{{ end }}
## OpenSpec workflow

Work through the three stages in order. They are what the OpenSpec commands `/opsx:propose`, `/opsx:apply` and `/opsx:archive` do; use the commands (or the `openspec-*` skills) when they are available to you, and follow the steps below by hand otherwise. The `openspec` CLI is on the `PATH`.

1. **Propose.** Read `openspec/specs/` and the recent folders under `openspec/changes/archive/` to learn the current behaviour and how this project writes specs. Create one change folder `openspec/changes/<change-id>/`, with a short kebab-case id derived from the item title, containing:
   - `proposal.md`: why the change is needed, what changes, and what it affects.
   - `design.md`, only when there are decisions worth recording (trade-offs, alternatives, migration).
   - `tasks.md`: an ordered checklist (`- [ ] 1.1 ...`) of the implementation steps, tests included.
   - `specs/<capability>/spec.md` per affected capability: delta specs with `## ADDED Requirements`, `## MODIFIED Requirements` and `## REMOVED Requirements` sections, each holding `### Requirement: <name>` blocks with SHALL/MUST wording and at least one `#### Scenario:` each.
   Run `openspec validate <change-id> --strict` and fix everything it reports. Commit the proposal on its own, before any code changes.
2. **Apply.** Implement the tasks of `tasks.md` in order and tick each one off (`- [x]`) as it is done. Add or update tests for what you change and run the relevant test suite. Commit in small, well described commits. If the code proves the proposal wrong, update the proposal and the delta specs to match what you actually build, and say so in the summary.
3. **Archive.** Once every task is ticked and the tests pass, run `openspec archive <change-id> --yes` (the `--yes` answers the prompts a headless session cannot). It merges the delta specs into `openspec/specs/` and moves the change to `openspec/changes/archive/<date>-<change-id>/`. Read the merged specs once to check they read well, run `openspec validate --specs --strict`, and commit the archive as the last commit. When you finish, your change must be under `openspec/changes/archive/`, not under `openspec/changes/`.

## Rules

- Follow the conventions of the repository (CLAUDE.md, AGENTS.md, .cursor/rules, contributing docs).
- Never edit `openspec/specs/` by hand: every requirement change is a delta spec of your change and reaches the main specs through the archive step.
- Leave other folders under `openspec/changes/` alone; they are other people's work in progress.
- Commit your work in small, well described commits. Leave the working tree clean.
- Do not push, do not open a pull request, do not switch branches.
- When you are done, write a short summary for the pull request description into `{{ .SummaryFile }}` (markdown: what changed, why, how it was tested, the path of the archived change, the capabilities whose specs changed, any assumptions or follow-ups).
{{- if gt .Attempt 1 }}

This is attempt {{ .Attempt }}. The previous attempt did not produce a usable result; start from the current state of the branch. A change folder the previous attempt left under `openspec/changes/` is yours to continue, not to duplicate.
{{- end }}
