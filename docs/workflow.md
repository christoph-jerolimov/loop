# Workflow, gates and merge policies

## Phases

```
queued → checkout → setup → [plan] → [gate before-code] → session → verify → [self-review] → [gate before-pr] → pr
      → monitor ⇄ [gate before-fix] fix → [gate before-merge] merge → close → cleanup → done
```

`plan` is optional (`workflow.plan: true`): a session explores the
repository without changing it and writes a short plan with an estimate,
which loop posts on the ticket. See [plan before code](#plan-before-code).

`monitor` polls the PR every `workflow.poll_interval` and decides in this
order:

1. **Merged** → `close`. **Closed without merge** → `blocked`.
2. **A human pushed** (the head moved since loop's last push and the new
   commits were not made by loop's account) → the worktree is
   fast-forwarded to the branch and, with `workflow.on_human_push: pause`
   (the default), the run parks as `blocked` with a note on the PR so loop
   never builds on top of a person's work unasked. `loop resume <run>` or a
   `/loop resume` comment continues from the new head. With `continue`,
   loop keeps driving on top of their commits. Commits by loop itself,
   from another process for example, never count.
3. **Conflict** (`mergeable: false`) → `fix` with reason `conflict`:
   `git merge origin/<base>`; if that conflicts, a session with the
   `conflict` template completes the merge. At most
   `workflow.conflict_attempts`.
4. **New feedback** (reviews requesting changes, inline comments, PR
   comments, not written by loop itself and not handled before) → `fix`
   with the `review` template. Handled comment ids are remembered, so the
   same comment never triggers twice. After the round loop replies in each
   inline thread with what the session did and resolves the threads the
   session reported as done (see [prompts](prompts.md#answering-reviewers)).
5. **Red CI** on the current head → first, with `workflow.ci_rerun: true`
   (the default), the failed GitHub Actions jobs are re-run (the GitLab
   pipeline is retried) once for this head and loop looks again on the
   next poll, so a flaky job does not cost an agent session. Still red →
   `fix` with the `ci` template, once per head commit. For checks that are
   CI jobs, the last `workflow.ci_log_lines` lines of the job log are part
   of the prompt, with timestamps and colour codes stripped.
6. **Pending CI** → wait.
7. **Green**: a draft PR is marked ready for review. Then, by policy:
   - `manual`: keep watching until a human merges.
   - `when-green`: merge now.
   - `when-green-and-approved`: merge when the latest review of every
     reviewer is an approval and at least one exists.
   - `auto-merge`: the host's auto-merge was enabled on the PR when it was
     opened (GitHub's auto-merge, GitLab's merge when pipeline succeeds);
     the host merges when its rules pass. loop keeps watching.

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
retrying every poll. A merge the host rejects for another reason is retried
up to three times, then the run parks the same way. After a human resolves
the cause, `loop resume <run>` continues to the merge.

`close` runs the source's close action (`close_issue_on_merge: true`), then
waits until the source reports the item closed; for a
[recurring item](#recurring-items) it only releases the claim. `cleanup`
runs `steps.cleanup`, then removes the worktree, the local branch and,
after a merge, the remote branch. A done run records its outcome in
`run.yaml`: `merged`, or `no-changes` for a recurring run that opened no
PR.

## Recurring items

An item with `every: 7d` (frontmatter or a body line, see
[configuration](configuration.md#recurring-items)) is a task that repeats.
Each occurrence is a run like any other, with two differences:

- **Start.** `loop watch --pick` and `loop run --all` start the item when
  it is due: never run yet, or the interval has passed since the previous
  run started. `loop run <item>` starts it at any time and is the manual
  trigger. The branch carries the date of the occurrence
  (`loop/<id>-<title>-20260915`), so each occurrence has its own branch and
  PR. While a previous occurrence is active, or parked as `blocked` with
  its PR still open, the item waits; `loop list` says so. Nothing can
  depend on a recurring item, because it never closes.
- **End.** After the merge the ticket stays open: loop releases the claim
  and notes the merge and the next due time on it. The PR body has no
  `Closes #n`. When the session commits nothing, the run ends as `done`
  with outcome `no-changes`, without a PR, and the ticket gets a note;
  for a one-shot item the same session would be a failed attempt.

The session template gets `.Item.Every` and `.Previous` (run id, outcome,
PR and summary of the previous occurrence), so the agent knows what last
week's run did. `budget.daily_runs` and `budget.daily_cost` cap recurring
runs like any other, which also bounds the burst after a lost `.loop`
folder makes every recurring item due at once.

## Plan before code

Steering a plan costs a person a minute; steering a finished PR costs an
hour. With `workflow.plan: true` every run starts with a planning session
driven by the `plan` template: the agent reads the ticket and the
repository, changes nothing, and writes goal, approach, files to touch,
tests, risks and an S/M/L estimate into `plan.md` in the run folder. loop
posts the plan as a comment on the ticket and hands it to the
implementation session as `.Plan`, so the session follows what was
approved.

Add `before-code` to `workflow.gates` to make the run wait for a human
after the plan. `loop approve <run>` continues it; for GitHub or GitLab
issues of the repository a `/loop approve` comment on the issue by a collaborator
with push access does the same. Anything the plan session leaves in the
worktree is discarded before the implementation starts. The gate works
without the plan too: it then simply holds the run before the session.

## Self-review before the PR

With `workflow.self_review: true` a second session reviews the branch
before it becomes a pull request. It gets the ticket and the diff against
the base (driven by the `self_review` template), may run the tests, but
changes nothing; it writes its findings into `findings.md` in the run
folder, or `No findings.`. Findings are handed to one fix round with the
`review` template, the verify steps run again, and only then is the PR
opened. The round is free: it does not count against `workflow.fix_rounds`,
and it runs once per run. Reviewers see fewer obvious mistakes; the cost is
one extra session per run.

## Loop's status on the PR

With `workflow.pr_status: true` (the default) loop posts a commit status
named `loop` on the PR head after every step that changes something:
`pending` while it monitors (with the fix rounds used), waits at a gate,
runs a fix round or merges; `success` once the PR is merged and the
ticket closed; `error` when the run is blocked and `failure` when it
failed, each with the reason. Reviewers see it in the checks list of the
PR next to CI, and `loop resume` updates it again. A commit status works
with a user token, which a check run would not. loop ignores its own
status when it decides whether CI is green, and branch protection ignores
it too unless you make `loop` a required status on purpose.

## API errors and rate limits

Every GitHub, GitLab and Jira call retries network errors, 5xx responses and
rate-limit responses (429, or 403 with the rate limit exhausted) up to four
times with exponential backoff and jitter, honouring `Retry-After` and
`X-RateLimit-Reset`. A rate limit that asks for more than two minutes is
not waited out inside the call; the run's next poll is scheduled at the
reset time instead. Other repeated poll failures stretch the poll interval
exponentially up to fifteen minutes and reset on the first success, so a
long outage neither hammers the API nor stops the run.

## Gates

`workflow.gates` lists points where the run parks until a human confirms:

- `before-code`: after setup and the optional plan, before the
  implementation session. There is no PR yet, so the note goes on the
  ticket; for a GitHub or GitLab issue of the repository, `/loop approve`
  works as a comment on the issue.
- `before-pr`: after verify, before pushing and opening the PR.
- `before-fix`: before every fix round.
- `before-merge`: before loop merges (only meaningful for the merge
  policies that merge).

`loop run` on a terminal asks interactively. Otherwise the run waits,
`loop status` shows the gate, and loop leaves a note on the PR. Two ways
to continue:

- `loop approve <run>` where loop runs.
- A comment `/loop approve` on the pull request from a collaborator with
  push access (admin, maintain or write). loop reacts with a thumbs up
  and continues on the next poll; commands from anyone else get a
  "confused" reaction and are ignored.

A blocked run with a PR can likewise be restarted with a `/loop resume`
comment. `workflow.pr_commands: false` turns both commands off.

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
  with `--pick`, starts new ones whenever capacity is free. It reports
  what it works on, what it waits for and why nothing is picked whenever
  that changes, so an idle watch is not silent about why.

Runs are locked per process (`.loop/runs/<run>/lock`), so several loop
processes on the same project never drive the same run.

## What stays on disk

A finished run keeps its folder under `.loop/runs` for good: `run.yaml`,
the run log, every session transcript and the prompt each session was
started from, so `loop logs` and `loop stats` work for old runs too. Its
checkout does not: the cleanup phase removes it when the item is closed,
and [`retention.workdirs`](configuration.md#retention) (7 days by default)
removes the checkouts of blocked and failed runs that nobody came back
to, from `loop watch` every ten minutes or from `loop clean --older-than`.
A run resumed after that checks its branch out again from the remote and
runs the setup steps before it continues, so a blocked PR can be picked
up weeks later without keeping a worktree around in the meantime.

## Contributing through a fork

Without push access to a repository, set `repo.fork` to a fork you can
push to. loop clones and reads from `repo.url`, pushes every branch to the
fork (`push_url`), opens pull requests (merge requests on GitLab) from the
fork against the base branch of the original repository, and deletes the
branch on the fork after the merge. Merge policies other than `manual` need push access to
the original repository; `loop doctor` warns when that is missing.

```yaml
repo:
  url: git@github.com:acme/widgets.git
  fork: me/widgets
```

## Watching several projects

`loop watch` accepts project folders and drives all of them in one
process, each with its own configuration, sources and concurrency; output
lines carry the project name:

```sh
loop watch --pick ~/loops/*
```

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
