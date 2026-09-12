# loop

`loop` picks ideas, goals or tickets from a backlog, starts an AI coding
agent (Claude Code or Cursor) in a fresh checkout of your repository, opens a
pull request, and keeps working that PR through CI failures and review
comments until it is merged and the ticket is closed.

```
backlog ──▶ checkout ──▶ agent session ──▶ verify ──▶ PR ──▶ monitor ──▶ merge ──▶ close ticket
 (md/GitHub/Jira)  (worktree)   (claude/cursor)  (steps)         (CI, reviews, conflicts → fix rounds)
```

## Install

```sh
go install github.com/christoph-jerolimov/loop/cmd/loop@latest
```

Requirements: `git`, one of `claude` (Claude Code CLI) or `agent` (Cursor
CLI), and a GitHub token (`GITHUB_TOKEN`, or `gh auth login`). Jira needs
`JIRA_EMAIL` + `JIRA_API_TOKEN` (Cloud) or `JIRA_TOKEN` (Server).

## Quick start

```sh
mkdir my-service-loop && cd my-service-loop
loop init                 # loop.yaml, prompts/, backlog/, hooks/, .gitignore
$EDITOR loop.yaml         # set repo.url and your sources
loop doctor               # config, tools, tokens and sources all good?
$EDITOR backlog/login.md  # write an idea
loop list                 # items in pick-up order, with blockers
loop run login.md         # one item end to end
loop run --all            # every ready item, in order
loop watch                # keep driving open PRs (reviews, CI, merge)
```

A loop project is a folder with a `loop.yaml`. Commit it: it is the home of
your backlog, prompt templates and scripts, and everything loop uses is
referenced from `loop.yaml`. `.loop/` holds only state (the base clone,
per-run worktrees and run folders with `run.yaml` and logs) and is
git-ignored.

## How one iteration works

1. **Pick.** `loop list` orders open items by the position of their source
   in `loop.yaml`, then oldest first. Items with open dependencies, an
   in-progress marker or a running loop are shown but skipped.
2. **Checkout.** A worktree (or clone) is created in `.loop/workdirs/` on a
   branch `loop/<id>-<title>`; an existing name gets a `-2`, `-3` suffix.
   The item is claimed (frontmatter, GitHub label + comment, Jira label).
3. **Setup.** The `steps.setup` list from `loop.yaml` runs in the workdir:
   shell commands, script files from the project folder, or agent sessions.
   Configured skill folders are symlinked into `.claude/skills/`. The repository's own
   `CLAUDE.md`, `AGENTS.md` or `.cursor/rules` are picked up by the agent as
   usual.
4. **Session.** The session prompt template is rendered with the item (and
   its ticket comments, if the template uses them) and the agent runs
   headlessly. The agent commits its work and writes a PR summary. Up to
   `agent.attempts` tries. Join a running or finished session at any time:
   `loop join <run>` prints `cd <workdir> && claude --resume <id>`.
5. **Verify.** `steps.verify` scripts run; a failing one starts a fix session
   with the output, then verification restarts.
6. **PR.** The branch is pushed and a (draft) PR is opened from a template,
   linked to the ticket (`Closes #n` for GitHub issues).
7. **Monitor.** The PR is polled. A merge conflict is merged with git or, if
   that fails, by an agent session. New review comments or a red CI start a
   fix session driven by the review or CI prompt template, with the failing
   job's log included, and the result is pushed. Once CI is green a draft PR is marked ready for review.
8. **Merge.** Depending on `workflow.merge`: never (`manual`), `when-green`,
   `when-green-and-approved`, or leave it to GitHub (`github-auto-merge`).
   Optional gates (`before-pr`, `before-fix`, `before-merge`) pause the run
   until `loop approve <run>`.
9. **Close.** The item is done when the ticket is closed. loop closes it
   after the merge (or GitHub does through the closing keyword), then removes
   the worktree and the remote branch.

Every step is idempotent and persisted in `.loop/runs/<run>/run.yaml`, so a
crashed `loop watch` simply continues where it stopped. Runs that exhaust
their fix rounds or hit an unrecoverable error are parked as `blocked` or
`failed` with a note on the PR or ticket; `loop resume <run>` puts them back.

## Commands

| Command | What it does |
| --- | --- |
| `loop init [dir]` | Scaffold a project. |
| `loop doctor` | Check config, tools, credentials, repository and sources before running. |
| `loop list [-s source] [--ready] [--json]` | Open items in pick-up order with status. |
| `loop show <item> [--prompt]` | Item details, or the rendered session prompt. |
| `loop run <item> [--force] [--no-watch]` | Run one item to completion. |
| `loop run --all [-s source]` | Run every ready item, respecting `workflow.concurrency`. |
| `loop watch [--pick]` | Drive all active runs; with `--pick` also start ready items. |
| `loop status [-a]` | Runs and phases. |
| `loop logs <run> [--session] [-f]` | Run log, or an agent session transcript or prompt. |
| `loop approve <run>` | Continue past a gate. |
| `loop resume <run>` | Re-activate a blocked or failed run. |
| `loop join <run>` | Print the command to continue the agent session by hand. |
| `loop clean [run]` | Remove workdirs of finished runs. |

Items are addressed by anything a source understands: `login.md`, `#12`,
`owner/repo#12`, `PROJ-7`, a ticket URL, or the full id `backlog:login`.

## Dependencies between items

Everywhere (markdown, GitHub, Jira) a line in the description that starts
with `depends on:` lists references, separated by commas or spaces:

```
depends on: auth.md, #12, PROJ-7
```

Markdown items can also use `depends_on:` in the frontmatter, and Jira
"is blocked by" / "depends on" links are honoured. References may cross
sources. An item is blocked until every referenced item is closed; `loop run
--force` overrides that and the in-progress check.

## Documentation

The same pages are published as a website built from the `website/` folder
(see [website/README.md](website/README.md)).

- [Command reference](docs/cli.md)
- [Configuration reference](docs/configuration.md)
- [Prompt templates](docs/prompts.md), including session templates
  [with](docs/examples/session-with-comments.md) and
  [without](docs/examples/session-without-comments.md) ticket comments
- [Workflow, gates and merge policies](docs/workflow.md)

## Development

```sh
make build   # ./bin/loop
make test
```
