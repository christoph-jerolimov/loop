# Command reference

Every command looks for `loop.yaml` in the current folder or any parent;
`-C <dir>` points it elsewhere. Items are addressed by anything a source
understands: `login.md`, `#12`, `owner/repo#12`, `PROJ-7`, a ticket URL, or
the full id `backlog:login`. Runs are addressed by run id, a unique prefix
of it, or the item id.

## `loop init [dir]`

Scaffold a project: `loop.yaml`, `prompts/` with the built-in templates,
`backlog/example.md`, `hooks/setup.sh` and a `.gitignore` for `.loop/`.
Existing files are kept unless `--force` is given.

## `loop doctor`

Run every check a run depends on, before any worktree is created:

| Group | Checks |
| --- | --- |
| Configuration | `loop.yaml` parses and validates; prompt templates render; step scripts exist and are executable; agent prompts and skill folders exist. |
| Tools | `git` and the agent CLI (`claude`, or `agent` for Cursor) are on the `PATH`. |
| Repository | `repo.url` is reachable and has the base branch. |
| Credentials | The GitHub token is accepted, can push to the repository, and the default branch matches `repo.base` (warning otherwise). Jira credentials are accepted for every Jira site. |
| Sources | Every source can be listed; the number of open items is shown. |
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

## `loop watch [--pick]`

The long-running worker: drives every active run (polls PRs, runs fix
rounds, merges, closes, cleans up). With `--pick` it also starts ready
backlog items whenever capacity is free.

## `loop status [-a]`

Active runs with phase, branch, PR and the current note (gate, next poll,
error). `-a` includes finished runs.

## `loop approve <run>`

Let a run continue past the gate it is waiting at. It continues on the
next `loop watch` tick.

## `loop resume <run>`

Put a `blocked` or `failed` run back into the workflow: into `monitor`
when it has a PR, otherwise into a new agent session.

## `loop join <run>`

Print the command that opens the run's workdir and resumes its latest
agent session, for example `cd .loop/workdirs/… && claude --resume <id>`.

## `loop clean [run]`

Remove the workdirs of finished runs, or of one run. `--force` also cleans
an active run and marks it blocked.
