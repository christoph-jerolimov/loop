# Workflow, gates and merge policies

## Phases

```
queued → checkout → setup → session → verify → [gate before-pr] → pr
      → monitor ⇄ [gate before-fix] fix → [gate before-merge] merge → close → cleanup → done
```

`monitor` polls the PR every `workflow.poll_interval` and decides in this
order:

1. **Merged** → `close`. **Closed without merge** → `blocked`.
2. **Conflict** (`mergeable: false`) → `fix` with reason `conflict`:
   `git merge origin/<base>`; if that conflicts, a session with the
   `conflict` template completes the merge. At most
   `workflow.conflict_attempts`.
3. **New feedback** (reviews requesting changes, inline comments, PR
   comments, not written by loop itself and not handled before) → `fix`
   with the `review` template. Handled comment ids are remembered, so the
   same comment never triggers twice.
4. **Red CI** on the current head → `fix` with the `ci` template, once per
   head commit. For checks that are GitHub Actions jobs, the last
   `workflow.ci_log_lines` lines of the job log are part of the prompt, with
   timestamps and colour codes stripped.
5. **Pending CI** → wait.
6. **Green**: a draft PR is marked ready for review. Then, by policy:
   - `manual`: keep watching until a human merges.
   - `when-green`: merge now.
   - `when-green-and-approved`: merge when the latest review of every
     reviewer is an approval and at least one exists.
   - `github-auto-merge`: auto-merge was enabled on the PR when it was
     opened; GitHub merges when its rules pass. loop keeps watching.

Each `fix` round runs `steps.verify` before it pushes, so a fix can never
push what the initial round would have rejected; a failing verify step
starts a `verify` session like it does after the initial session. Then
the round pushes a commit and comments on the PR. Review, CI and verify
rounds share `workflow.fix_rounds`; when they are used up the run is
`blocked` with a note on the PR.

Before merging, loop reads the PR's `mergeable_state`. When branch
protection holds a green PR (`blocked`: required reviewers loop cannot
satisfy, a required check that never reports, or a "branches must be up to
date" rule), the run parks as `blocked` with one note on the PR instead of
retrying every poll. A merge GitHub rejects for another reason is retried
up to three times, then the run parks the same way. After a human resolves
the cause, `loop resume <run>` continues to the merge.

`close` runs the source's close action (`close_issue_on_merge: true`), then
waits until the source reports the item closed. `cleanup` runs
`steps.cleanup`, then removes the worktree, the local branch and, after a
merge, the remote branch.

## Gates

`workflow.gates` lists points where the run parks until a human confirms:

- `before-pr`: after verify, before pushing and opening the PR.
- `before-fix`: before every fix round.
- `before-merge`: before loop merges (only meaningful for the merge
  policies that merge).

`loop run` on a terminal asks interactively. Otherwise the run waits and
`loop status` shows the gate; `loop approve <run>` lets it continue on the
next `loop watch` tick.

## Before the first run

`loop doctor` checks the configuration, the tools on the `PATH`, the
repository, the credentials and every source, and exits non-zero when
something is missing. See the [command reference](cli.md).

## Process model

- `loop run <item>` drives one run in the foreground until the ticket is
  closed. Ctrl-C leaves the run in its current phase.
- `loop run --all` starts ready items in backlog order, at most
  `workflow.concurrency` at a time in the agent phases, and returns when
  everything is done or parked.
- `loop watch` is the long-running worker: it drives every active run and,
  with `--pick`, starts new ones whenever capacity is free.

Runs are locked per process (`.loop/runs/<run>/lock`), so several loop
processes on the same project never drive the same run.

## Retries and limits

| Limit | Default | When exceeded |
| --- | --- | --- |
| `agent.attempts` | 2 | run `failed`, claim released, note on ticket |
| `agent.timeout` / `max_turns` | 45m / 200 | the session counts as a failed attempt |
| `workflow.fix_rounds` | 3 | run `blocked`, note on PR |
| `workflow.conflict_attempts` | 1 | run `blocked`, note on PR |

Whenever a run parks, `steps.blocked` or `steps.failed` run with
`LOOP_RUN_ERROR` set to the reason, so a script can post to Slack, send an
email or open a ticket. `loop resume <run>` clears the error and puts the
run back into `monitor` (when it has a PR) or `session`. `loop logs <run>` shows what happened,
and `loop logs <run> --session` the transcript of the last agent session.
