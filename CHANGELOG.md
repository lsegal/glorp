# Changelog

## Unreleased

- Show each agent's checkout directory and session ID in its dashboard viewport, including after completion or failure.
- Launch issue-fixing agents with the repository reported by GitHub instead of relying on the current directory.

## v1.0.1 - 2026-07-17

- Prevent `--no-ui` from crashing when normal log messages are written.

## v1.0.0 - 2026-07-17

- Add the glorp robot-patcher logo and a Hugo-powered GitHub Pages site generated directly from the README.
- Add `--yolo` to opt into launching Codex or Claude without sandbox and permission checks.
- Add `--no-ui` to disable the interactive dashboard and print normal logs in a terminal.
- Watch multiple repository or project targets in one process.
- Use synchronized GitHub webhooks over a managed ngrok tunnel by default, with polling available through `--poll`.
- Show an interactive terminal dashboard with job cards, status counts, targets, scrolling logs, and push or polling state.
- Stream Codex progress into dashboard job cards, show completed jobs with a green checkmark, and display the weekly quota remaining.
- Reload and resynchronize when `.glorp.json` changes.
- Default issue watching to open issues authored by the current user.
- Support repeated `--filter` arguments using GitHub issue search syntax.
- Prevent launched agents from waiting for or reporting additional stdin input.
- Ask launched agents to summarize changes without printing code diffs or large code blocks.
- Reliably update project-board status and keep watching when a failed issue has been removed from the board.
- Always remove isolated clone directories when the issue-fix workflow exits.
- Explain and report the `project` scope required to update project-board issue status.
- Finalize releases after tags created by GitHub Actions.
- Skip project-board issues that are already in progress or completed.
- Avoid applying the `agent-started` label to issues watched through a project board.
- Add a `--version` flag and promote changelog entries during releases.
- Use the versioned changelog section as GitHub release notes.
- Fix labeling issues discovered while watching GitHub project boards.
- Recover stuck project issues from their `In Progress` board status without relying on the `agent-started` label.
- Use the `bug:` prefix for new bug-report issue titles.
- Omit the default `agent-ready` label filter when watching a project board.
- Allow selecting the agent model and reasoning level with `--model` and `--model-level`.
- Omit robot output from autofilled bug reports to prevent private data from being disclosed.
- Respect issue dependencies and leave blocked issues for a later poll.
- Parse `label=...` filter terms as GitHub label search queries.
- Update project-board issue status as agents start and finish work.
- Report the required `read:project` scope when project-board polling cannot access project items.
- Add a scrubbed autofilled bug-report URL when an agent exits unsuccessfully.
- Preserve agent session IDs after an issue is completed.
- Remove `agent-ready` and `agent-started` labels after the originating PR is merged.
- Ensure the `agent-ready` and `agent-started` labels exist when watching a repository.
- Add issue label filtering with a default `label=agent-ready` filter and an `--all-issues` override.
- Track active agent work with the `agent-started` issue label and persisted session state.
- Allow watching GitHub repository and project URLs as targets.
