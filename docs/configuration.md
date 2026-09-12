# Configuration reference (`loop.yaml`)

Paths are relative to the folder containing `loop.yaml`. Durations use Go
syntax (`45m`, `1h30m`, `90s`).

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

Set `GITHUB_API_URL` for GitHub Enterprise.

## `sources`

An ordered list. Order matters: it is the pick-up order.

Common keys:

| Key | Default | Description |
| --- | --- | --- |
| `name` | the type | Unique name; item ids are `<name>:<native id>`. |
| `type` | required | `markdown`, `github` or `jira`. |
| `labels` | none | Only items carrying all of these labels are listed. Recommended: `[ready-for-agent]`. |
| `claim` | `false` (`true` when `claim_label` is set) | Mark items in progress so two loops never pick the same one. |
| `claim_label` | `loop:in-progress` | Label used by GitHub and Jira for the claim. |
| `comments` | `true` | Load ticket comments into the template data (`.Item.Comments`). |

### `type: markdown`

| Key | Default | Description |
| --- | --- | --- |
| `path` | `backlog` | Folder of `*.md` files. |

Frontmatter keys: `id`, `title`, `status` (`open`, `in-progress`, `closed`),
`created`, `labels`, `depends_on`, `model`, `loop_run`. Without a title the
first `# heading` or the file name is used. Claiming writes
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
(`loop init` writes copies to `prompts/`).

| Key | Used for |
| --- | --- |
| `session` | The initial implementation session. |
| `review` | Fix rounds triggered by reviews and PR comments. |
| `ci` | Fix rounds triggered by failing checks. |
| `conflict` | Resolving merge conflicts git could not resolve. |
| `verify` | Fixing a failing verify step. |

See [prompts.md](prompts.md).

## `agent`

| Key | Default | Description |
| --- | --- | --- |
| `runner` | `claude` | `claude` (Claude Code CLI) or `cursor` (Cursor `agent` CLI). |
| `command` | runner name | Executable override. |
| `model` | runner default | Model for every session. Items override it with a `model:` line or frontmatter. |
| `permission_mode` | `acceptEdits` | Claude only: `default`, `acceptEdits`, `bypassPermissions` or `plan`. Written to the workdir settings and passed to `claude --permission-mode`. |
| `allow` | git defaults | Claude only: permission rules the headless session may use without prompting, for example `Bash(npm test:*)`. See below. |
| `deny` | `git push`, `gh pr`, `gh api`, `git reset --hard` | Claude only: rules the session may never use. |
| `timeout` | `45m` | Wall clock limit per session. |
| `max_turns` | `200` | Passed to `claude --max-turns`. |
| `attempts` | `2` | Attempts for the initial session before the run fails. |
| `skills` | none | Folders symlinked into `<workdir>/.claude/skills/`. The links are kept out of git through the repository's `info/exclude`. |
| `env` | none | Extra environment variables for sessions and steps. |
| `extra_args` | none | Extra CLI arguments. |

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
rules are configured. The Cursor runner always runs with `--force`, which
is Cursor's equivalent of bypassing permissions.

How the prompt reaches the agent: `claude` receives it on standard input
with a pre-assigned session id. `agent` (Cursor) receives short prompts
as the positional argument; long ones (over 16 KiB, typical for tickets
with comment threads) are piped on standard input while the positional
argument points at the prompt file in the run folder, so the command line
never exceeds the operating system's argument limit.

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
| `steps.verify` | After the session and after every verify fix. A failing `run` or `script` step starts a session with the `verify` template; a failing `agent` step fails the run. |
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
`LOOP_RUN_ERROR` (the reason a run parked), plus `agent.env`.

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
| `merge` | `manual` | `manual`, `when-green`, `when-green-and-approved`, `github-auto-merge`. |
| `merge_method` | `squash` | `squash`, `merge` or `rebase`. |
| `delete_branch` | `true` | Delete the remote branch after the merge. |
| `close_issue_on_merge` | `true` | Close the ticket after the merge. When `false`, loop waits until someone closes it. |
| `gates` | `[]` (`[before-merge]` for auto merge policies) | Pause points: `before-pr`, `before-fix`, `before-merge`. |
| `concurrency` | `1` | Runs in an agent, verify or fix phase at the same time. |
| `required_checks` | all | Only these check names decide green or red. |
| `ci_log_lines` | `200` | Lines from the end of a failed GitHub Actions job log passed to the CI fix prompt. `0` disables log fetching. |
| `cleanup` | `true` | Remove the workdir when the item is closed. |

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
