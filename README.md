<p align="center">
  <img src="assets/glorp-logo.svg" alt="glorp robot patcher logo" width="280">
</p>

# glorp

**Git Loop fOr Robot Patchers** — a near automated loop for building. Open issues on GitHub, let the 🤖s handle the rest.

glorp runs a daemon that watches (push or poll) GitHub repositories or project boards for issues and spins up a coding agent for each detected issue with a configurable concurrency, tracking the issue to completion.

> [!NOTE]
> glorp launches agents non-interactively. Agent sandbox and permission controls remain enabled by default. `--yolo` disables those protections; use it only with repositories and issues whose contents you trust.

## Prerequisites

Install and configure these tools before installing glorp:

- [GitHub CLI](https://cli.github.com/) (`gh`), authenticated with access to every repository glorp will watch.
- [Node.js](https://nodejs.org/) and `npx`. The installer uses `npx` to install the bundled `gh-fix` and `gh-discuss` skills through skills.sh.
- [ngrok](https://ngrok.com/) for the default webhook mode. Installing it is optional: glorp runs `npx ngrok` when no `ngrok` is on `PATH`, so the Node.js above is enough. Configure ngrok authentication before starting glorp either way. ngrok is not required with `--poll`.
- A Chromium-based browser ([Chrome](https://www.google.com/chrome/), Chromium, or Edge) for `--browser` mode. Only required with `--browser`, which needs no ngrok tunnel and no extra token. Safari cannot be used: it does not speak the DevTools Protocol. Browser mode reads GitHub as a signed-in user would, and glorp's browser profile starts signed out, so run `glorp auth` once before watching anything private; see [`glorp auth`](#glorp-auth).
- At least one supported coding agent. glorp ships definitions for [Codex CLI](https://developers.openai.com/codex/cli/) (`codex`), [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`), [Gemini CLI](https://github.com/google-gemini/gemini-cli) (`gemini`), [Meta Muse Code](https://dev.meta.ai/docs/muse-code) (`muse`), [opencode](https://opencode.ai) (`opencode`), and [Cline](https://cline.bot) (`cline`). Any other compatible CLI can be added without waiting for a glorp release by declaring it in `.glorp.config.json`; see the [agent definitions reference](https://lsegal.github.io/glorp/agents/).

The Unix installer also requires `curl` and standard archive tools. The Windows installer requires PowerShell.

Authenticate the GitHub CLI before running glorp:

```sh
gh auth login
```

Browser mode needs no `gh` scopes of its own, but its browser profile does need a GitHub session. Sign it in once, before the first `glorp watch --browser` against a private repository or board:

```sh
glorp auth          # opens a browser window; sign in there
glorp auth --status # confirm it took
```

The session is stored in the profile directory and survives later runs, so this is a one-time step per profile.

Repository targets require permission to read issues, manage glorp's labels, and manage webhooks unless `--poll` is used. GitHub Project targets require the `read:project` scope to list items and the `project` scope to update their status. Push mode for organization-owned Projects also requires organization-owner access and the `admin:org_hook` scope:

```sh
gh auth refresh -s read:project -s project
gh auth refresh -s admin:org_hook # organization Project push mode only
```

## Installation

On macOS or Linux:

```sh
curl -fsSL https://github.com/lsegal/glorp/releases/latest/download/install.sh | bash
```

The script downloads the release for the current operating system and architecture, installs `glorp` into `~/.local/bin`, and installs the repository's `gh-fix` and `gh-discuss` skills globally through skills.sh, for every agent glorp ships a definition for. The installer asks the binary it just installed for that list (`glorp agents -skills`), so an agent added to the registry is covered without the installer changing.

On Windows PowerShell:

```powershell
irm https://github.com/lsegal/glorp/releases/latest/download/install.ps1 | iex
```

The PowerShell installer places `glorp.exe` in `%USERPROFILE%\AppData\Local\glorp`, adds that directory to the user `PATH`, and installs the same `gh-fix` and `gh-discuss` skills through skills.sh, for the same registry-derived agent list. Restart the terminal if `glorp` is not immediately found.

Installer behavior can be overridden with environment variables:

| Variable | Purpose | Default |
| --- | --- | --- |
| `GLORP_REPO` | Repository from which to download glorp and install `gh-fix`/`gh-discuss` | `lsegal/glorp` |
| `GLORP_VERSION` | Release tag to install, or `latest` | `latest` |
| `GLORP_BIN_DIR` | Destination directory for the executable | `~/.local/bin` on Unix; `%USERPROFILE%\AppData\Local\glorp` on Windows |

The public `.agents/skills/gh-fix` and `.agents/skills/gh-discuss` directories in this repository are the skills.sh package sources.

The installers cover the built-in agents only. An agent declared in `--config` may name its own skills.sh target with `"skills": {"target": "ID"}` -- a dedicated id such as `opencode`, `cline`, or `gemini-cli`, or `universal` for a CLI skills.sh has no dedicated id for -- and installs its own skills with the same command the installers use:

```sh
npx --yes skills add lsegal/glorp@gh-fix --global --agent ID -y
npx --yes skills add lsegal/glorp@gh-discuss --global --agent ID -y
```

`glorp agents` lists the agents in force, and `glorp agents -skills` lists the target ids they install skills for. The [agent definitions reference](https://lsegal.github.io/glorp/agents/) documents the whole definition schema, field by field, with a tutorial for registering a CLI glorp does not ship.

### Upgrading

```sh
glorp upgrade
```

The command re-runs the installer for the current platform, so it picks up the latest release along with the current `gh-fix` skill. `GLORP_REPO` and the other installer environment variables above still apply.

## Quick start

Every glorp invocation starts with a subcommand:

| Command | Description |
| --- | --- |
| `glorp watch [flags] [TARGET ...]` | Watch GitHub targets and dispatch agents for ready issues. |
| `glorp agents [flags]` | List the agents glorp can dispatch to, or with `-skills` the skills.sh target ids they install skills for. |
| `glorp ui [flags]` | Open a running glorp dashboard in a browser. |
| `glorp version` | Print the glorp version. |
| `glorp upgrade` | Upgrade glorp to the latest release. |
| `glorp help [command]` | Show help for glorp or one of its commands. |

Options must appear before the first target. A target can be an `OWNER/REPO`, a GitHub repository URL, a GitHub Project URL, or a GitHub Discussions board URL.

Watch a repository using the default Codex agent and webhook mode:

```sh
glorp watch owner/repo
```

By default, repository targets select open issues authored by the authenticated GitHub user. Watch all open issues instead:

```sh
glorp watch --all-issues owner/repo
```

Select issues using GitHub issue-search syntax:

```sh
glorp watch --filter "assignee:@me" --filter "-label:blocked" owner/repo
```

Run without ngrok or managed webhooks by polling every 30 seconds:

```sh
glorp watch --poll --interval 30s owner/repo
```

The browser dashboard is available at `http://localhost:8765` by default. If that port is occupied, glorp uses the next available port and logs the selected URL. Choose a different starting port, switch to the terminal dashboard, or disable UI entirely:

```sh
glorp watch --web-ui-port 9000 owner/repo
glorp watch --ui tui owner/repo
glorp watch --ui none owner/repo
```

`--no-ui` remains available as an alias for `--ui none`:

```sh
glorp watch --no-ui owner/repo
```

Open a running dashboard in a browser without hunting for its URL. `glorp ui` scans localhost from port 8765 upward, and when several instances are running it shows an interactive picker (or opens the lowest port when stdout is not a terminal):

```sh
glorp ui
glorp ui --port 9000
```

Use Claude Code and run up to three agent jobs concurrently:

```sh
glorp watch --agent claude --concurrency 3 owner/repo
```

Load balance work evenly across Codex and Claude by repeating `--agent`:

```sh
glorp watch --agent codex --agent claude --concurrency 4 owner/repo
```

Pick the model and reasoning level per agent with `--agent AGENT/MODEL:LEVEL`:

```sh
glorp watch --agent codex/gpt-5.6:high --agent claude/opus:medium --concurrency 4 owner/repo
```

Allow agents to run without sandbox or permission checks:

```sh
glorp watch --yolo owner/repo
```

Watch several repositories and projects in one process:

```sh
glorp watch --concurrency 3 owner/first owner/second https://github.com/orgs/example/projects/3
```

The concurrency limit is shared across all targets. GitHub webhook deliveries cause an immediate refresh, while `--interval` controls the periodic synchronization cadence.

Only deliveries that can change which issues are dispatchable cause a refresh. Closing an issue or pull request triggers an immediate continuation sweep for same-repository issues mentioned as `#123`: work from this instance resumes through the existing agent session, while unowned work uses the cooperative handoff before it continues. Pushes, other pull request activity, pings, and ordinary comments are logged and ignored, as are `issues` actions that leave labels, state, and dependencies untouched (`edited`, `assigned`, `locked`, and similar). In push mode, `--interval` therefore controls only the immediate follow-up refreshes that outlast GitHub's issue index lag; the periodic reconciliation that recovers missed deliveries runs every 15 minutes.

## How it works

In the default mode, glorp:

1. Starts a webhook listener on a randomly assigned available port.
2. Starts an ngrok tunnel for that listener.
3. Creates a GitHub webhook for each repository target or organization-owned Project and removes stale ngrok webhooks previously managed for it.
4. Queries GitHub for matching open issues and queues previously unhandled work.
5. Starts the selected agent with `/gh-fix ISSUE_NUMBER` and tracks its output and result.

For repository targets, opening an issue yourself and assigning it to yourself marks it eligible for pickup; glorp's default filter only dispatches open issues that the authenticated user both authored and is assigned to, so somebody else assigning you their issue cannot start a run. Ownership of a claimed issue is tracked entirely through the comment-based handoff protocol below rather than a label. Project items are moved through their configured status as work starts and finishes.

Organization-owned Projects use GitHub's `projects_v2_item` organization webhook event for immediate refreshes, plus a narrow `issues`/`issue_comment` webhook on each repository backing the board so a change to an issue an agent is already working reaches it straight away. GitHub does not provide that event for user-owned Projects, so personal project targets continue to refresh on `--interval`; use `--poll` to avoid starting an unused webhook tunnel for those targets.

`--browser` selects a different transport for the same loop. The default mode reaches GitHub through the API: webhook deliveries arrive over an ngrok tunnel for instant refreshes, and `--interval` polling through `gh` (30s by default) covers everything webhooks do not. Browser mode instead launches one headless Chromium-based browser for the whole run, gives each target a tab on it, and reloads those pages on a short loop, reading what a signed-in user would see. There is no webhook server, no ngrok tunnel, and no API rate limit to stay under, which is why the interval defaults to 20s rather than 30s. Reading the pages a signed-in user sees means the browser profile has to be signed in: it starts empty, and a private repository or board renders as a 404, a 403, or a login wall until it has a session. `glorp auth` opens a visible window on that same profile so you can sign in once; `glorp watch` itself never opens a window, and Chrome allows only one process per profile directory, so signing in is a separate step rather than something a running watch does on the side. `glorp auth --status` reports the profile's current state without opening anything. Because it always polls, `--browser` is rejected rather than silently ignored when combined with `--listen`, `--webhook-path`, `--webhook-secret`, or `--ngrok-binary`, and a browser that cannot be launched is a fatal error instead of a quiet fallback to the API path.

That profile has to be signed in to GitHub, once: `--browser-profile` defaults to a directory of glorp's own, and a fresh one is signed out. It matters more than it looks, because the default `--filter` is written in terms of `@me`. A signed-out session resolves `@me` to nobody, so GitHub answers with a real, correctly empty search result rather than an error, and the run would otherwise poll a repository full of ready work forever without dispatching any of it. glorp checks for this on any page that comes back with no rows and stops with the profile directory and a pointer to `glorp auth`, rather than reporting zero issues.

Reading a rendered page means depending on GitHub's markup, which can change. `--browser-vision` adds an opt-in safety net for that day: when the page extractor fails on a page that loaded fine, glorp hands a single screenshot to the configured agent and asks it for the issue numbers. It is bounded hard in code (one screenshot per target per 10 minutes, three per run, then off) and logs every call it makes, because the steady state is meant to be zero AI calls. Recovering a list this way is a signal that the extractor needs fixing, not a mode to run in.

When a browser-mode run is misbehaving, `--no-headless` shows you the browser instead of making you infer it from CDP traffic: the run is identical, on the same profile and the same interval, but its window is on screen so you can watch each poll navigate and see what GitHub actually served.

Handled issues and active sessions are stored in `.glorp.json` by default. This prevents duplicate work after a restart and allows glorp to resume an interrupted session with the original agent, even when the new process selects a different `--agent`. If the original working directory is gone, the resumed agent is told to regenerate its missing work. Two cases restart the work instead of resuming it. Work pinned to an agent the current configuration no longer dispatches to is not resumed with the retired agent: the persisted session is discarded, logged as `discarded persisted agent "NAME"; it is no longer configured`, and the issue is redispatched to a currently configured agent with a fresh session. Agents whose definition declares `"session": {"assign": "none"}` -- `opencode` and `cline` among the built-ins -- have no resumable session at all, so picking their work back up always restarts the agent with the recovery prompt; because `gh-fix` is re-entrant, the restarted run adopts the branch and draft pull request the previous one left open rather than starting the issue over. Issues that declare dependencies using `depends on #123` or GitHub's issue-dependency relationship remain blocked until those dependencies close.

Each glorp instance gets a random in-memory identity used to cooperate with other instances (including ones on other machines) sharing the same repository. Before reaping an issue that has no local record of being this instance's own work, glorp posts a signed `Does anyone have this? /glorp:<identity>` comment on the issue (or its open draft pull request, if one exists) and waits at least two minutes before claiming it with a `Starting work on this issue` or `Continuing work on this issue` comment. If another instance answers first, glorp stands down and leaves the ticket alone; the last instance to post a claim always wins. An instance that already holds the newest claim on a ticket skips the handshake and picks the work back up silently, rather than re-asking a question it has already answered.

Repository webhooks also subscribe to GitHub's `issue_comment` event so this handshake does not depend on the polling interval: when a `Does anyone have this?` comment arrives for a ticket this instance is actively working on, glorp replies immediately with a signed `I am working on this` comment instead of waiting for the next poll.

glorp terminates every subprocess it starts — the ngrok tunnel, agent runs, and helper commands — when it shuts down, including on `SIGINT`, `SIGTERM`, and `SIGHUP`. It also arranges the same should glorp be killed outright and run no cleanup at all: on Linux each subprocess carries a parent-death signal, and on Windows each one joins a job object that is destroyed with glorp. macOS and the BSDs offer no kernel equivalent, so glorp there keeps a small reaper process that holds a pipe to glorp and records the process groups glorp owns; the kernel closes that pipe when glorp dies, and the reaper terminates whatever is left. A `SIGKILL` of `glorp watch` therefore leaves no tunnel or agent behind on any supported platform.

Glorp serves either a localhost-only browser dashboard or an interactive terminal dashboard, selected by `--ui`. The browser dashboard mirrors the terminal dashboard's agent cards, output viewports, scrolling behavior, daemon logs, job counts, quota, delivery mode, and target status. `--ui web` is the default, `--ui tui` enables the terminal dashboard when stdout is a terminal, and `--ui none` writes timestamped progress to stdout. `--no-ui` is equivalent to `--ui none`.

## CLI reference

```text
glorp <command> [arguments]
```

| Command | Description |
| --- | --- |
| `watch` | Watch GitHub targets and dispatch agents for ready issues. |
| `agents` | List the agents glorp can dispatch to, or with `-skills` the skills.sh target ids they install skills for. |
| `ui` | Open a running glorp dashboard in a browser. |
| `auth` | Sign glorp's browser profile in to GitHub for `--browser` mode. |
| `version` | Print the glorp version. |
| `upgrade` | Upgrade glorp to the latest release. |
| `help` | Show help for glorp or one of its commands. |

`glorp --version` and `glorp -h` are accepted as aliases for `glorp version` and `glorp help`. Running `glorp` with no command, or with a target instead of a command, prints the command list and exits with status 2.

### `glorp watch`

```text
glorp watch [options] [TARGET [TARGET ...]]
```

If no `TARGET` is given, glorp uses the current directory's `origin` git remote when it points to a GitHub repository.

| Option | Default | Description |
| --- | --- | --- |
| `--agent AGENT[/MODEL][:LEVEL]` | `codex` | Agent to run, with an optional model and reasoning level, such as `claude`, `claude/opus`, or `codex/gpt-5.6:high`. glorp ships definitions for `codex`, `claude`, `gemini`, `muse`, `opencode`, and `cline`; `glorp agents` lists the ones in force. Each accepts the levels its own definition names -- `low`, `medium`, and `high` for `codex`, `claude`, and `opencode`, those plus `none` and `xhigh` for `cline`, and `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `ultra` for `muse` -- and a level outside that set is rejected here with the list. Any agent declared in `--config` is accepted too, on the models and levels its own definition lists; see the [agent definitions reference](https://lsegal.github.io/glorp/agents/) for how to add one. An agent whose CLI has no reasoning-effort flag -- `gemini` is the built-in one -- declares an empty level list and rejects a level outright, so `--agent gemini:high` stops the run naming the agent instead of accepting a level it has nowhere to pass. Repeatable; when given more than once, new issues are load balanced evenly across the listed agents, each using its own model and level. |
| `--agent-binary NAME=PATH` | the definition's own `binary` | Executable to invoke one agent through, overriding the `binary` its definition names. Repeatable, once per agent, and the only way to point a `--config` agent at a different install. It outranks `--claude-binary` and `--codex-binary`, and the executable it names is used for the agent's quota command as well as its runs. An unregistered agent name is rejected with the agents that do exist. |
| `--all-issues` | `false` | Disable the default issue-search filter and consider all open issues. The `is:issue state:open` qualifiers glorp always adds still apply. |
| `--allowed-commenters LOGINS` | the authenticated `gh` user | Comma-separated GitHub logins allowed to trigger a direct `@/glorp:ID` mention run. |
| `--browser` | `false` | Read GitHub through a headless Chromium-based browser instead of the GitHub API. Implies `--poll`, so no webhook server and no ngrok tunnel are started, and it cannot be combined with `--listen`, `--webhook-path`, `--webhook-secret`, or `--ngrok-binary`. Unless `--interval` is given explicitly it also shortens the interval to `20s`. |
| `--browser-binary PATH` | auto-detected | Chromium-based browser executable for `--browser`. Resolved through `PATH`, so a bare name works as well as a full path. By default glorp looks for `google-chrome`, `google-chrome-stable`, `chromium`, `chromium-browser`, or `msedge`, then the usual per-platform install locations. |
| `--browser-profile PATH` | `<config dir>/glorp/browser-data` | Browser profile directory for `--browser`. Defaults to glorp's own profile, kept separate from your everyday browsing profile: `~/Library/Application Support/glorp/browser-data` on macOS, `%AppData%\glorp\browser-data` on Windows, and `~/.config/glorp/browser-data` on Linux. |
| `--browser-vision` | `false` | In `--browser` mode only, let an agent read a screenshot of an issues page whose markup glorp no longer recognises. Off by default and never used on the success path: it fires only on the extraction failure above, at most once per target per 10 minutes and at most 3 times per run, after which it disables itself for the rest of the run. The agent is asked for a bare JSON list of issue numbers and anything else is discarded without a retry. It is a bridge until the page extractor is fixed in code, not a replacement for it.
| `--no-headless` | `false` | In `--browser` mode only, drive a visible browser window instead of a headless one. Nothing else about the run changes: the same profile, the same tabs, the same poll interval — you just get to watch the pages glorp reads while it reads them, which is the fastest way to see why an extraction failed. It is refused on a machine where no window can appear (a Linux session with neither `DISPLAY` nor `WAYLAND_DISPLAY` set) rather than launching a browser nobody can see, and it cannot be used without `--browser`. |
| `--claude-binary PATH` | `claude` | Claude Code executable name or path. A legacy alias for `--agent-binary claude=PATH`, kept for the scripts that already pass it; `--agent-binary` wins when both are given, and it is the only one of the three that reaches an agent other than `codex` or `claude`. |
| `--codex-binary PATH` | `codex` | Codex executable name or path. A legacy alias for `--agent-binary codex=PATH`, kept for the scripts that already pass it; `--agent-binary` wins when both are given. |
| `--concurrency N` | `0` | Maximum concurrent agents across all targets. `0` is normalized to `3`; negative values are invalid. |
| `--filter QUERY` | `assignee:@me author:@me` | GitHub issue-search filter. Repeat the option to combine terms. glorp always searches for open issues, so `is:issue state:open` (`is:issue is:open` on a Project board) is added to every query and is not part of the default: a filter of your own does not have to repeat it, and a kind or state it names of its own is dropped rather than dispatching a closed issue or a pull request. The default requires that you both opened the issue and assigned it to yourself, so another user cannot trigger a run by assigning you an issue they filed. It applies to repository targets; Project targets default to all open project issues. |
| `--interval DURATION` | `30s` | Periodic GitHub synchronization interval. Uses Go duration syntax such as `10s`, `2m`, or `1h30m`; must be positive. |
| `--listen ADDRESS` | `:0` | Address for the local GitHub webhook HTTP server. Port `0` selects an available port automatically. |
| `--ngrok-api URL` | `http://127.0.0.1:4040` | Deprecated and ignored. The public tunnel URL is read from the log of the ngrok process glorp starts. |
| `--ngrok-binary PATH` | `ngrok` | ngrok executable name or path. Left at its default, glorp falls back to `npx --yes ngrok` when no `ngrok` is installed; set to anything else, the named executable must exist. |
| `--ui MODE` | `web` | Select the UI: `web`, `tui`, or `none`. |
| `--no-ui` | `false` | Disable all UI; equivalent to `--ui none`. |
| `--poll` | `false` | Use polling without starting ngrok or configuring GitHub webhooks. |
| `--ready-state NAME` | auto (`Todo` or `Ready`) | Project status that marks an issue ready for an agent; matching is case-insensitive. |
| `--remote-control` | `false` | Ask Claude runs to start Remote Control so a headless agent is viewable, and steerable, from the Claude mobile app and claude.ai/code. Claude receives `--settings {"remoteControlAtStartup":true}`, layered on top of your own settings, plus `--rc "glorp owner/repo#N"` so concurrent runs are told apart by issue instead of sharing a host name. **Claude does not honour this under `-p`**: measured against Claude Code 2.1.248, a headless run started with exactly these arguments starts no bridge and never reaches the app, which is why the flag is off by default. It is kept as an opt-in so a Claude release that reads the setting in print mode needs no change here. **No substitute lever exists either** (issue #506): `autoUploadSessions`, the hoped-for view-only mirror, is not a Claude Code setting at all — it is absent from the settings reference, and a `-p` run with every debug category on makes no upload or session-share request of any kind. `claude remote-control`, the separate server mode, hosts only the sessions it creates itself: its `--session-id` takes a server-side code session rather than a local session UUID, so reattaching to a run glorp started is rejected as `invalid session ID: must be a cse_… or session_… tagged ID`, and it also refuses to start until the workspace trust dialog has been accepted interactively — which a `-p` run never triggers, because print mode skips that dialog — and then asks `Enable Remote Control? (y/n)` on the terminal. The remaining answer is a Claude Code change, filed upstream as [anthropics/claude-code#91906](https://github.com/anthropics/claude-code/issues/91906); until it lands, a run is followed in glorp's own dashboard, which is localhost only, and turning the flag on prints that once at startup rather than silently reaching nobody. Codex runs are unaffected and stay viewable in glorp's own dashboard only, so opting in on a watch that has Codex configured prints that once at startup rather than leaving the run's absence from the phone unexplained. `codex exec` takes no Remote Control argument, and Codex's separate `codex remote-control` daemon is not a way around that: it is the app-server hosting the threads it starts itself, so glorp has nothing to hand it an already-running `codex exec`, its `start`, `stop`, and `pair` commands run on Unix only, and pairing a device to it is a manual step. Reaching Codex runs from a phone would mean driving Codex over the app-server protocol instead of `codex exec` altogether. |
| `--state PATH` | `.glorp.json` | File used to persist handled issues and active session state. |
| `--config PATH` | `.glorp.config.json` | Agent definition file. Every agent glorp can dispatch to is described by a JSON definition -- the executable, the argv for a fresh run, a resume, and a vision call, the environment its child gets, how its session ID is established, and which models and levels `--agent` accepts -- and this file overrides the definitions glorp ships or adds new ones, so another CLI is a JSON blob rather than a new build. How an agent's output is read is part of the definition too: `"output": {"format": "plain"}` shows it as written, `"claude-stream-json"` decodes Claude's event envelope, and `"jsonl"` decodes a line-delimited JSON stream described by field paths -- `{"format": "jsonl", "jsonl": {"type": "event", "text": "delta.text", "toolName": "delta.tool.name", "toolInput": "delta.tool.arguments", "ignore": ["usage"]}}` -- where a key suffixed with `[]` steps into an array, a line that is not JSON is passed through, and an event the paths name nothing in renders nothing. A CLI whose JSON mode streams token-sized fragments rather than whole messages adds `"textDelta": true` beside `"text"`, which buffers the fragments and ends the line on the first event carrying no text instead of writing a line per token; the built-in `muse` definition is the one that needs it. Every definition must declare `args.run` and, unless its `"session": {"assign": "none"}` says it has no resumable session, `args.resume`; a sessionless agent that leaves `args.resume` out has a resume render its `run` template, so a recovery re-runs the work with the recovery prompt rather than failing. `"missingSession": ["thread has expired"]` adds, for that agent alone, phrases glorp reads as "the session you asked me to resume is gone" before it restarts the work, on top of the shared ones every agent is matched against. A definition may declare the lowest version of its executable its argv is known to work with, as `"minVersion": "0.58.0"` beside `"binary"`; glorp asks the binary for its version before dispatching to it and fails with an error naming the agent, the version found, and the version required instead of letting an older CLI die on an argument it has never heard of, while a binary whose version cannot be read warns and runs anyway. The built-in `gemini` definition declares `0.58.0`, the release its `--session-id`, `--resume`, and `--output-format` arguments were measured against; a definition that declares no minimum is not checked at all, and costs no process. A definition may also name the skills.sh target its copy of `gh-fix`/`gh-discuss` installs for with `"skills": {"target": "ID"}`; the installer scripts derive their `skills add --agent` list from the built-in definitions, so a config-defined agent installs its own skills with that command. It may name a quota source with `"quota": {"reader": "..."}`: `none` (the default, reported as untracked and costing no process on any poll), `codex`, `claude`, or `command`. The `command` reader runs the argv in `"command"` -- where `{binary}` substitutes the executable the agent was resolved to -- parses its stdout as JSON, and renders `"format"` from the dotted field paths in `"percentUsed"` and `"resetAt"`, substituting `{percentUsed}`, `{percentLeft}`, and `{resetAt}`; `"format"` defaults to `{percentLeft}% left` and `"timeout"` bounds one read, defaulting to `30s`. A reading that fails leaves the last good one in the status bar rather than blanking it. Its shape is `{"agents": [...]}`, an array of definitions or an object keyed by agent name. The `levels` and `models` allow-lists have three states: leaving one out accepts any value, listing values accepts exactly those, and an empty list `[]` accepts none at all, for a CLI that has no such flag -- which is how a level is rejected at the `--agent` prompt rather than parsed and then dropped for want of a `{level}` fragment to render it into. A definition whose name matches a built-in overrides it field by field, keeping every field it does not mention; a field given as `null` drops what it inherited, which for an allow-list restores accepting anything rather than accepting nothing; an unknown name registers a new agent. A missing file is not an error, and glorp only ever reads this one -- work state stays in `--state`, which glorp rewrites, so definitions kept there would be lost on the next save. Malformed JSON, an unknown field, or an invalid value stops the run naming the file, the agent, and the field, since a definition dropped quietly is indistinguishable from a typo in `--agent`. The [agent definitions reference](https://lsegal.github.io/glorp/agents/) documents every field of the schema with defaults and worked examples, and walks through registering a CLI glorp does not ship. |
| `--web-ui-port PORT` | `8765` | Starting localhost port for the browser dashboard; glorp uses the next available port if occupied. |
| `--webhook-path PATH` | `/webhook` | HTTP path that accepts GitHub webhook deliveries. |
| `--webhook-secret SECRET` | empty | Shared secret used to verify GitHub `X-Hub-Signature-256` signatures. The same secret is set when glorp creates each webhook. |
| `--yolo` | `false` | Disable the selected agent's sandbox, approval, and permission checks. Each agent's definition decides what that means for it: a fragment guarded on `"when": "yolo"` renders the CLI's own bypass flag (`--dangerously-bypass-approvals-and-sandbox` for Codex, `--dangerously-skip-permissions` for Claude, `--yolo` for Gemini CLI and Muse), and a fragment guarded on `"when": "!yolo"` renders the safer default the rest of the time. An agent whose definition names no bypass is unaffected by the flag. |

### `glorp ui`

```text
glorp ui [options]
```

Finds glorp dashboards by probing 16 consecutive localhost ports and opens one in the default browser. A port only counts as a dashboard when it answers glorp's own state endpoint, so unrelated local servers are skipped. When exactly one instance is found it opens directly; with several, an interactive picker appears on a terminal and the lowest port is opened otherwise. Exits with status 1 when nothing is running.

| Option | Default | Description |
| --- | --- | --- |
| `--port PORT` | `8765` | First localhost port to scan. |

### `glorp auth`

```text
glorp auth [options]
```

Signs the browser profile that `glorp watch --browser` reads GitHub with in to GitHub. The profile starts signed out, so private repositories and project boards render as a 404, a 403, or a login wall until it has a session.

`glorp auth` opens an ordinary, visible browser window on glorp's own profile at `https://github.com/login` and waits until the sign-in finishes, then closes it and prints the account it signed in as. The session lives in the profile directory, so it survives later `glorp watch` runs and only has to be done once per profile. glorp gives up after 5 minutes rather than leaving the window open indefinitely.

`glorp watch --browser` also does this for you. When a poll reads a page GitHub served to a signed-out session — a 404 or a 403 on a repository or board you asked it to watch, or a page carrying GitHub's own signed-out markers — the run logs what it saw, stops its headless browser, opens the same login window on the same profile, and resumes polling once you have signed in. A sign-in that is declined, times out, or cannot happen is not offered again on the next poll: the run backs off (10 minutes, doubling to an hour), keeps watching in the meantime, and leaves `glorp auth` available at any time.

Chrome allows only one process per profile directory, so stop a running `glorp watch --browser` (or point `--browser-profile` at a different directory) before signing in yourself. On Linux with no display server — `DISPLAY` and `WAYLAND_DISPLAY` both unset — the command refuses to open a window and says so instead of hanging; sign in on a desktop session and point `--browser-profile` at that profile directory.

| Option | Default | Description |
| --- | --- | --- |
| `--status` | `false` | Report whether the profile is currently signed in, and as whom, without opening a window. Exits 0 when signed in and 1 when not. |
| `--browser-binary PATH` | auto-detected | Chromium-based browser executable, resolved the same way as `glorp watch --browser-binary`. |
| `--browser-profile PATH` | `<config dir>/glorp/browser-data` | Profile directory to sign in. Defaults to the same profile `glorp watch --browser` uses: `~/Library/Application Support/glorp/browser-data` on macOS, `%AppData%\glorp\browser-data` on Windows, and `~/.config/glorp/browser-data` on Linux. |

Supported target forms are:

```text
owner/repository
https://github.com/owner/repository
https://github.com/users/OWNER/projects/NUMBER
https://github.com/orgs/OWNER/projects/NUMBER
https://github.com/OWNER/REPOSITORY/projects/NUMBER
https://github.com/OWNER/REPOSITORY/discussions
https://github.com/OWNER/REPOSITORY/discussions/categories/CATEGORY
projects:OWNER/REPOSITORY/NUMBER
projects:NUMBER
discussions:OWNER/REPOSITORY/CATEGORY
discussions:OWNER/REPOSITORY
discussions:CATEGORY
```

The `projects:` and `discussions:` forms are shorthands for the URLs above. The `OWNER/REPOSITORY` prefix may be omitted inside a git checkout whose `origin` remote points at a GitHub repository, so `glorp watch projects:3 discussions:q-a` watches project 3 and the Q&A discussions category of the current repository. A discussions category is named by its URL slug (`q-a`) or its display name (`Q&A`); without one, every category is watched.

A Discussions board target (`https://github.com/OWNER/REPOSITORY/discussions`) is watched differently from repository and Project targets: instead of the `gh-fix` skill, glorp dispatches the read-only `gh-discuss` skill for each new top-level Discussion thread that has no replies yet. `gh-discuss` only reads the repository to answer the question and posts a single top-level reply when it can do so accurately and positively; otherwise it leaves the thread untouched. Discussions targets work in both push and poll mode: in push mode glorp subscribes the repository webhook to GitHub's `discussion` event so a new thread dispatches immediately, with the periodic synchronization interval as the fallback. They are not affected by `--filter` or `--all-issues`, and do not use issue assignment or the comment-based ownership handoff protocol described below.

Press `q` or `Ctrl+C` to exit the interactive dashboard. glorp waits for running agents during shutdown.
