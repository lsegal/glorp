# Changelog

## Unreleased

- Reduce GitHub API usage while agents are actively working. The per-issue closure/competing-claim polling loops now poll every 30 seconds instead of 10, matching the default base poll interval rather than tripling its request rate, since every active issue runs two of these loops in parallel and each poll costs several API requests.
- Have the `gh-fix` skill post a comment on the issue explaining why no fix was implemented when it stops during validation without opening a pull request, instead of only reporting the reason to the caller.
- Fix `glorp upgrade` failing on Windows with `Copy-Item : The process cannot access the file '...\glorp.exe' because it is being used by another process`. `install.ps1` now renames the running `glorp.exe` to `glorp.exe.bak` before copying the new binary into place, since Windows allows renaming an in-use file even though it won't allow overwriting one.
- Take glorp's subprocesses down with it when glorp is killed outright. Owned subprocesses now carry a Linux parent-death signal, belong to a Windows job object that is destroyed with glorp, and on macOS and the BSDs — which have no kernel equivalent — are recorded with a small reaper process that terminates them when its pipe to glorp closes, so `kill -9` of a running `glorp watch` no longer strands the ngrok tunnel or a running agent on any supported platform.

## v1.2.3 - 2026-08-17

- Mark the `gh-fix` skill's initial `Start work on issue #N` commit with `[skip ci]`, and teach the push-triggered `CI` and `Deploy Pages` workflows to honor that marker, so opening a draft pull request no longer runs a full build against a tree identical to the default branch.
- Stop leaving subprocesses behind when glorp exits. Every process glorp starts — the ngrok tunnel, agent runs, `gh` calls, quota probes, and the web UI dev server — now runs in its own process group and is tracked, so shutting down terminates it along with anything it spawned instead of orphaning an ngrok tunnel that keeps holding the public URL. Closing the terminal (`SIGHUP`) now shuts glorp down too, and a second `Ctrl-C` kills the remaining subprocesses immediately instead of waiting for a graceful stop.

## v1.2.2 - 2026-08-17

- Restructure the CLI around subcommands: `glorp watch`, `glorp ui`, `glorp version`, `glorp upgrade`, and `glorp help`. Watching now requires the `watch` subcommand — the old top-level form (`glorp owner/repo`, `glorp --poll …`) has been removed — while `glorp --version` and `glorp -h` still work as aliases for `glorp version` and `glorp help`.
- Add `glorp ui`, which finds running glorp dashboards on localhost and opens one in the default browser. Each `glorp watch` records the dashboard port it bound, so instances started with a distant `--web-ui-port` are found without repeating the port, and a localhost scan (from port 8765 upward, or `--port`) still finds instances that left no record. When several instances are running it shows an interactive picker; on a non-interactive terminal it opens the lowest-numbered port.
- Fix a panic on the first log line when `glorp` runs with `--ui none` or `--ui tui`. The unused browser dashboard was still registered as a UI reporter as a typed-nil pointer, so the first log message dereferenced it.
- Add a `projects:` / `discussions:` target shorthand so boards no longer need a full GitHub URL: `glorp watch projects:lsegal/glorp/3 discussions:lsegal/glorp/q-a`, or `glorp watch projects:3 discussions:q-a` inside a checkout, where the `OWNER/REPO` is taken from the `origin` remote. A discussions target can now also name a single category (`https://github.com/OWNER/REPO/discussions/categories/q-a`), and only threads in that category are watched.

## v1.2.1 - 2026-08-17

- Stop probing organization-owned project boards in push mode. Those boards already deliver every card change over their `projects_v2_item` webhook, so the 30-second board fingerprint probe was duplicate polling; it now runs only for boards GitHub gives no board-level push signal for (user-owned and repository-scoped Projects). The startup line also reports the real refresh strategy in push mode (`webhook push with a 15m0s fallback poll`) instead of the unused `-interval` value.

## v1.2.0 - 2026-08-17

- Show what a Claude agent is actually working on in the dashboard progress lines: tool activity now reads `Running: Read main.go`, `Running: Grep pattern`, or `Running: WebFetch https://…` instead of a bare `Running: Read`.
- Add support for watching a GitHub Discussions board target (`https://github.com/OWNER/REPO/discussions`). glorp dispatches the new read-only `gh-discuss` skill for each new top-level Discussion thread with no replies yet; the skill only reads the repository to answer the question and posts a single top-level reply when it can do so accurately and positively, otherwise it leaves the thread untouched. Discussions boards are watched in push mode as well as poll mode: glorp subscribes the repository webhook to GitHub's `discussion` event, widening an existing glorp webhook when the same repository is already watched as a repository or Project target.
- Log every step of the cooperative handoff protocol: when a reap pass starts and in which mode, why a candidate issue looks claimed and where it will be negotiated, when "Does anyone have this?" is asked and how long the wait is, which instance answered, whether the work was picked up or let go, and which instance claimed an issue out from under a running agent. The instance identity now appears in the startup line and in handoff messages so multiple instances can be told apart in the logs.
- Restart an issue from scratch when the agent reports that the recorded session no longer exists, instead of failing the run and printing a bug report link. Sessions expire out of the agent's local history while glorp's work state keeps referring to them, and the issue workflow is re-entrant, so it picks the existing draft pull request back up.
- Detect board-only project changes in push mode by probing a cheap project board fingerprint every 30 seconds, so dragging an issue onto a board or moving a card into the ready column dispatches promptly instead of waiting for the 15-minute fallback poll.
- Detect new project board issues in push mode for user-owned Projects, which GitHub gives no `projects_v2_item` webhook, by subscribing to repository webhooks on every repository backing the board instead of leaving the whole target to the periodic poller. Those webhooks are re-reconciled on every periodic poll, so a repository added to the board later is watched without restarting `glorp`.
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
