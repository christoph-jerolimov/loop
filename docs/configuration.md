# Configuration reference (`loop.yaml`)

Paths are relative to the folder containing `loop.yaml`. Durations use Go
syntax (`45m`, `1h30m`, `90s`) plus `d` for days and `w` for weeks (`2d`,
`1w`).

## Top level

| Key | Default | Description |
| --- | --- | --- |
| `name` | folder name | Project name, available as `{{ .Project }}` in templates. |

## `repo`

| Key | Default | Description |
| --- | --- | --- |
| `url` | required | Clone URL of the target repository. |
| `base` | `main` | Base branch for worktrees and pull requests. |
| `workdir` | `worktree` | `worktree` keeps one base clone in `.loop/repo` and adds a worktree per run; `clone` makes a full clone per run. |
| `branch_prefix` | `loop/` | Prefix for run branches. |
| `github` | derived from `url` | `owner/name` used for the GitHub API. Set it when the URL is not a github.com URL. |
| `gitlab` | derived from `url` | Project path (`group/subgroup/project`) used for the GitLab API. Derived when the URL's host contains `gitlab`; set it for other hosts. Exactly one of `github` and `gitlab` is set; it decides where pull requests (merge requests on GitLab) open. |
| `gitlab_url` | `https://<host of url>` | The GitLab instance, for self-hosted GitLab. |
| `fork` | none | `owner/name` of a fork to push branches to when you have no push access to the repository. Pull requests open from the fork against the repository. |
| `push_url` | derived from `url` and `fork` | Git URL of the fork; set it when loop cannot rewrite `url` (it handles `https://`, `ssh://` and `git@host:` URLs). |

Credentials: `GITHUB_TOKEN` (or `gh auth login`) for GitHub, `GITLAB_TOKEN`
(a personal or project access token with the `api` scope) for GitLab. Set
`GITHUB_API_URL` for GitHub Enterprise.

On GitLab, loop opens merge requests, reads approvals, reviewer states
(`requested_changes`), diff discussions and notes, pipeline jobs and
external commit statuses, retries a failed pipeline once, replies in and
resolves discussions, posts its own status on the commit, and merges with
`squash` when `merge_method` is `squash`; `rebase` and `merge` leave the
project's merge method in charge. `pr.reviewers` are GitLab usernames.

## `sources`

An ordered list. Order matters: it is the pick-up order.

Common keys:

