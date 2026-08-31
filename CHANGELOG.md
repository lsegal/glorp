# Changelog

## Unreleased

- Add `glorp watch -browser -no-headless`, which drives browser mode's browser visibly instead of headlessly. A failing page read in `-browser` mode previously left nothing to inspect but the CDP traffic, because the browser glorp drives is always headless; the flag puts its window on screen so a poll can be watched navigating and the page GitHub actually served can be seen. Nothing else about the run changes -- the same profile, tabs, poll interval, and readers -- and the first tab attaches to the window Chrome opens by itself rather than adding an empty one beside it. It only means anything in browser mode, so passing it without `-browser` is an error rather than a silent no-op, and it is refused on a machine where no window can appear (a Linux session with neither `DISPLAY` nor `WAYLAND_DISPLAY` set) instead of launching a browser nobody can see.
- Stop glorp re-asking who owns work it has already claimed itself. The reap that runs before reclaiming a contested ticket only looked at claims signed by *other* instances, so an instance was blind to its own: a ticket that came back around without a local work record had its whole handshake re-run, posting a fresh `Does anyone have this?` and `Starting work on this issue` pair signed by the same identity on every pass and stalling each one for the two-minute grace window. The reap now reads the newest claim on the ticket whichever instance signed it, and when that claim is this instance's own and still within the staleness window it dispatches the work without commenting again. A newer claim from another instance still takes the work, and a claim of the instance's own that has gone stale still re-opens the handshake.
- Keep the browser profile signed in to GitHub between runs, instead of needing `glorp auth` before every `glorp watch -browser`. The profile directory was already persistent, but GitHub's sign-in lives in *session* cookies (`user_session` and friends), which a browser holds in memory and drops when its process exits: only the crumbs around them (`logged_in`, `_octo`, `_device_id`) carry an expiry, so every new browser process started signed out. glorp now saves the profile's cookies when it stops a browser and injects them back before the next one navigates anywhere, covering `glorp auth`, `glorp watch -browser`, and the suspend/resume handover the in-run sign-in recovery does. The expiring crumbs are carried alongside the session cookies rather than left to the browser's own database, which a browser only commits on a batch timer of roughly 30 seconds and does not flush when it is stopped: a short-lived process such as `glorp auth` used to lose them entirely. Their expiry is carried with them, so one that has since passed is dropped instead of replayed. The cookies are written inside the profile they belong to and readable only by the user, so `-browser-profile` still picks which sign-in is used, and a profile that was signed out clears its saved sign-in rather than replaying a session GitHub has already invalidated.
- Stop the poll loop repeating itself in the log. Every tick wrote a start line, a count, and a completion line whether or not anything had changed, so a five-second poll with nothing to do produced three identical lines every five seconds; a failing poll additionally logged its cause where it was raised and again where it was returned, twice per tick, for as long as it kept failing. The poll now reports what it found only when that changes, reports a failure once (with the target or step that raised it, on the single line the caller writes) and says once when the poll recovers, and the same is true of a repeating project-board probe or discussion-listing failure. An issues page whose list container renders no rows is also read as an empty list rather than as unreadable markup, so a repository with no ready issues stops being reported as an error at all.
- Open one browser window from `glorp auth`, not two. A headed Chrome puts a window on screen as soon as it starts, and glorp then asked the DevTools endpoint for a tab of its own, which opened a second window: the user was shown an empty `New Tab` window sitting behind the `github.com/login` window the sign-in is actually driven in. The first headed tab now attaches to the window the browser already opened, so the sign-in window is the only one that appears; a browser that reports no window still falls back to opening its own tab, and headless `glorp watch -browser` runs are unchanged.
- Wait for GitHub's issues page to render before reporting it unreadable in `glorp watch -browser`. The issues list is a client-rendered React app, but the extractor ran once, immediately after the tab navigated or reloaded, so a poll that landed before the list had drawn saw no rows and no empty-state marker and failed the tick with `no issue rows and no empty-list marker were found on the page` while the repository plainly had open issues. The read is now retried until the page recognises itself, bounded the same way the project board reader's wait is (up to 5s), so a page that has already drawn still costs a single evaluation and only a page that never renders spends the budget and is reported as an extraction failure.
- Ask a project board for each search qualifier once. The item query for a `projects:` target hardcoded `is:issue is:open` and then appended the whole `--filter`, which already opens with `is:issue state:open`, so the board was asked for `is:issue is:open is:issue state:open ...` and a `--filter "is:pr ..."` contradicted itself instead of selecting what was asked for. Both qualifiers are now supplied only as defaults for a filter that does not already name a kind or a state, and a state named in the issues-page vocabulary (`state:open`) is translated to the board's own (`is:open`), which a board understands. `--filter label:ready` and `--all-issues` are unchanged, and the default filter selects the same items it did before.
- Stop repeating `is:issue state:open` in the GitHub search query glorp lists issues with. The query prepended both qualifiers and then appended the whole `--filter`, which already opens with them, so the default run searched `is:issue state:open is:issue state:open assignee:@me author:@me`. The qualifiers are now supplied only as defaults for a filter that does not name its own kind or state, so a `--filter` such as `is:pr ...` or `state:closed ...` gets the search it asked for instead of a self-contradictory one. `--all-issues` and `@me` substitution are unchanged.

