# Changelog

## Unreleased

- Detect board-only project changes in push mode by probing a cheap project board fingerprint every 30 seconds, so dragging an issue onto a board or moving a card into the ready column dispatches promptly instead of waiting for the 15-minute fallback poll.
- Detect new project board issues in push mode for user-owned Projects, which GitHub gives no `projects_v2_item` webhook, by subscribing to repository webhooks on every repository backing the board instead of leaving the whole target to the periodic poller.
- Keep running when only some of a target's push webhooks can be configured, reporting the failures instead of exiting.

- Reclaim project items stranded at `In Progress` by an instance that died mid-run: a new instance now runs the cooperative handoff handshake on them instead of skipping them forever, and only stands down when another instance answers that it still owns the work.
- Keep renegotiating ownership of an issue after standing down for another instance, instead of treating it as brand new on the next poll and dispatching it anyway.
- Reap abandoned work at least every 10 minutes instead of effectively only at startup, even in push mode where the polling fallback is 15 minutes. To keep repeated reaps from spamming "Does anyone have this?", only the first reap after startup asks unconditionally; later reaps ask only when the newest claim comment from another instance is more than 2 hours old.

- Add a `glorp upgrade` command that re-runs the published install script for the current platform.
- Stop polling GitHub for webhook deliveries that cannot change which issues are dispatchable. Push, pull request, ping, and ordinary comment deliveries, along with `issues` actions such as `edited`, `assigned`, and `locked`, no longer trigger a refresh.
- Stop the webhook follow-up refresh chain as soon as a refresh actually observes the issue the delivery named, instead of always running the full retry budget.
- Increase the push-mode fallback synchronization interval from 90 seconds to 15 minutes, since webhook deliveries already provide the real-time path and the fallback only needs to recover missed deliveries.

## v1.1.0 - 2026-08-17

- Replace the manual full-version `Release` workflow input with a `major`/`minor`/`patch`/`version` level chooser that computes the next tag from the latest existing tag, matching the release bump logic from `lsegal/aviary`.
- Remove the `agent-started` issue label; the cooperative comment-based handoff protocol is now the sole authority for whether a repository issue is claimed.
- Add a cooperative handoff protocol so multiple glorp instances sharing a repository negotiate ownership through signed `/glorp:UUID` comments ("Does anyone have this?" / "Starting work on this issue" / "Continuing work on this issue" / "I am working on this") before reaping a ticket that already looks claimed, instead of silently duplicating or abandoning work. Each instance gets a random in-memory identity, and the last instance to claim a ticket wins.
- Watch for a competing starting/continuing claim from another instance while an agent is actively working an issue, cooperatively stopping the run and removing its local checkout directory if one takes over mid-run.
- Subscribe repository webhooks to GitHub's `issue_comment` event so a `Does anyone have this?` handoff comment gets an immediate `I am working on this` reply from the owning instance instead of waiting for the next poll.
- Make the `gh-fix` skill re-entrant: `/gh-fix N` now resumes an existing open draft pull request for issue `N` (or for `N` itself when it names a PR) instead of starting over. Ownership negotiation stays entirely in `glorp` itself; the skill no longer reads or posts comments to decide whether a branch is safe to adopt.
- Replace `--model` and `--model-level` with per-agent `--agent AGENT/MODEL:LEVEL` specs so each load balanced agent can run its own model and reasoning level.

## v1.0.7 - 2026-08-16

- Support repeating `--agent` to configure multiple agents, load balanced evenly across concurrency, with each agent's quota shown by name in the dashboard.
- Show Claude's actual session/week usage in the dashboard quota display instead of "not tracked", read via the free local `/usage` slash command.
- Default the repository target to the current directory's `origin` git remote when no `TARGET` argument is given.
- Redispatch a completed project item when its status is moved back to `Todo` without requiring a daemon restart.
- Fix a previously seen project item not being redispatched when its status changes to `Todo`/`Ready` (matching was case-sensitive and restricted to `In Progress`).
- Fix the checkout directory getting stuck reporting "pending" when the agent wraps its `GLORP_CHECKOUT_DIRECTORY` progress line in markdown formatting.
- Show each agent's clone status inside its viewport output area in the browser dashboard.
- Record the contributing agent CLI and model in a gh-fix pull request footer, accumulating every agent/model that has worked the issue when a different one resumes mid-flight.
- Stream Claude agent progress into the dashboard as it happens instead of showing "waiting for output..." until the job finishes.

## v1.0.6 - 2026-08-16

- Run non-interactive Claude agents in autonomous permission mode so issue workflows can use required tools instead of exiting immediately.

## v1.0.5 - 2026-07-21

- Replace separate UI-disabling flags with `--ui web|tui|none`, retaining `--no-ui` as an alias for `--ui none`.
- Move the browser dashboard status bar beneath the title area.
- Clarify gh-fix UI attachment terminology as screenshots and screen recordings.
- Inline gh-fix UI screenshots in pull request descriptions instead of committing them as repository assets.
- Build the browser dashboard with Vite only for release artifacts, while local development uses the Vite dev server and CI validates the UI's build, lint, and tests.
- Restore readable spacing between metrics in the web dashboard status bar.
- Retry webhook-triggered refreshes when GitHub's issue index lags, so newly pushed issues are not skipped.

## v1.0.4 - 2026-07-20

- Add a localhost React browser dashboard that mirrors terminal job viewports, logs, scrolling, and status, with configurable port selection and an option to disable it.
- Use the owner type encoded in GitHub Project URLs when polling, avoiding intermittent GitHub CLI owner-detection failures.
- Create individual follow-up issues for unresolved TODOs and known issues after merging an issue-fix pull request, preserving origin links and project membership.
- Stop active agent work as failed when its originating issue or an unmerged linked pull request is closed.
- Preserve completed agents' viewport scrollback and scroll position in the dashboard.
- Open issue-fix pull requests as linked drafts immediately, push development checkpoints every five minutes, and mark them ready when implementation is complete.
- Poll every 90 seconds as a fallback in push mode to reduce GitHub CLI load.

## v1.0.3 - 2026-07-20

- Show each agent's actual isolated clone directory in the dashboard and persist it for resumed sessions.
- Resume interrupted Codex and Claude sessions with their original agent and working directory.
- Keep each dashboard viewport pinned to new output only while it is at the bottom, with per-pane mouse scrolling, draggable scrollbars, and a clickable more indicator.
- Update attached GitHub Project statuses when repository-watched issues start, finish, or reset.
- Keep watching when a failed project item's status cannot be reset.
- Require issue-fixing agents to provide screenshots or recordings for completed UI changes.
- Stream live Codex standard output as readable dashboard text.

## v1.0.2 - 2026-07-20

- Reset stale locally completed or active issues and requeue them when their remote state no longer matches.
- Keep polling repositories and project boards after their initial issue scan, including when webhooks are enabled, so inconsistent issue state can be resynchronized.
- Support push-mode refreshes for organization-owned GitHub Project boards.
- Allow project boards to configure their ready status, defaulting to case-insensitive `Todo` and `Ready` matching.
- Use an available random webhook port by default and report the actual address passed to ngrok.
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
