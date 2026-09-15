# Prompt templates

Templates are Go `text/template` files. The built-in defaults are used
unless `loop.yaml` points at your own; `loop init --prompts` writes
editable copies to `prompts/`, and `loop show <item> --prompt` prints the
rendered session prompt for an item.

## Data

| Field | Type | Available in |
| --- | --- | --- |
| `.Project` | string | all |
| `.Item` | item: `.ID`, `.NativeID`, `.Source`, `.Title`, `.Body`, `.URL`, `.Labels`, `.Created`, `.DependsOn`, `.Every` (the schedule of a recurring item, empty otherwise), `.Comments` | all |
| `.Previous` | the previous occurrence of a recurring item: `.RunID`, `.Started`, `.Phase`, `.Outcome`, `.PRURL`, `.Summary`, `.Error`, and `.Result` (a one-line rendering such as `merged (url)` or `no changes`); nil for one-shot items and the first occurrence | all |
| `.Item.Comments` | list of `.Author`, `.Body`, `.Created`, `.URL` | all (empty when the source sets `comments: none`) |
| `.Branch`, `.Base`, `.Workdir`, `.RunID` | string | all |
| `.SummaryFile` | path the agent should write the PR summary to | all |
| `.Attempt` | attempt number of the initial session | session |
| `.Round` | fix round number | review, ci, verify |
| `.PR` | `.Number`, `.URL` | review, ci, conflict |
| `.Reviews` | `.Author`, `.State`, `.Body`, `.URL` | review |
| `.ReviewComments` | `.ID`, `.Author`, `.Path`, `.Line`, `.Body`, `.DiffHunk`, `.URL` | review |
| `.RepliesFile` | path of the JSON file where the session records, per comment id, its reply and whether the request is done | review |
| `.PlanFile` | path the plan session writes its plan to | plan |
| `.Plan` | the approved plan's text, empty without `workflow.plan` | session |
| `.Diff`, `.FindingsFile` | the branch's diff against the base (cut at 200 KiB) and where to write findings | self-review |
| `.PRComments` | `.Author`, `.Body`, `.Created`, `.URL` | review |
| `.Checks` | `.Name`, `.Conclusion`, `.URL`, `.Summary`, `.Text`, `.Log` (tail of the CI job log, see `workflow.ci_log_lines`) | ci |
| `.Conflicts` | list of file paths | conflict |
| `.VerifyStep`, `.VerifyOutput` | string | verify |
| `.Summary` | contents of the summary file | pr body |

Functions: `quote` (markdown blockquote), `indent n`, `trunc n`, `join`,
`trim`, `default`.

## Prompt files