- Sign the browser profile back in from `glorp watch -browser` itself, instead of only reporting that it is signed out. A private repository or board is served to a signed-out profile as a 404 or a 403, which is indistinguishable from a missing one by status alone, so glorp now asks the page GitHub actually served: a failing status confirmed by the page's own signed-out markers is read as a sign-in signal, while a signed-in run that mistyped a repository is still told it was a 404. The check costs no extra request and no agent — it piggybacks on the navigation the poll already made. On detection the run logs what it saw, stops its headless browser, opens a login window on the same profile at GitHub's login page, and resumes polling once the sign-in lands; the session persists across later runs. A sign-in that was declined or timed out is not re-offered on the next tick but backed off (10 minutes, doubling to an hour, cleared by a successful sign-in), the headless browser is restarted either way so the run keeps watching, and on a machine where no window can appear (no display server) glorp refuses to open one and points at `glorp auth` instead.
- Tell the user when `glorp watch -browser` is reading GitHub with a signed-out browser profile, instead of reporting an empty issue list forever. With `@me` in the default filter a signed-out session matches nobody, so GitHub renders a genuine "No results" page: the extraction succeeds, the poll logs `browser read 0 issue(s)`, and nothing is ever dispatched. A page that comes back with no rows is now probed for GitHub's own signed-in markers, and a page served to a signed-out session fails the poll with an actionable message naming the profile directory and pointing at `glorp auth`. Repository issues pages and project boards are both covered, the probe is a failure-path check that costs a successful read nothing, a page carrying no evidence either way is never blamed on the profile, and a signed-out page is not handed to the `-browser-vision` screenshot fallback, which cannot sign a profile in.
- Add the `glorp auth` command that browser mode has been documented against but never shipped. `glorp auth` opens a visible browser window on the same profile `glorp watch -browser` reads GitHub with, waits (up to 5 minutes) for the sign-in to finish, and closes it again, so private repositories and boards stop rendering as a 404, a 403, or a login wall; the session persists in the profile across watch runs. `glorp auth -status` reports whether the profile is signed in and as whom without opening anything, exiting 0 or 1, and both honour `-browser-profile` and `-browser-binary`. On a Linux session with no display server the command explains itself instead of waiting on a window that can never appear.
- Hydrate browser-mode issues with the body and dependency/sub-issue state the rendered issues page and project board do not carry, so `glorp watch -browser` dispatches with the real issue body and blocks on dependencies and open sub-issues exactly like the API path does, for repository targets and `projects:` targets alike. A board item's `Status` column is left untouched by the fetch. The fetch is one targeted REST read plus the existing dependency lookup, made only for issues that are dispatch candidates this instance does not already have in flight or completed, and memoized for the life of the run: a poll whose issue list did not change makes no API calls at all, and no GraphQL query is ever issued.
- Add an opt-in `glorp watch -browser -browser-vision` safety net for the day GitHub's markup changes: when the issues page loads but its rows cannot be read, glorp hands one screenshot to the configured agent and asks for the issue numbers so the run keeps dispatching. It is off by default and never runs on a schedule, on a successful read, on an empty list, or on a page that failed to load. The budget is enforced in code — one screenshot per target per 10 minutes and three per run, after which the fallback turns itself off for the rest of the run — every call is logged with its reason and running count, and an answer that is not a bare JSON list of issue numbers is discarded rather than retried. A project board that loads but never renders is now reported as the same distinguishable extraction failure an unreadable issues page is, so both are told apart from a navigation or HTTP error, and the fallback covers it too: because a board spans repositories, the agent is asked for `OWNER/REPO#NUMBER` references there rather than bare numbers, and an answer that omits the repository is discarded. Each recovered board item also carries the `Status` column read off the same screenshot, so a recovered board dispatches under `--ready-state` like a board glorp read normally; an item the board shows no status for stays undispatchable and is logged as such, with its count and reason, rather than going quiet. The per-run cap is one budget shared by issue pages and boards.