| Key | Default | Description |
| --- | --- | --- |
| `name` | the type | Unique name; item ids are `<name>:<native id>`. |
| `type` | required | `markdown`, `github`, `gitlab` or `jira`. |
| `labels` | none | Only items carrying all of these labels are listed. Recommended: `[ready-for-agent]`. |
| `claim` | `false` (`true` when `claim_label` is set) | Mark items in progress so two loops never pick the same one. |
| `claim_label` | `loop:in-progress` | Label used by GitHub, GitLab and Jira for the claim. |
| `comments` | `writers` for GitHub and GitLab, `all` otherwise | Whose ticket comments are loaded into the template data (`.Item.Comments`): `all`, `writers` (GitHub and GitLab only: the repository owner and collaborators with write access, or members with developer access, checked per author) or `none`. See [security](security.md#prompt-injection-from-tickets). |

### Priority

Items with a priority are picked before items without, highest first,
and only then does source order and age decide. The spellings are the
same everywhere: the numbers `1` (highest) to `9`, `P0` to `P9`, or the
words `highest`, `critical`, `blocker`, `urgent`, `high`, `medium`,
`normal`, `major`, `low`, `minor`, `lowest`, `trivial`. Where it comes
from:

- markdown: a `priority:` frontmatter key.
- GitHub: a label such as `priority: high`, `priority/high`, `prio-2` or
  `P1`; the highest one wins when several match.
- Jira: the issue's priority field.
- any source: a body line `priority: high`, like `depends on:` and
  `model:`.

`loop list` shows the priority in its own column.

### Recurring items

An item with a schedule is picked up again and again instead of once:

- markdown: an `every:` frontmatter key.
- any source: a body line `every: 7d`, like `depends on:` and `model:`.

The value is a duration in Go syntax extended with `d` (days) and `w`
(weeks), for example `7d`, `2w`, `36h`, `1d12h`, or one of the words
`hourly`, `daily`, `weekly`, `fortnightly`, `monthly` (30 days). Anything
below a minute is rejected; `loop doctor` fails on a schedule that does
not parse and `loop list` shows it as `invalid schedule`.

A recurring item is due when the interval has passed since its previous
run started, counted from `.loop/runs`; a run that failed counts too, so a
broken task does not restart every tick. It is never closed: after the
merge loop releases the claim, notes the merge and the next due time on
the ticket, and the PR carries no `Closes #n`. A session without commits
ends the run as `done` with outcome `no-changes` instead of failing. See
[workflow](workflow.md#recurring-items) for the whole lifecycle.

### `type: markdown`

| Key | Default | Description |
| --- | --- | --- |
| `path` | `backlog` | Folder of `*.md` files. |

Frontmatter keys: `id`, `title`, `status` (`open`, `in-progress`, `closed`),
`created`, `labels`, `depends_on`, `model`, `priority`, `every`,
`loop_run`. Without a title the first `# heading` or the file name is used. Claiming writes
`status: in-progress` and `loop_run`; closing writes `status: closed` and
`closed_note`. These edits touch only the keys loop owns: every other key
keeps its position, quoting and comments, and a file without frontmatter
gets a minimal one. Loop notes are appended under a `## Loop log` heading.

### `type: github`

| Key | Default | Description |
| --- | --- | --- |
| `repo` | `repo.github` | `owner/name` whose issues form the backlog. |

Claiming adds the label and a comment with the run id. Closing happens
through `Closes #n` in the PR body or explicitly after the merge.

### `type: gitlab`

| Key | Default | Description |
| --- | --- | --- |
| `repo` | `repo.gitlab` | Project path (`group/project`) whose issues form the backlog. |
| `url` | `repo.gitlab_url` | The GitLab instance. |

Issues are listed with `scope: all`, so issues of others count too. Labels
give the priority the same way as on GitHub (`P1`, `priority::high`, ...).
Claiming adds the label and a note with the run id. Closing happens through
`Closes #n` in the merge request description or explicitly after the merge.
`/loop approve` and `/loop resume` work as notes on the issue and on the
merge request for members with developer access or more. Credentials:
`GITLAB_TOKEN`.

### `type: jira`

| Key | Default | Description |
| --- | --- | --- |
| `url` | required | Site URL, e.g. `https://acme.atlassian.net`. |
| `jql` | required | Query for the backlog. Labels from `labels` are appended. |
| `transitions.in_progress` | none | Transition (or target status) to run on claim. |
| `transitions.done` | `Done` | Transition to run when the PR merged. |

Credentials: `JIRA_EMAIL` + `JIRA_API_TOKEN` (Cloud, basic auth) or
`JIRA_TOKEN` (bearer, Server/Data Center).

## `prompts`

Paths to template files; missing keys use the built-in defaults
(`loop init --prompts` writes editable copies to `prompts/`).

| Key | Used for |
| --- | --- |
| `session` | The initial implementation session. |
| `plan` | The planning session before the implementation (`workflow.plan: true`). |
| `self_review` | The review of the branch before the PR opens (`workflow.self_review: true`). |
| `review` | Fix rounds triggered by reviews and PR comments. |
| `ci` | Fix rounds triggered by failing checks. |
| `conflict` | Resolving merge conflicts git could not resolve. |
| `verify` | Fixing a failing verify step. |

See [prompts.md](prompts.md).

## `agent`

| Key | Default | Description |
| --- | --- | --- |
| `runner` | `claude` | The harness: `claude`, `cursor`, `codex`, `gemini`, `aider`, `opencode`, `copilot`, `amp` or `custom`. `--runner` and `LOOP_RUNNER` override it per invocation. See [harnesses](#harnesses). |
| `command` | profile default | Executable override (a name on the `PATH` or a path). |
| `args` | profile default | Argument template; required for `custom`. See [harnesses](#harnesses). |
| `prompt_via` | profile default | How the prompt reaches the CLI: `stdin`, `args` or `both`. |
| `session_id` | profile default | How loop learns the resumable session id: `uuid`, `none`, `run:<args>` or `json:<key>`. |
| `resume` | profile default | Command a human runs in the workdir to continue the session; `loop join` prints it. |
| `model` | runner default | Model for every session. Items override it with a `model:` line or frontmatter. |
| `permission_mode` | `acceptEdits` | Claude only: `default`, `acceptEdits`, `bypassPermissions` or `plan`. Written to the workdir settings and passed to `claude --permission-mode`. Other harnesses run with their own auto-approve flag (see below). |
| `allow` | git defaults | Claude only: permission rules the headless session may use without prompting, for example `Bash(npm test:*)`. See below. |
| `deny` | `git push`, `gh pr`, `gh api`, `git reset --hard` | Claude only: rules the session may never use. |
| `timeout` | `45m` | Wall clock limit per session. |
| `max_turns` | `200` | Passed to `claude --max-turns`; other profiles ignore it unless their `args` use `{max_turns}`. |
| `attempts` | `2` | Attempts for the initial session before the run fails. |
| `skills` | none | Folders symlinked into `<workdir>/.claude/skills/`. The links are kept out of git through the repository's `info/exclude`. |
| `env` | none | Extra environment variables for sessions and steps. |
| `env_passthrough` | none | Variable names or globs (`DATABASE_URL`, `MY_APP_*`) sessions inherit from loop's environment on top of the built-in allowlist. See below. |
| `extra_args` | none | Extra CLI arguments, appended after the template. |

### Harnesses

One generic runner drives every harness. A profile is four things: the
command line, how the prompt travels, how the session id is learned, and
how a human resumes the session. The built-in profiles:

| `runner` | Command line | Prompt | Session id, resume |
| --- | --- | --- | --- |
| `claude` | `claude -p --output-format stream-json --verbose --session-id {session} --permission-mode {permission_mode} --model {model} --max-turns {max_turns}` | stdin | pre-assigned UUID; `claude --resume {session}` |
| `cursor` | `agent -p --force --output-format stream-json --resume {session} --model {model} {prompt}` | both | `agent create-chat` first; `agent --resume {session}` |
| `codex` | `codex exec --json --full-auto --model {model} {prompt}` | args | `thread_id` from the JSON output; `codex resume {session}` |
| `gemini` | `gemini --output-format stream-json --yolo --model {model} -p {prompt}` | args | `session_id` from the JSON output; `gemini --resume {session}` |
| `aider` | `aider --message-file {prompt_file} --yes-always --no-check-update --model {model}` | args (file) | none; `aider` reopens the folder's chat history |
| `opencode` | `opencode run --format json --model {model} {prompt}` | args | `sessionID` from the JSON output; `opencode --session {session}` |
| `copilot` | `copilot --allow-all-tools --model {model} -p {prompt}` | args | none; `copilot --resume` |
| `amp` | `amp -x --stream-json --dangerously-allow-all` | stdin | none; `amp threads continue` |

Placeholders in `args`: `{prompt}` (the prompt text, or a one-line pointer
at the prompt file when the text exceeds 16 KiB, so the command line never
hits the operating system's argument limit), `{prompt_file}`, `{model}`,
`{max_turns}`, `{permission_mode}`, `{session}` and `{workdir}`. An
argument that is only a placeholder without a value disappears together
with the flag before it, which is why `--model {model}` is harmless when no
model is configured.

`prompt_via` says whether the prompt file's content is piped on standard
input (`stdin`), substituted into the arguments (`args`), or both (`both`:
the content on stdin and `{prompt}` replaced by the pointer, for CLIs that
read either). `session_id` is `uuid` (loop generates the id and
substitutes `{session}` before the start), `run:<args>` (loop runs the
command with these arguments first and takes its output as the id),
`json:<key>` (the id is read from that key of any JSON line the CLI prints)
or `none`.

Claude Code and Cursor are exercised by loop's own tests against fake CLIs
that speak their protocols. The other profiles follow the headless modes
those CLIs document; every field can be overridden, so a renamed flag is a
one-line change in `loop.yaml` rather than a new loop release:

```yaml
agent:
  runner: codex
  command: /opt/codex/bin/codex   # override just the executable
  resume: "codex exec resume {session}"
```

A harness loop does not know is a `custom` runner. `command` and `args`
are required; `prompt_via` defaults to `stdin`, `session_id` to `none`:

```yaml
agent:
  runner: custom
  command: ./hooks/agent.sh
  args: ["--task", "{prompt_file}", "--model", "{model}"]
  prompt_via: args
  session_id: json:session   # the script prints {"session": "..."} on stdout
  resume: "./hooks/agent.sh --continue {session}"
```

The script runs in the workdir with the session environment (see below)
plus `LOOP_PROMPT_FILE`, must commit its work, and signals failure with a
non-zero exit code. JSON lines on stdout are optional: a Claude-style
`{"type":"result","is_error":false}` line reports the outcome, an
`assistant` message or an `item` with a `text` shows up as progress, and
the `session_id` key is read from any line.

`--runner <name>` on any command, or `LOOP_RUNNER=<name>` in the
environment, selects the harness for that invocation without editing
`loop.yaml`; the flag wins over the variable. Both accept a built-in name
or `custom`, and the rest of `agent` stays as configured.

Authentication is the harness's own business: `loop doctor` checks that
the command is on the `PATH`, and each CLI reads its credentials from its
usual place (`ANTHROPIC_*`, `OPENAI_*`, `GEMINI_*`, `GOOGLE_*`, `CURSOR_*`,
`COPILOT_*`, `AMP_*` and the like are passed through). Copilot CLI
authenticates with `copilot login`; loop withholds `GITHUB_TOKEN` and
`GH_TOKEN` from sessions on purpose, so do not expect them there.

### Permissions in headless sessions

A headless `claude -p` session cannot ask for permission: a tool call the
rules do not allow is denied, and an agent that cannot run the tests or
commit stalls or gives up. Before every session loop writes
`<workdir>/.claude/settings.local.json` (kept out of git) with
`permission_mode` as the default mode and the `allow` and `deny` rules, so
the headless session and anyone who later joins it with `loop join` get the
same rules.

The defaults allow the git commands needed to inspect and commit work and
deny `git push`, `gh pr`, `gh api` and `git reset --hard`, because pushing
and opening the PR is loop's job. The defaults contain nothing project
specific, so add the commands your verify steps and your agent need:

```yaml
agent:
  permission_mode: acceptEdits
  allow:
    - "Bash(npm test:*)"
    - "Bash(npm run lint:*)"
    - "Bash(go test:*)"
```

Setting `allow` replaces the defaults; include the git rules you still
want. `permission_mode: bypassPermissions` skips all checks and is the
quickest way to get a first run going in a sandbox, at the cost of the
agent being able to run anything. `loop doctor` warns when only the default
rules are configured. The other harnesses have no equivalent of a rules
file; their profiles run with the CLI's own auto-approve flag (`--force`
for Cursor, `--full-auto` for Codex, `--yolo` for Gemini, `--yes-always`
for Aider, `--allow-all-tools` for Copilot, `--dangerously-allow-all` for
Amp), because a headless session that stops to ask never finishes. Verify
steps, the withheld credentials and the merge gates are what keep that
safe.

### Environment of a session

Agent sessions do not inherit loop's whole environment. They get an
allowlist: shell and locale basics, proxy settings, git identity variables,
the toolchain variables of Go, Node, Rust, Java and Python, the agent CLIs'
own configuration and credentials (`ANTHROPIC_*`, `CLAUDE_*`, `CURSOR_*`,
`OPENAI_*`, `CODEX_*`, `GEMINI_*`, `GOOGLE_*`, `AIDER_*`, `OPENCODE_*`,
`COPILOT_*`, `AMP_*`),
the `LOOP_*` variables and `agent.env`. `GITHUB_TOKEN`, `GH_TOKEN`,
`JIRA_*` and anything else are withheld, so an agent cannot push, merge or
comment with loop's credentials. Name what else your project needs in
`env_passthrough`:

```yaml
agent:
  env_passthrough: [DATABASE_URL, "MY_APP_*"]
```

`run:` and `script:` steps are your own scripts and keep the full
environment; `agent:` steps are sessions and get the allowlist.

How the prompt reaches the agent is part of the harness profile (see
[harnesses](#harnesses)): every prompt is a file in the run folder first,
and the profile says whether the CLI gets it on standard input, as an
argument, or as the file's path.

## `steps`

Everything loop runs around a session is a step list in `loop.yaml`. There
are no hidden hook directories and no global configuration. Each step is
exactly one of:

| Kind | Meaning |
| --- | --- |
| `run: <command>` | Shell command executed with `sh -c` inside the workdir. |
| `script: <path>` | Executable file, path relative to `loop.yaml`, executed inside the workdir. |
| `agent: <path>` | Prompt template rendered with the run data and executed as an agent session. |

Optional keys: `name`, `model` and `timeout` (agent steps).

| Phase | When |
| --- | --- |
| `steps.setup` | After checkout, before the session. |
| `steps.verify` | After the initial session and after every review, CI or conflict fix round, before anything is pushed. A failing `run` or `script` step starts a session with the `verify` template and the list runs again; a failing `agent` step fails the run. |
| `steps.before_pr` | After verify, before the push. A failure fails the run. |
| `steps.merged` | After the PR merged. Failures are logged. |
| `steps.cleanup` | Before the workdir is removed. Failures are logged. |
| `steps.blocked` | When a run parks because it needs a human: fix rounds used up, branch protection, PR closed, gate declined. Failures are logged. |
| `steps.failed` | When a run fails: no usable session result, setup or verify error, push or PR creation error. Failures are logged. |

```yaml
steps:
  setup:
    - script: hooks/setup.sh
    - run: npm ci
  verify:
    - name: tests
      run: npm test
    - name: self-review
      agent: prompts/self-review.md
  merged:
    - run: echo "merged $LOOP_PR_URL" >> "$LOOP_PROJECT_DIR/merged.log"
  blocked:
    - script: hooks/notify-slack.sh   # reads LOOP_RUN_ERROR and LOOP_PR_URL
  failed:
    - script: hooks/notify-slack.sh
```

Steps see this environment: `LOOP_PROJECT`, `LOOP_PROJECT_DIR`,
`LOOP_WORKDIR`, `LOOP_BRANCH`, `LOOP_BASE`, `LOOP_ITEM_ID`,
`LOOP_ITEM_TITLE`, `LOOP_ITEM_URL`, `LOOP_RUN_ID`, `LOOP_RUN_DIR`,
`LOOP_SUMMARY_FILE`, `LOOP_PR_URL`, `LOOP_PR_NUMBER`, `LOOP_RUN_PHASE`,
`LOOP_RUN_ERROR` (the reason a run parked), plus `agent.env`. Agent
sessions additionally get `LOOP_PROMPT_FILE`, the file their prompt was
loaded from (see [prompt files](prompts.md#prompt-files)).

## `pr`

| Key | Default | Description |
| --- | --- | --- |
| `draft` | `true` | Open as draft; marked ready for review once CI is green. |
| `title` | `{{ .Item.Title }}` | Title template. |
| `body` | built-in | Body template, inline or a file path. `{{ .Summary }}` is what the agent wrote to the summary file. |
| `reviewers` | none | Reviewers to request. |
| `labels` | none | Labels to add. |
| `link_issue` | `true` | Add `Closes #n` for GitHub issues. |
| `commit_uncommitted` | `true` | Commit whatever the agent left uncommitted. |

## `workflow`

| Key | Default | Description |
| --- | --- | --- |
| `poll_interval` | `60s` | How often open PRs are polled. |
| `fix_rounds` | `3` | Review, CI and verify fix sessions per run. |
| `conflict_attempts` | `1` | Conflict resolution sessions per run. |
| `merge` | `manual` | `manual`, `when-green`, `when-green-and-approved`, `auto-merge` (the host merges: GitHub's auto-merge, GitLab's merge when pipeline succeeds). |
| `merge_method` | `squash` | `squash`, `merge` or `rebase`. |
| `delete_branch` | `true` | Delete the remote branch after the merge. |
| `close_issue_on_merge` | `true` | Close the ticket after the merge. When `false`, loop waits until someone closes it. |
| `gates` | `[]` (`[before-merge]` for auto merge policies) | Pause points: `before-code`, `before-pr`, `before-fix`, `before-merge`. |
| `plan` | `false` | Run a planning session first and post the plan on the ticket; with the `before-code` gate the run waits for a human to approve it. See [plan before code](workflow.md#plan-before-code). |
| `self_review` | `false` | Let a second session review the diff before the PR opens and fix its findings in one free round. See [self-review](workflow.md#self-review-before-the-pr). |
| `concurrency` | `1` | Runs in an agent, verify or fix phase at the same time. |
| `required_checks` | all | Only these check names decide green or red. |
| `ci_log_lines` | `200` | Lines from the end of a failed CI job log (GitHub Actions, GitLab CI) passed to the CI fix prompt. `0` disables log fetching. |
| `pr_status` | `true` | Post loop's phase, fix rounds and outcome as a commit status named `loop` on the PR head. See [loop's status on the PR](workflow.md#loops-status-on-the-pr). |
| `ci_rerun` | `true` | Re-run the failed GitHub Actions jobs (retry the GitLab pipeline) once per head commit before starting a CI fix round, so a flaky job does not cost an agent session. Checks from other apps cannot be re-run. |
| `cleanup` | `true` | Remove the workdir when the item is closed. |
| `pr_commands` | `true` | Let collaborators with push access drive a run from the PR: `/loop approve` releases a gate, `/loop resume` restarts a blocked run. |
| `on_human_push` | `pause` | What happens when someone other than loop pushes to the run branch: `pause` parks the run with a note on the PR, `continue` fast-forwards the worktree and keeps driving on top of their commits. See [workflow](workflow.md#phases). |

## `budget`

Caps on what a run and the project may spend. Zero, the default, means no
cap.

| Key | Default | Description |
| --- | --- | --- |
| `run_cost` | `0` | The most one run may spend in USD across all its sessions. |
| `run_time` | `0` | The most agent session wall clock one run may use, for example `2h`. |
| `daily_cost` | `0` | The most the project may spend per calendar day. |
| `daily_runs` | `0` | The most runs the project may start per calendar day. |

Cost comes from harnesses that report it in their result (Claude Code
does; the others report nothing, so cost caps are no-ops with them). Time
caps count every session's wall clock and work with every harness.

A run checks its caps before every session, including fix rounds. A run
that hits one parks as `blocked` with a note on its PR, never as `failed`:
raise the budget in `loop.yaml` and `loop resume <run>`. The daily caps
also stop `loop watch --pick` and `loop run` from starting new runs; the
watch loop says so once and keeps driving the runs it has. `loop stats`
shows today's spend against the caps.

```yaml
budget:
  run_cost: 5
  run_time: 2h
  daily_cost: 40
  daily_runs: 10
```

## Run state

Each run lives in `.loop/runs/<run id>/`:

| File | Content |
| --- | --- |
| `run.yaml` | Phase, branch, workdir, PR, fix rounds, handled comment ids, sessions, event history. |
| `run.log` | Human-readable log of the run. |
| `session-NN-<kind>.prompt.md` | The exact prompt of each agent session. |
| `session-NN-<kind>.log` | Raw agent output of each session. |
| `summary.md` | What the agent wrote for the PR description. |
| `lock` | Advisory lock held by the process driving the run. |
