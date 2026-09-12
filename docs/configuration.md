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
`status: in-progress` and `loop_run`; closing writes `status: closed`.
Loop notes are appended under a `## Loop log` heading.

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
| `permission_mode` | `acceptEdits` | Passed to `claude --permission-mode`. |
| `timeout` | `45m` | Wall clock limit per session. |
| `max_turns` | `200` | Passed to `claude --max-turns`. |
| `attempts` | `2` | Attempts for the initial session before the run fails. |
| `skills` | none | Folders symlinked into `<workdir>/.claude/skills/`. |
| `env` | none | Extra environment variables for sessions and steps. |
| `extra_args` | none | Extra CLI arguments. |

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
```

Steps see this environment: `LOOP_PROJECT`, `LOOP_PROJECT_DIR`,
`LOOP_WORKDIR`, `LOOP_BRANCH`, `LOOP_BASE`, `LOOP_ITEM_ID`,
`LOOP_ITEM_TITLE`, `LOOP_ITEM_URL`, `LOOP_RUN_ID`, `LOOP_RUN_DIR`,
`LOOP_SUMMARY_FILE`, `LOOP_PR_URL`, `LOOP_PR_NUMBER`, plus `agent.env`.

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