Every prompt is a file before it is anything else. The rendered template
for the initial session, a CI or review fix round, a conflict round, a
verify round or an agent step is written to
`.loop/runs/<run>/session-NN-<kind>.prompt.md` first, and the agent CLI
loads it from there in the way its [harness profile](configuration.md#harnesses)
says: on standard input, as an argument, or as the file's path, with long
prompts replaced by a one-line pointer at the file. The session sees the path as
`LOOP_PROMPT_FILE`, so the agent (or a hook) can read the instructions
again at any point. `loop logs <run> --prompt` prints the same file, so
what you inspect is exactly what the agent received. A prompt that cannot
be written or renders empty stops the session before the CLI starts.

## Ticket comments: use them or not

Whether the discussion on a ticket reaches the agent is decided by the
session template, not by code. Two ready-made variants:

- [examples/session-with-comments.md](examples/session-with-comments.md)
  renders the discussion as a section of the prompt. Use it when your team
  refines ideas in the ticket thread.
- [examples/session-without-comments.md](examples/session-without-comments.md)
  ignores comments entirely. Use it when threads are noisy or contain
  process chatter you do not want the agent to act on.

Copy one to `prompts/session.md`, or point `prompts.session` at it. If you
never want comments loaded at all (for example to keep the prompt small),
set `comments: none` on the source; `.Item.Comments` is then always empty.
On GitHub only comments by the repository owner and by collaborators with
write access are loaded unless the source sets `comments: all`, because
anyone can write them and they reach the agent verbatim; see
[security](security.md).

The same pattern applies to fix rounds: the review, CI and conflict
templates receive the feedback as data and decide how to present it.

## Spec-driven development with OpenSpec

[OpenSpec](https://github.com/Fission-AI/OpenSpec) keeps a repository's
requirements as markdown specs under `openspec/specs/` and takes every
change through three stages: a proposal under `openspec/changes/<id>/`
(with delta specs and a task list), the implementation, and an archive
step that merges the delta specs into the main specs and moves the change
to `openspec/changes/archive/`. Interactively that is `/opsx:propose`,
`/opsx:apply` and `/opsx:archive`, one command at a time.

[examples/session-openspec.md](examples/session-openspec.md) is a session
template that does all three in the initial session. The agent writes the
proposal, validates it with `openspec validate <id> --strict` and commits
it, implements the tasks and ticks them off, then runs
`openspec archive <id> --yes` and commits the result. The pull request
loop opens therefore carries the code, the updated specs and the archived
change together, a reviewer reads the proposal, the spec deltas and the
implementation in one diff, and nothing stays behind under
`openspec/changes/` waiting for a second session. The proposal takes the
place of loop's own [plan phase](workflow.md#plan-before-code), so
`workflow.plan` is not needed; a `before-pr` gate still lets you read the
proposal and the diff before the PR opens.

What the repository and the project need:

- The repository is set up with `openspec init` and the `openspec/` folder
  and the installed commands or skills (`.claude/commands/opsx/`,
  `.claude/skills/openspec-*` for Claude Code) are committed, so every
  fresh worktree has them.
- The `openspec` CLI is on the `PATH` where loop runs
  (`npm install -g @fission-ai/openspec`); sessions and verify steps call
  it.
- `loop.yaml` points at the template, lets the headless session run the
  CLI, and validates the merged specs after every session and fix round:

```yaml
prompts:
  session: prompts/session.md   # a copy of docs/examples/session-openspec.md

agent:
  allow:                        # replaces the defaults, so the git rules are repeated
    - "Bash(openspec:*)"
    - "Bash(npm test:*)"
    - "Bash(git status:*)"
    - "Bash(git diff:*)"
    - "Bash(git log:*)"
    - "Bash(git show:*)"
    - "Bash(git add:*)"
    - "Bash(git commit:*)"
    - "Bash(git restore:*)"
    - "Bash(git stash:*)"
    - "Bash(git branch:*)"
    - "Bash(git merge:*)"
    - "Bash(git rm:*)"
    - "Bash(git mv:*)"

steps:
  verify:
    - name: specs
      run: openspec validate --specs --strict
    - name: tests
      run: npm test
```

Fix rounds use the ordinary review, CI and conflict templates: a spec
correction a reviewer asks for is then an edit of the already archived
change and the merged spec, which the verify step validates again before
the round pushes. Harnesses other than Claude Code get the same prompt;
the template names the stages and the CLI calls, so an agent without the
`/opsx` commands follows them by hand.

## Recurring items

For an item with `every:` the built-in session template adds a "Recurring
task" section: the interval, what the previous occurrence did (`.Previous`,
with its summary quoted), and the instruction that changing nothing is a
valid result, because a session without commits ends a recurring run as
"no changes" instead of failing it. A custom template can use the same
data; without the instruction the agent may invent work to have something
to commit.

## Answering reviewers

The built-in review template asks the session to write
`[{"id": <comment id>, "reply": "...", "resolved": true|false}]` to
`.RepliesFile` (also available as `LOOP_REPLIES_FILE`). After the round
loop replies in every inline thread it handed to the session, with the
agent's note and the pushed commit, and resolves the threads marked
`resolved`. Comments the session did not report on get a neutral note and
stay open. A custom review template that drops this instruction still gets
the neutral notes, so no reviewer thread is left unanswered.

## Agent steps

`steps.setup` and `steps.verify` entries with `agent: path.md` render that
file with the same data and run a session with it. Example self-review step:

```
Review the diff of branch {{ .Branch }} against origin/{{ .Base }} in
{{ .Workdir }} for "{{ .Item.Title }}". Fix real bugs, missing tests and
violations of the repository conventions, commit, and append a "Self
review" section to {{ .SummaryFile }}. Do not push.
```
