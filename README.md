# loop

`loop` picks ideas, goals or tickets from a backlog, starts an AI coding
agent (Claude Code, Cursor, Codex, Gemini CLI, Aider, OpenCode, Copilot
CLI, Amp or a script of your own) in a fresh checkout of your repository,
opens a pull request, and keeps working that PR through CI failures and
review comments until it is merged and the ticket is closed.

```
backlog ──▶ checkout ──▶ agent session ──▶ verify ──▶ PR ──▶ monitor ──▶ merge ──▶ close ticket
 (md/GitHub/GitLab/Jira) (worktree) (any harness) (steps)      (CI, reviews, conflicts → fix rounds)
```

## Install

Download the binary for your platform from the
[releases page](https://github.com/christoph-jerolimov/loop/releases)
(Linux, macOS and Windows, amd64 and arm64) and put it on your `PATH`, or
build from source:

```sh
go install github.com/christoph-jerolimov/loop/cmd/loop@latest
```

`loop --version` prints the release version.

Requirements: `git`, the CLI of the harness you pick (`claude`, Cursor's
`agent`, `codex`, `gemini`, `aider`, `opencode`, `copilot` or `amp`; or
any script for `runner: custom`), and a token for the code host:
`GITHUB_TOKEN` (or `gh auth login`) for GitHub, `GITLAB_TOKEN` for GitLab,
also self-hosted. Jira needs
`JIRA_EMAIL` + `JIRA_API_TOKEN` (Cloud) or `JIRA_TOKEN` (Server). On
Windows, `run:` steps need an `sh` on the `PATH` (Git for Windows provides
one); `script:` and `agent:` steps do not.

Agent sessions get an allowlisted environment: toolchain and agent
variables pass through, loop's own `GITHUB_TOKEN` and `JIRA_*` credentials
do not, so an agent cannot push or merge on its own. Name extra variables a
project needs in `agent.env_passthrough`.

Headless Claude sessions can only run commands the permission rules allow.
loop writes `agent.allow` and `agent.deny` from `loop.yaml` into the
workdir before each session (git add and commit allowed, git push denied by
default); add your test command there, see
[configuration](docs/configuration.md#permissions-in-headless-sessions).

## Quick start

```sh
cd my-service             # your checkout, or an empty folder next to it
loop init                 # loop.yaml from the checkout (or --repo), backlog/, then the doctor checks
$EDITOR backlog/login.md  # write an idea
loop list                 # items in pick-up order, with blockers
loop run login.md         # one item end to end
loop run --all            # every ready item, in order
loop watch                # keep driving open PRs (reviews, CI, merge)
```

A loop project is a folder with a `loop.yaml`: the repository itself, or a
separate folder next to it when the backlog should not live in the code
repository. Commit it: it is the home of your backlog, prompt templates
and scripts, and everything loop uses is referenced from `loop.yaml`.
`.loop/` holds only state (the base clone, per-run worktrees and run
folders with `run.yaml` and logs) and is git-ignored; runs work in those
worktrees, never in your working copy.

## How one iteration works

1. **Pick.** `loop list` orders open items by priority (a frontmatter key, a
   `priority: high` or `P1` label, or Jira's priority field), then by the
   position of their source in `loop.yaml`, then oldest first. Items with open dependencies, an
   in-progress marker or a running loop are shown but skipped.
2. **Checkout.** A worktree (or clone) is created in `.loop/workdirs/` on a
   branch `loop/<id>-<title>`; an existing name gets a `-2`, `-3` suffix.
   Without push access, branches go to a fork (`repo.fork`) and the PR
   opens from there.
   The item is claimed (frontmatter, GitHub or GitLab label + comment, Jira label).
3. **Setup.** The `steps.setup` list from `loop.yaml` runs in the workdir:
   shell commands, script files from the project folder, or agent sessions.
   Configured skill folders are symlinked into `.claude/skills/`. The repository's own
   `CLAUDE.md`, `AGENTS.md` or `.cursor/rules` are picked up by the agent as
   usual. The harness comes from `agent.runner`, or from `--runner` and
   `LOOP_RUNNER` for one invocation; a `custom` runner is any command that
   reads the prompt file and commits its work
   (see [harnesses](docs/configuration.md#harnesses)).
4. **Plan (optional).** With `workflow.plan: true` a session first writes a
   short plan with an estimate, changes nothing, and loop posts it on the
   ticket. A `before-code` gate lets a person approve the plan before any
   code is written, from the terminal or with `/loop approve` on the issue.
5. **Session.** The session prompt template is rendered with the item (and
   its ticket comments, if the template uses them; on GitHub and GitLab
   only comments by the owner and collaborators with write access, by default), written
   to a file in the run folder, and the agent loads it from there and runs
   headlessly. Fix rounds work the same way. The agent commits its work and writes a PR summary. Up to
   `agent.attempts` tries. Join a running or finished session at any time:
   `loop join <run>` prints `cd <workdir> && claude --resume <id>`.
6. **Verify.** `steps.verify` scripts run; a failing one starts a fix session
   with the output, then verification restarts. With `workflow.self_review`
   a second session then reviews the diff and its findings get one free fix
   round before anyone else sees the branch.
7. **PR.** The branch is pushed and a (draft) PR is opened from a template,
   linked to the ticket (`Closes #n` for GitHub and GitLab issues). On
   GitLab that is a merge request; everything below applies the same way.
8. **Monitor.** The PR is polled. A merge conflict is merged with git or, if
   that fails, by an agent session. New review comments or a red CI start a
   fix session driven by the review or CI prompt template, with the failing
   job's log included; failed Actions jobs are re-run once first, so a
   flaky job costs no session. A commit status named `loop` on the PR
   shows the phase, fix rounds and outcome next to CI. The verify steps run again before the result is
   pushed, and every inline review thread gets a reply with what was done;
   threads the agent reports as done are resolved. Once CI is green a draft PR is marked ready for review.
   If a person pushes to the branch, loop stops driving the PR and leaves
   it to them (`workflow.on_human_push`).
9. **Merge.** Depending on `workflow.merge`: never (`manual`), `when-green`,
   `when-green-and-approved`, or leave it to the host (`auto-merge`).
   Optional gates (`before-pr`, `before-fix`, `before-merge`) pause the run
   until `loop approve <run>`, or until a collaborator comments
   `/loop approve` on the PR.
10. **Close.** The item is done when the ticket is closed. loop closes it
   after the merge (or the host does through the closing keyword), then removes
   the worktree and the remote branch. A [recurring item](#recurring-tasks)
   stays open and is picked up again when it is due.

Every step is idempotent and persisted in `.loop/runs/<run>/run.yaml`, so a
crashed `loop watch` simply continues where it stopped. Runs that exhaust
their fix rounds, are held by branch protection, or hit an unrecoverable
error are parked as `blocked` or `failed` with a note on the PR or ticket,
and `steps.blocked` or `steps.failed` can notify you; `loop resume <run>`
puts them back. `budget` caps cost and agent time per run and per day, so
a worker left alone cannot spend more than you decided; `loop stats` shows
what it did spend.

## Commands

| Command | What it does |
| --- | --- |
| `loop init [dir]` | Scaffold a project. |
| `loop doctor` | Check config, tools, credentials, repository and sources before running. |
| `loop list [-s source] [--ready] [--json]` | Open items in pick-up order with schedule and status. |
| `loop show <item> [--prompt]` | Item details, or the rendered session prompt. |
| `loop run <item> [--force] [--no-watch] [--dry-run]` | Run one item to completion; `--dry-run` only prints what it would do. |
| `loop run --all [-s source]` | Run every ready item, respecting `workflow.concurrency`. |
| `loop watch [--pick] [dir...]` | Drive all active runs, of one or several projects; with `--pick` also start ready items. |
| `loop status [-a]` | Runs and phases. |
| `loop stats [--json]` | Merged PRs, fix rounds, cost, agent time and today's spend against `budget`. |
| `loop logs <run> [--session] [-f]` | Run log, or an agent session transcript or prompt. |
| `loop approve <run>` | Continue past a gate. |
| `loop resume <run>` | Re-activate a blocked or failed run. |
| `loop join <run>` | Print the command to continue the agent session by hand. |
| `loop clean [run]` | Remove workdirs of finished runs. |

Items are addressed by anything a source understands: `login.md`, `#12`,
`owner/repo#12`, `PROJ-7`, a ticket URL, or the full id `backlog:login`.

## Dependencies between items

Everywhere (markdown, GitHub, GitLab, Jira) a line in the description that starts
with `depends on:` lists references, separated by commas or spaces:

```
depends on: auth.md, #12, PROJ-7
```

Markdown items can also use `depends_on:` in the frontmatter, and Jira
"is blocked by" / "depends on" links are honoured. References may cross
sources. An item is blocked until every referenced item is closed; `loop run
--force` overrides that and the in-progress check.

## Recurring tasks

An item with an `every:` line runs on a schedule instead of once: a weekly
dependency bump, a nightly lint sweep, a monthly docs check.

```
---
title: Bump dependencies
every: 7d
---
Update every dependency to its latest compatible version and run the tests.
```

The same line works in the body of a GitHub, GitLab or Jira ticket, so a
pinned issue can be the home of a recurring task and collect its history
as comments. Intervals are `7d`, `2w`, `36h`, or `hourly`, `daily`,
`weekly`, `fortnightly`, `monthly`.

Every occurrence is an ordinary run: fresh branch (with the date in its
name), session, verify, PR, monitor, merge. What differs is the end: the
ticket is not closed and not linked with `Closes #n`; loop notes the merge
and the next due time on it instead. A session that finds nothing to
change ends the run as done without a PR ("no changes"), not as a failure.
`loop watch --pick` and `loop run --all` start a recurring item once its
interval has passed since the previous run started; `loop run <item>`
starts it at any time. While a previous occurrence is still running, or
parked with its PR open, the next one waits. Nothing can `depends on:` a
recurring item, because it never closes. See
[workflow](docs/workflow.md#recurring-items).

## Documentation

The same pages are published as a website built from the `website/` folder
(see [website/README.md](website/README.md)).

- [Command reference](docs/cli.md)
- [Security](docs/security.md): what the agent can do, and prompt injection from tickets
- [Configuration reference](docs/configuration.md)
- [Prompt templates](docs/prompts.md), including session templates
  [with](docs/examples/session-with-comments.md) and
  [without](docs/examples/session-without-comments.md) ticket comments
- [Workflow, gates and merge policies](docs/workflow.md)

## Development

```sh
make build   # ./bin/loop
make test
make lint    # gofmt, go vet, staticcheck
```

Releases are built by GoReleaser when a `v*` tag is pushed
(`.github/workflows/release.yml`); `make build` stamps the version from
`git describe`. Dependabot opens weekly, grouped update PRs for Go modules, the website's
npm packages and the GitHub Actions used in the workflows
(`.github/dependabot.yml`). CI runs on those PRs like on any other.
