# Command reference

Every command looks for `loop.yaml` in the current folder or any parent;
`-C <dir>` points it elsewhere. `--runner <name>` (or `LOOP_RUNNER=<name>`)
selects the agent harness for this invocation instead of the one in
`loop.yaml`; see [harnesses](configuration.md#harnesses). Items are
addressed by anything a source understands: `login.md`, `#12`,
`owner/repo#12`, `PROJ-7`, a ticket URL, or the full id `backlog:login`.
Runs are addressed by run id, a unique prefix of it, or the item id.

## `loop --version`

Prints the release version (`dev` for source builds without `make`).

## `loop init [dir]`

Scaffold a project: `loop.yaml`, `prompts/` with the built-in templates,
`backlog/example.md`, `hooks/setup.sh` and a `.gitignore` for `.loop/`.
Existing files are kept unless `--force` is given.

## `loop doctor`

Run every check a run depends on, before any worktree is created:

| Group | Checks |
| --- | --- |
| Configuration | `loop.yaml` parses and validates; prompt templates render; step scripts exist and are executable; agent prompts and skill folders exist. |
| Tools | `git` and the harness command (`claude`, `agent` for Cursor, `codex`, `gemini`, `aider`, `opencode`, `copilot`, `amp`, or the custom command) are on the `PATH`; for Claude a warning when `agent.allow` holds only the default git rules or `permission_mode` bypasses all checks. |
| Repository | `repo.url` is reachable and has the base branch. |
| Credentials | The GitHub token is accepted, can push to the repository, and the default branch matches `repo.base` (warning otherwise). Jira credentials are accepted for every Jira site. |
| Sources | Every source can be listed; the number of open items is shown, and for GitHub sources whether ticket comments are loaded and why. |
| State | `.loop/` can be created. |

Exits non-zero when any check fails. Run it after editing `loop.yaml` and
whenever a run fails early.

## `loop list`

Open items in pick-up order with a status column: `ready`, `blocked by …`,
`in progress` or `running (<phase>)`.

| Flag | Meaning |
| --- | --- |
| `-s, --source <name or type>` | Only one source. |
| `--ready` | Only items that can be started now. |
| `--json` | Machine-readable output including the readiness details. |

## `loop show <item> [--prompt]`

Item details, or with `--prompt` the rendered session prompt exactly as an
agent would receive it.

## `loop run <item>`

Start a run for one item and drive it in the foreground until the ticket
is closed. Items with open dependencies or an in-progress marker are
refused unless `--force` is given. On a terminal, gates ask interactively.

| Flag | Meaning |
| --- | --- |
| `--force` | Ignore open dependencies and in-progress markers. |
| `--no-watch` | Return once the PR is opened; monitor later with `loop watch`. |
| `--all` | Start every ready item in backlog order, respecting `workflow.concurrency`, and return when nothing is left. |
| `-s, --source` | With `--all`: only items from this source. |

## `loop watch [--pick] [project-dir...]`

The long-running worker: drives every active run (polls PRs, runs fix
rounds, merges, closes, cleans up). With `--pick` it also starts ready
backlog items whenever capacity is free. Given project folders, it watches
all of them at once and prefixes output with the project name:
`loop watch --pick ~/loops/*`.

## `loop status [-a]`

Active runs with phase, branch, PR and the current note (gate, next poll,
error). `-a` includes finished runs.

## `loop stats [--json]`

Every run in the project summarised: runs per phase, merged PRs, mean fix
rounds per PR, sessions and agent time, cost in total and per merged PR
(for harnesses that report it, Claude Code does), the mean time from
start to done, and today's runs and spend against `budget`.

## `loop logs <run>`

Without flags, the run log: every phase change, session start and end,
push, poll result and error.

| Flag | Meaning |
| --- | --- |
| `--session[=N]` | The raw transcript of an agent session instead, by default the latest; `--session=2` selects the second. |
| `--prompt` | The prompt file the selected session was started from, exactly as the agent loaded it. |
| `-f, --follow` | Keep printing as the file grows; the easiest way to watch a running session. |
| `-n, --lines N` | Only the last N lines. |

## `loop approve <run>`

Let a run continue past the gate it is waiting at. It continues on the
next `loop watch` tick. A collaborator with push access can do the same
from the pull request with a `/loop approve` comment.

## `loop resume <run>`

Put a `blocked` or `failed` run back into the workflow: into `monitor`
when it has a PR, otherwise into a new agent session. A `/loop resume`
comment on the PR from a collaborator with push access does the same.

## `loop join <run>`

Print the command that opens the run's workdir and resumes its latest
agent session, for example `cd .loop/workdirs/… && claude --resume <id>`.

## `loop clean [run]`

Remove the workdirs of finished runs, or of one run. `--force` also cleans
an active run and marks it blocked.
