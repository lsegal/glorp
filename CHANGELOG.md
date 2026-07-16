# Changelog

## Unreleased

- Limit the dashboard agent grid to six two-column cards and keep agent output in its card instead of the Logs panel.
- Ask launched agents to summarize changes without printing code diffs or large code blocks.
- Show agent output in its dashboard job card.
- Do not retain agent output outside the interactive dashboard.
- Show the Codex weekly quota remaining in the dashboard status bar.
- Fix the Codex quota status showing as unavailable.
- Improve the terminal dashboard with titled panels, a dedicated scrolling log area, and clearer status boundaries.
- Log webhook delivery details and retry webhook-triggered issue refreshes after GitHub indexing catches up.
- Preserve pending webhook follow-up refreshes so newly created issues are not skipped when deliveries arrive close together.
- Watch multiple repository or project targets in one process.
- Support GitHub webhooks as the default issue refresh mechanism, with polling available through `--poll`.
- Manage an ngrok tunnel and synchronize GitHub webhooks for watched repositories.
- Show an interactive Bubble Tea dashboard with job cards, status counts, targets, and push or polling state when attached to a terminal.

## v1.0.0 - 2026-07-15

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
