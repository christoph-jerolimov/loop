---
title: Example idea
created: 2026-01-01
labels: [example]
# depends_on: [other-idea.md]
# model: claude-opus-5
# every: 7d          # a recurring task: run again a week after each run, never closed
# every: mon 06:00   # or at set times: "weekdays 07:30", "daily 03:00", cron "0 6 * * 1"
---
Describe the idea or goal here as you would in a ticket. The whole body is
handed to the agent through the session prompt template.

Lines like the following are understood everywhere (markdown, GitHub, Jira);
an "every: 7d" line would make the item a recurring task:

depends on: other-idea.md, #12, PROJ-7