- Read the issue list from GitHub's own issues page when `glorp watch -browser` is used, instead of calling the GitHub API. Browser mode keeps one tab per watched repository, reloads it on each poll, and reads the rendered rows out of the page in a single evaluation, so polling spends no API quota, no agent tokens, and issues no GraphQL query. `-filter` and `-all-issues` keep their meaning, a list with no results is reported as no issues rather than an error, and a page that loads but cannot be read is reported once with its URL instead of on every poll.
- Read project boards through their Projects v2 page under `glorp watch -browser`, instead of the `projects(v2)` GraphQL API. A `projects:` target's items, repositories, titles, and `Status` column now come from the rendered board, so the `--ready-state` gate and the push-mode board probe work in browser mode without a GraphQL call. User, organization, and repository-scoped boards are all supported; draft cards and pull requests on the board are ignored. Moving an issue's status still goes through the API.
- Add an opt-in `glorp watch -browser` mode that drives GitHub through a headless Chrome instead of the GitHub API, with `-browser-profile` and `-browser-binary` to choose the profile directory and executable. Browser mode implies `-poll` (no webhook server and no ngrok tunnel are started), defaults `-interval` to 5s unless one is given, and fails with an actionable error when no Chromium-based browser can be launched. Without `-browser` nothing changes and no browser is started.

## v1.2.23 - 2026-08-30

- Require `author:@me` in addition to `assignee:@me` in the default `--filter`. Marking a repository issue ready for an agent now means opening it yourself *and* assigning it to yourself, so another user cannot trigger a run by assigning you an issue they filed.
- Fix `gh-fix` potentially resuming a CLOSED (unmerged) pull request when its branch name matched the `fix/issue-N-*` convention for the issue being fixed. The skill now only resumes an OPEN pull request, regardless of which signal matched it.

## v1.2.22 - 2026-08-30

- Mark repository issues ready for an agent by assigning them to yourself instead of labelling them. The default `--filter` is now `is:issue state:open assignee:@me`, glorp no longer creates or manages the `agent-ready` label on watched repositories, and the `gh-fix` skill assigns follow-up issues to the authenticated user rather than labelling them.

## v1.2.21 - 2026-08-29

- Answer the `Does anyone have this?` handoff ask when it is posted on an issue's pull request. Once a draft PR exists the handshake happens on the PR, but the running instance only recognised asks that named the issue number, so it stayed silent while actively committing and another instance took its work. Handoff ownership lookups also now resolve project-scoped work back to its repository.
- Read the ngrok tunnel URL from the log of the ngrok process glorp starts instead of the fixed local ngrok API on port 4040. When another ngrok agent already owns that port (commonly an orphaned tunnel from an earlier run), glorp used to adopt that agent's tunnel and point the GitHub webhook at a dead port while reporting the tunnel as ready. ngrok's own error output is now shown and included in the failure message when no tunnel comes up. `--ngrok-api` is deprecated and ignored.
- Fix `install.sh` silently skipping its final steps when piped into `bash`: the `npx skills add` calls consumed the piped script from stdin, so the installed-version message was echoed back as raw script text instead of running.
- Stop leftover ngrok agents from earlier glorp runs before starting a tunnel. An agent abandoned by a build that predates glorp's orphan guards (or whose reaper was killed with it) could keep running for weeks, holding the local ngrok API port and one of the account's simultaneous agent sessions, which made later runs fail with `ERR_NGROK_108`. Agents still owned by a running glorp are left alone.
- Stop resuming an issue with the agent recorded in `.glorp.json` when the current `-agent` configuration no longer includes it; the issue is dispatched to a configured agent with a fresh session instead.

## v1.2.20 - 2026-08-29

- Log the failing agent's actual output (not just the exit status) when a dispatched run fails, and include that output in the bug report link glorp generates, instead of a `[robot output omitted]` placeholder.
- Ignore `.glorp.json` state entries for repositories that are no longer being watched, so stale scoped keys from previous runs do not stop polling.
- Fix the webhook daemon starting an ngrok tunnel to its listener's wildcard address (`[::]`/`0.0.0.0`), which macOS refuses to connect to; ngrok now targets `127.0.0.1` on the bound port instead.

## v1.2.19 - 2026-08-29

- Fix `glorp upgrade` still re-running the installer for release builds whose embedded version omits the leading `v` from the latest GitHub release tag.

## v1.2.18 - 2026-08-24

- Add a settings icon to the browser dashboard that opens a modal for changing concurrency, ready-state label, allowed commenters, and the dispatched agent on a running instance without a restart.
- Have `gh-fix` crop native (non-browser) screenshots and recordings to the captured window's bounds instead of the full screen, unless the app itself is full screen.

## v1.2.17 - 2026-08-23

- Show the dispatched agent, model, and effort level in each job's TUI and web dashboard viewport.

## v1.2.16 - 2026-08-23

- Show the running glorp version in the browser dashboard's masthead.
- Skip the CI workflow's build-and-test jobs for pushes that only change Markdown files.
- Only skip dispatching an issue for having sub-issues when at least one sub-issue is still open, not merged or closed.

## v1.2.15 - 2026-08-23

- Disable Claude Code's headless background-task wait ceiling for dispatched `claude` runs, so long-lived autonomous work is no longer terminated mid-task after 10 minutes.

## v1.2.14 - 2026-08-22

- Keep the browser dashboard's Retry control enabled for queued jobs as well as active, failed, and completed work.
- Skip dispatching issues that contain sub-issues.

## v1.2.13 - 2026-08-21

- Have `gh-fix` attach a follow-up that addresses an unresolved dependency as a sub-issue of its related blocking issue.

## v1.2.12 - 2026-08-21

- Fix the `gh-fix` documentation guide's screenshot-proof image on the GitHub Pages site.

## v1.2.11 - 2026-08-21

- Replace the `gh-fix` guide's CSS-only screenshot-proof example with a representative dashboard image, so the proof section demonstrates the artifact it describes.
- Correct the GitHub CLI installation commands in the public skill guides to use `gh skill install`.
- Allow browser-dashboard jobs to be retried after completion or while active; active retries stop the current agent run before starting a fresh `gh-fix` pass.
- Retry a direct `@/glorp:ID` mention when GitHub's issue listing temporarily omits the mentioned issue, so its fresh threaded `gh-fix` run is not lost.
- Make the Pages skill-installation instructions agent-agnostic.
- Improve the public `gh-fix` guide with a stable two-line hero, a draft-PR checkpoint walkthrough, and an example of attached screenshot proof.
- Add linked GitHub Pages guides for the `gh-fix` and `gh-discuss` skills, with their installation commands, workflow diagrams, and GitHub-style visual walkthroughs.

## v1.2.10 - 2026-08-21

- Add Retry and Stop controls to each browser-dashboard job viewport, keeping unavailable actions visible but disabled, logging both intents, rerunning failed `gh-fix` work immediately, and cancelling active work on request.
- Fix the default `--filter` for repository targets no longer restricting dispatch to issues carrying the `agent-ready` label, so `glorp watch` on a repository once again only picks up labeled issues instead of every open issue authored by the current user.
- `glorp upgrade` now checks the latest published release before downloading anything. If the running binary already matches it, upgrade noops and prints the current version instead of re-running the installer.

## v1.2.9 - 2026-08-20

- Have the `gh-fix` skill prefer squash merges when the repository allows more than one merge method, falling back to a normal merge and then rebase, and use the PR's own title and body as the squash commit message instead of overriding it.
- Let issue comments directly address a running Glorp instance with `@/glorp:ID`, triggering a fresh threaded `gh-fix` pass that receives the instance identity and treats matching mentions as agent instructions. Only the *last* comment on the issue can trigger this, and only when posted by an allowed commenter — the authenticated `gh auth status` user by default, or the logins listed with the new `--allowed-commenters` flag.
- Immediately sweep issues referenced by a closed issue or pull request, resuming this instance's failed session or using the cooperative handoff before continuing work owned elsewhere.
- Have the `gh-fix` skill create actionable follow-up issues before stopping failed or stalled runs, associate them with the originating work, and route them to the matching project `Todo` status or the `agent-ready` label for Glorp-managed repositories.

## v1.2.8 - 2026-08-20

- `install.sh` and `install.ps1` now print the version that was installed instead of just the install path.

## v1.2.7 - 2026-08-20

- Fix `gh-fix` still losing track of its target repository even after v1.2.6: the repo was appended as a free-text `Repository: OWNER/REPO` line after the command, which a bare `/gh-fix N` dispatch could omit entirely (e.g. before an issue's checkout directory exists, the agent now runs outside glorp's own working directory per v1.2.5, so there was no git remote to fall back on either). `glorp` now passes the repository as part of the command itself, `/gh-fix OWNER/REPO#N`, so it can no longer be dropped.

## v1.2.6 - 2026-08-20

- Fix the `gh-fix` skill sometimes asking the user which repository to work in even though glorp always tells it, via a `Repository: OWNER/REPO` line in the dispatched prompt. The skill now uses that line directly instead of trying to infer the repository from `git remote -v`, which can fail or point elsewhere before the isolated clone exists.
- Have the `gh-fix` skill post a comment on the issue whenever it gives up before opening a pull request for any reason — not just when it stops during initial validation — so the reason is always visible on GitHub instead of only in the final report to the caller.

## v1.2.5 - 2026-08-19

- Document how `gh-fix` should upload UI screenshots and screen recordings to pull requests, using GitHub's undocumented `uploads.github.com` attachment endpoint with a bearer token instead of giving up for lack of a public API.
- Fix the coding agent sometimes targeting the wrong repository when `glorp watch` is watching more than one repo. Before an issue's checkout directory exists, the agent process now starts outside glorp's own working directory instead of inheriting it, so it can no longer mistake glorp's launch directory's git remote for the repository it was actually asked to work on.

## v1.2.4 - 2026-08-19

- Stop burning through GitHub's (much smaller) GraphQL rate limit for ordinary issue polling. The main issue-list poll and per-issue dependency lookups now use the REST Search and Issues APIs (`gh api search/issues`, `gh api repos/…/issues/…`) instead of `gh issue list`/`gh issue view`, both of which query GraphQL under the hood despite looking like plain REST commands. GraphQL is now only used for GitHub Projects (v2) boards and Discussions boards, which GitHub exposes exclusively through GraphQL. Also slow the per-issue closure/competing-claim polling loops from every 10 seconds to every 30, matching the default base poll interval rather than tripling its request rate, since every active issue runs two of these loops in parallel and each poll costs several API requests.
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
