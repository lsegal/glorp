---
title: Agent definitions
description: "Every coding agent glorp dispatches to is a JSON document: the reference for .glorp.config.json, the agent-definition schema, and how to register a CLI glorp has never heard of."
---

# Agent definitions

glorp does not know how to talk to any agent in Go. Every CLI it dispatches to — the executable, the argv for a fresh run, a resume, and a vision call, the environment its child process gets, how its session ID comes to exist, how its output is decoded, where its quota is read, and which skills.sh target its skills install for — is described by a JSON **definition**. glorp ships definitions for seven agents and reads more from a config file, so supporting another CLI is a JSON document rather than a new glorp release.

This page is the reference for that file and that schema.

## The agents glorp ships

`glorp agents` reports on the agents in force for the current configuration -- whether each CLI is installed, its version, whether it is signed in, its quota, and the `agent/model` names `--agent` accepts. `glorp agents -names` prints one name per line instead, and `glorp agents -skills` prints the skills.sh target ids they install skills for. The built-in set is:

| `--agent` | CLI | Levels accepted | Default model | Session resume | Quota | skills.sh target |
| --- | --- | --- | --- | --- | --- | --- |
| `codex` | [Codex CLI](https://developers.openai.com/codex/cli/) | `low`, `medium`, `high` | `gpt-5.6-terra` | yes — Codex prints the ID, glorp reads it back | `codex` | `codex` |
| `claude` | [Claude Code](https://docs.anthropic.com/en/docs/claude-code) | `low`, `medium`, `high` | `sonnet` | yes — glorp assigns the ID | `claude` | `claude-code` |
| `gemini` | [Gemini CLI](https://github.com/google-gemini/gemini-cli) | none — the CLI has no reasoning-effort flag | `gemini-3.5-flash` | yes — glorp assigns the ID | none | `gemini-cli` |
| `muse` | [Meta Muse Code](https://dev.meta.ai/docs/muse-code) | `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `ultra` | none — per-account catalog | yes — glorp assigns the ID | none | `universal` |
| `opencode` | [opencode](https://opencode.ai) | `low`, `medium`, `high` | none — per-account catalog | no — recovery restarts the work | none | `opencode` |
| `cline` | [Cline](https://cline.bot) | `none`, `low`, `medium`, `high`, `xhigh` | none — per-account catalog | no — recovery restarts the work | none | `cline` |
| `agy` | [Antigravity CLI](https://antigravity.google/docs/cli/overview) | `low`, `medium`, `high` | `claude-sonnet-4-6` | no — recovery restarts the work | none | `antigravity-cli` |

An agent with no resume support is not a degraded one. glorp's recovery prompt asks the agent to pick the work back up from the branch and the open draft pull request, and the `gh-fix` skill is re-entrant by design, so a restarted run adopts what the previous one left behind instead of starting over.

None of the built-ins names a `models` allow-list: whatever `--agent NAME/MODEL` names is passed straight through to the CLI, because these CLIs take a live catalog and a list that validated it would reject a model released this morning. `glorp agents` enumerates them all the same, and it enumerates them by asking each CLI rather than by carrying a list of its own, which would be stale the morning after a vendor ships. `opencode` and `agy` list their own with `opencode models` and `agy models`, filtering each line through `doctor.modelPattern`, and `codex` prints its catalog with `codex debug models`; `cline` and `gemini` have no listing subcommand but answer over their agent-client protocol, so the probe runs `--acp` and reads the models the new session reports; `muse` answers `model/list` over `muse serve`. `claude` is the one built-in that cannot be asked at all -- its CLI has no listing command and no protocol that carries one -- so it declares `doctor.modelsNote` and the report says so instead of naming models it cannot check. An agent whose CLI is signed out of its provider lists nothing, and the report says which command could not list it.

`gemini` is the built-in that declares `"levels": []`. That is not the same as naming no list at all: an empty list accepts nothing, so `--agent gemini:high` stops the run naming the agent instead of accepting a level the definition has no `{level}` fragment to pass on. See [allow-lists](#allow-lists) below.

## `.glorp.config.json`

Agent definitions live in `.glorp.config.json` in the directory glorp is started from. `--config PATH` reads a different file. A missing file is not an error — glorp runs on the built-in definitions alone.

**glorp does not rewrite this file as it works.** That is the whole reason it exists separately from the state file — the one exception is the `settings` section below, which the web dashboard saves a changed setting into, leaving every other section and every switch it does not mention untouched:

| File | Flag | Who writes it | What it holds |
| --- | --- | --- | --- |
| `.glorp.config.json` | `--config PATH` | you, by hand; the dashboard, when a setting is changed | agent definitions and default switch values |
| `.glorp.json` | `--state PATH` | glorp, on every dispatch | handled issues and active sessions |

Putting a definition in `.glorp.json` is the mistake this split exists to prevent: the next state save rewrites that file and the definition is gone. glorp catches the mix-up in the other direction too — hand it a work-state file as `--config` and it reports the state record it found by name rather than a pile of unknown fields.

The file's top level is an object with two sections defined so far, `agents` and [`settings`](#settings):

```json
{
  "agents": [
    { "name": "my-agent", "binary": "my-agent", "...": "..." }
  ]
}
```

`agents` may also be an object keyed by agent name, in which case a definition's own `name` field is optional — and when it is present, it has to agree with its key:

```json
{
  "agents": {
    "my-agent": { "binary": "my-agent", "...": "..." }
  }
}
```

Any other top-level section is rejected by name, so a typo is reported rather than ignored.

## `settings`

The `settings` section holds default values for the switches `glorp watch` takes, so a run's usual configuration lives in the file rather than in a shell alias. It is an object keyed by switch name — the same names `glorp help watch` prints, with or without the leading dashes:

```json
{
  "settings": {
    "concurrency": 5,
    "pollmode": "poll",
    "interval": "90s",
    "no-headless": true,
    "ready-state": "Queued",
    "agent": ["claude/opus", "codex"],
    "filter": ["is:open label:bug", "is:open label:chore"]
  }
}
```

Every switch may be given here except `--config`, which selects the file being read and so cannot be set from inside it. Values follow the switch's own type: a string for a string or duration switch, a number for a numeric one, `true`/`false` for a boolean, and an array for a repeatable switch such as `--agent`, `--agent-binary`, or `--filter`, whose entries are applied in the order they are written.

**The command line wins.** The section supplies defaults, not overrides: a switch written on the command line keeps the value given there, and the file only fills in what was left alone. An unknown switch name, a value the switch will not accept, or a value that is not a string, number, boolean, or array of those stops the run naming the file and the key, for the same reason a malformed agent definition does — a setting dropped quietly is indistinguishable from a typo.

The web dashboard's settings modal writes back to this section. Changing concurrency, the ready state, the allowed commenters, or the active agent set applies to the running instance and is saved as `concurrency`, `ready-state`, `allowed-commenters`, and `agent`, so the change survives a restart. Nothing else in the file is touched, and a file that cannot be written is logged rather than failing the change that has already taken effect.

### Merge and override rules

Definitions are loaded built-ins first, then the config file on top:

- A definition whose `name` matches a **built-in overrides it field by field**. Fields the document does not mention keep the built-in's value, so pointing `claude` at a different binary is a two-line document, not a copy of the whole definition.
- A field given as **`null` drops what it inherited** and falls back to the schema's own default, which is how you remove a built-in's `env`, `quota`, or `levels` rather than override it.
- A **name the registry does not know registers a new agent**, immediately usable as `--agent NAME`.

Anything wrong with the file stops the run, naming the file, the agent, and the field: malformed JSON, a field the schema does not define, an invalid value, or a definition that fails validation. Nothing is dropped silently, because an agent missing from the registry is indistinguishable, at the `--agent` prompt, from a typo.

## The definition schema

### Top level

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | The name used in `--agent name/model:level`. Letters, digits, dot, dash, and underscore only, since `/` and `:` are that syntax's separators. |
| `binary` | string | yes | — | The executable the agent is invoked through. `--agent-binary NAME=PATH` overrides it per run. |
| `minVersion` | string | no | none | The lowest version of `binary` the definition's argv works with, as a dotted version such as `"0.58.0"`. See [`minVersion`](#minversion). |
| `args` | object | yes | — | The argv templates. See [`args`](#args). |
| `env` | object of string→string | no | none | Extra environment for the child process, layered on top of glorp's own environment. |
| `session` | object | yes | — | How the session ID is established. See [`session`](#session). |
| `levels` | array of string | no | any | Allow-list for the `:level` part of `--agent`. See [allow-lists](#allow-lists). |
| `models` | array of string | no | any | Allow-list for the `/model` part of `--agent`. See [allow-lists](#allow-lists). |
| `defaultModel` | string | no | none | The model glorp runs the agent with when `--agent` names none. See [`defaultModel`](#defaultmodel). |
| `output` | object | yes | — | How stdout is decoded. See [`output`](#output). |
| `missingSession` | array of string | no | none | Extra phrases that mean "the session you asked me to resume is gone". See [`missingSession`](#missingsession). |
| `quota` | object | no | `{"reader": "none"}` | Where the status bar's quota reading comes from. See [`quota`](#quota). |
| `skills` | object | no | none | The skills.sh target the agent's skills install for. See [`skills`](#skills). |
| `doctor` | object | no | none | The read-only probes `glorp agents` runs to report on the agent. See [`doctor`](#doctor). |

### Allow-lists

`levels` and `models` validate the two optional halves of `--agent name/model:level`, and each has **three** distinct states, because "this definition names no list" and "this CLI has no such flag at all" are different claims:

| JSON | Meaning |
| --- | --- |
| field absent, or `null` | Accepts any value. |
| `["low", "medium", "high"]` | Accepts exactly those, and rejects anything else at the `--agent` prompt, with the list. |
| `[]` | Accepts nothing: the CLI has no such flag, so naming one is an error rather than a value silently dropped. |

The empty list is what keeps a level from being parsed and then quietly discarded for want of a `{level}` fragment to render it into — indistinguishable, from the outside, from a level that was honoured. `gemini` declares `"levels": []` for exactly that reason.

Overriding an allow-list with `null` restores accepting *anything*, not accepting nothing; write `[]` when you mean the latter.

### `defaultModel`

Agent CLIs mostly default to their largest model, which is the wrong thing to point a queue of issues at. A definition may name the model glorp runs it with when `--agent` gives it none, so the choice is glorp's rather than the CLI's:

```json
{"name": "codex", "defaultModel": "gpt-5.6-terra"}
```

The default is resolved where the `--agent` spec is parsed, so it is the model the rendered argv passes, the model the dashboard shows, and the model written into the `agent/model:level` string the work state persists -- a resumed session keeps the model it started with even if the default later moves. An explicit `--agent codex/gpt-5.6-sol` always wins, and `glorp agents` prints the default on its own `default` line.

The value must be one `models` admits when that allow-list names values. A config file that narrows `models` on a built-in therefore has to name a `defaultModel` the narrowed list allows, or drop the inherited one with `"defaultModel": null`, rather than have glorp quietly run a model the file just said it would not accept.

Leaving the field out keeps the older behaviour: no `{model}` fragment is rendered and the CLI decides for itself. That is what a built-in whose catalog is per-account has to do, because there is no model id glorp can promise every account has: `muse`, `cline`, and `opencode` name no default deliberately. Their catalogs are fetched for the signed-in account -- `cline` answers with 289 provider-prefixed ids drawn from whatever that account can reach, `opencode` with 20 of the same shape, and `muse` with none at all, since its rows are hidden from the wire until an account is entitled to them -- so any id written into the definition would fail the dispatch outright on an account that does not carry it, which is strictly worse than letting the CLI choose. Naming a model explicitly still works for all three.

`gemini` and `agy` do name one. Gemini CLI builds its `availableModels` list inside the CLI rather than fetching it for the account, so `gemini-3.5-flash` is promised by the binary the same way `codex`'s and `claude`'s defaults are, and it is the mid-tier of what that list offers rather than the `gemini-3.1-pro-preview` at the top of it. `agy` is the one whose default is constrained by its own argv: it renders `{level}` into `--effort`, and almost every id its catalog offers spells the reasoning level into the id itself (`gemini-3.8-flash-medium`, `gemini-3.1-pro-high`), so a default like that would have glorp dispatch `--model gemini-3.8-flash-medium --effort high` and leave the CLI to decide which of the two contradictory levels wins. `claude-sonnet-4-6` is the mid-tier id in that catalog with no level embedded in it, so the `--effort` fragment stays the only thing that names one.

### `args`

`args` carries one argv template per shape of invocation glorp makes. `run` is always required, and `resume` is required of every agent glorp can hold a session ID for; `vision` is optional and an agent without one is simply never asked to read a screenshot.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `args.run` | array of fragment | yes | A fresh dispatch. |
| `args.resume` | array of fragment | unless `session.assign` is `"none"` | Continuing an earlier session. An agent with no resumable session may leave it out: a resume renders `args.run` instead, restarting the work with the recovery prompt. Declaring one anyway overrides that fallback. |
| `args.vision` | array of fragment | no | Browser mode's one-shot screenshot read (`--browser-vision`). |

Each template is a list of **fragments**, appended in order. A fragment is deliberately not an expression language: it tests one named value for presence, and nothing else.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `when` | string | no | The condition. Empty always holds. A leading `!` negates it. |
| `args` | array of string | yes | The arguments contributed when the condition holds. Cannot be empty. |

#### Placeholders

`{name}` spans inside a fragment's arguments are substituted from the invocation's values. An unknown placeholder is rejected at load.

| Placeholder | Value |
| --- | --- |
| `{prompt}` | The text the agent is asked to act on. |
| `{session}` | The session ID being assigned or resumed. |
| `{model}` | The model from `--agent name/model`. |
| `{level}` | The reasoning level from `--agent name:level`. |
| `{image}` | The screenshot path a vision call reads. |
| `{settings}` | Claude's Remote Control settings payload (`--remote-control`). |
| `{sessionName}` | The name a Remote Control run appears under, such as `glorp owner/repo#123`. |

#### Conditions

`when` accepts every placeholder name above — which holds when that value is non-empty — plus two that test the run's own flags:

| Condition | Holds when |
| --- | --- |
| `yolo` | The run was started with `--yolo`. |
| `remoteControl` | The run was started with `--remote-control`. |

So `{"when": "yolo", "args": ["--dangerously-skip-permissions"]}` adds the bypass flag only under `--yolo`, and `{"when": "!yolo", "args": ["--permission-mode", "auto"]}` adds the safe default the rest of the time.

### `minVersion`

An agent's argv is written against a particular release of its CLI. When an older one is installed, the flags the definition renders do not exist yet, and the run dies on an unrecognized-argument error from the child process that says nothing about the version being the cause.

`minVersion` says which release the definition was written against:

```json
{"binary": "gemini", "minVersion": "0.58.0"}
```

Before every dispatch glorp runs `binary --version` on the executable the run resolved -- so `--agent-binary` is what gets checked -- and compares what it reports:

- **Below the minimum:** the dispatch fails before the agent starts, with an error naming the agent, the version found, the version required, and both ways to fix it: upgrade the CLI, or point glorp at a newer install with `--agent-binary NAME=PATH`.
- **At or above it:** the run proceeds as usual.
- **No version glorp can read**, either because the CLI prints none or because asking it failed: a warning goes into the run's output and the dispatch proceeds anyway. Blocking there would break any CLI that prints its version some way glorp has not seen.

The version is read out of whatever banner surrounds it, compared component by component -- so `0.9.0` is older than `0.58.0`, not newer the way a string comparison has it -- and a prerelease suffix such as `-beta.3` is ignored rather than treated as unreadable.

A definition that omits `minVersion` is not checked at all and starts no extra process. Of the built-ins only `gemini` declares one, `0.58.0`, the release its `--session-id`, `--resume`, and `--output-format` arguments were measured against.

### `session`

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `session.assign` | string | yes | — | `glorp` when glorp generates the ID and passes it to the agent (Claude's `--session-id`), `capture` when the agent mints its own and prints it (Codex), or `none` when the agent has no resumable session at all. |
| `session.capture` | string (regexp) | when `assign` is `capture` | — | Reads the ID out of stdout. Its first capture group is the ID. Rejected on the other two `assign` values rather than ignored. |
| `session.clearOnResumeFailure` | bool | no | `false` | Drops the recorded ID when a resume fails because the session is gone, so the restarted run takes a fresh one. Agents glorp assigns IDs for keep theirs. |

### `output`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `output.format` | string | yes | `text` (alias `plain`) shows output as it is written; `stream-json` (alias `claude-stream-json`) decodes Claude's streaming event envelope; `jsonl` decodes a line-delimited JSON stream described by the block below. |
| `output.jsonl` | object | when `format` is `jsonl` | The generic decoder's field paths. Rejected on the other formats. |

A **path** is dot-separated object keys, each optionally suffixed with `[]` to step into an array and continue into every element: `message.content[].text` reads the text of every content block. A path naming nothing in an event contributes nothing, so an event shape the definition does not describe is passed over rather than failing the stream, and a line that is not JSON at all is passed through as written.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `output.jsonl.type` | path | when `ignore` is used | Where the event's type is, used only to skip the types in `ignore`. |
| `output.jsonl.text` | path | one of `text` or `toolName` | The human-readable text an event carries. |
| `output.jsonl.toolName` | path | one of `text` or `toolName` | A tool call's name, rendered as the `Running: ...` progress lines. |
| `output.jsonl.toolInput` | path | no | The tool call's input object. Needs `toolName`. Paths that share a prefix are paired element by element, so a name and its own input come from the same block. |
| `output.jsonl.ignore` | array of string | no | Event types dropped before anything is read out of them, for the bookkeeping events a stream repeats every turn. Needs `type`. |
| `output.jsonl.textDelta` | bool | no | The text an event carries is a fragment of a message rather than a whole one. Needs `text`. Defaults to `false`. |
| `output.jsonl.toolNamePrefix` | string | no | Only a tool name starting with this is a call, and the prefix is trimmed off what is rendered. Needs `toolName`. |
| `output.jsonl.events` | object | no | Per-event-type overrides of the paths above, keyed by the value `type` names. Needs `type`. |
| `output.jsonl.events.<type>.text` | path | one of `text` or `toolName` | The text this event type carries, replacing `output.jsonl.text` for it. |
| `output.jsonl.events.<type>.toolName` | path | one of `text` or `toolName` | This event type's tool-call name, replacing `output.jsonl.toolName` for it. |
| `output.jsonl.events.<type>.toolInput` | path | no | This event type's tool input. Needs the override's own `toolName`. |
| `output.jsonl.events.<type>.toolNamePrefix` | string | no | This event type's tool-name prefix. Needs the override's own `toolName`. |

A decoder that reads neither `text` nor `toolName` renders nothing, so at least one is required, either on the block itself or on one of its `events` overrides.

`events` is for an envelope that spreads one logical event over several typed events, where a single set of paths cannot describe all of them: a path pointed at where one type keeps its tool name reads something else, or nothing, on every other type. An override names only what differs for its type. `text` is overridden on its own; `toolName` carries `toolInput` and `toolNamePrefix` with it, since pairing one type's name with another type's input path renders another call's arguments. An event type in `ignore` is dropped before any override applies, so listing the same type in both is rejected.

`toolNamePrefix` is for a stream whose name field doubles as a kind covering more than tool calls. Muse's `task.lifecycle.proposed` carries `tool.read_file` in the same field a model turn fills with `model.meta.response`, so `"toolNamePrefix": "tool."` renders `Running: read_file` for the first and nothing for the second.

`textDelta` is for a CLI whose JSON mode streams token-sized fragments. `muse exec --json` emits its message a delta at a time -- `"a.txt "`, `"contains "`, `"hi"` -- and the default decoder writes a line per event, so the sentence is broken across three lines. With `"textDelta": true` the decoder joins the fragments instead and ends the line on the first event carrying no text: a tool call, an event of a shape the definition describes nothing in, a line that is not JSON, or the end of the stream. An event type listed in `ignore` is dropped before any of that, so a bookkeeping event repeated mid-message does not split it. A definition that leaves `textDelta` out decodes exactly as it did before the field existed, one line per event, which is right for an envelope whose text events carry whole messages.

### `missingSession`

Sessions expire, and a glorp work-state file routinely outlives the agent's local conversation history. When a resume fails because the session is gone, glorp restarts the work instead of reporting a failed job — which means it has to recognise the message. These phrases are always matched, case-insensitively, anywhere in the agent's output:

```text
no conversation found
no session found
session not found
could not find session
unable to find session
```

`"missingSession": ["thread has expired"]` **adds** a phrase for that agent alone, on top of the shared list, which is what keeps one CLI's distinctive wording out of every other agent's detection.

### `quota`

An agent that declares no quota reports none, which the dashboard shows as untracked. That is the deliberate default and it costs no process on any poll.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `quota.reader` | string | no | `none` | `none`, `codex`, `claude`, or `command`. |
| `quota.command` | array of string | when `reader` is `command` | — | The argv to run, whose stdout is parsed as JSON. `{binary}` substitutes the executable the agent was resolved to, so `--agent-binary` reaches the quota call too. No argument may be empty. |
| `quota.percentUsed` | path | when the format substitutes a percentage | — | Dotted path to the percentage of the window already consumed. |
| `quota.resetAt` | path | when the format substitutes `{resetAt}` | — | Dotted path to the time the window resets. |
| `quota.format` | string | no | `{percentLeft}% left` | The status-bar template. Substitutes `{percentUsed}`, `{percentLeft}`, and `{resetAt}`; any other placeholder is rejected. |
| `quota.timeout` | duration string | no | `30s` | Bounds one read. A quota call is a status-bar nicety, so it is never allowed to hang a poll. Must be positive. |

Every field but `reader` belongs to the `command` reader and is rejected on the others rather than ignored, because a `quota` block that silently does nothing looks exactly like a working one until the status bar stays empty. A reading that fails leaves the last good one in place rather than blanking it.

### `skills`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `skills.target` | string | no | The skills.sh target id `skills add --agent` takes: a dedicated one such as `codex`, `claude-code`, `opencode`, `cline`, or `gemini-cli`, or `universal` for a CLI skills.sh has no dedicated id for. Lowercase letters, digits, and dashes. |

The installers derive their `skills add --agent` list from the registry, so a built-in agent is covered without either installer script being edited. They cover the built-ins only: an agent you declare yourself installs its own skills with the same command, shown in the tutorial below.

The set of ids skills.sh knows grows without glorp, so the shape of the id is checked and its value is not — an id glorp has never heard of is yours to pass on.

### `doctor`

`glorp agents` reports on each registered agent: whether its CLI is installed and which version, whether it is signed in, how much quota is left, and which models it accepts, written as the fully qualified `agent/model` names `--agent` takes. Most of that it can answer from the definition it already has. The two things it cannot are whether the CLI is signed in, and what a provider-agnostic CLI's model list is today — no static allow-list keeps up with a CLI that fronts dozens of models. `doctor` names the commands that answer them.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `doctor.auth` | array of string | no | none | The argv whose exit status reports whether the CLI is signed in. `{binary}` substitutes the executable the agent was resolved to, so `--agent-binary` reaches the probe too. No argument may be empty. |
| `doctor.signedIn` | regular expression | no | none | What the auth command's output has to match for the agent to count as signed in, for the CLIs that report a signed-out account on a zero exit status. Needs `doctor.auth`. |
| `doctor.models` | array of string | no | none | The argv that lists the models the agent accepts, one per line on its stdout, or in JSON when `doctor.modelsJSON` says where. `{binary}` substitutes as above. |
| `doctor.modelPattern` | regular expression | no | none | Narrows what counts as a model in that output, for a command that decorates its list. Only a matching line is a model, and its first capture group, when it has one, is the model id. Needs `doctor.models`. |
| `doctor.modelsStdin` | array of string | no | none | What the probe writes to that command's stdin, one line each, for a CLI whose model list is only reachable over a stdio protocol — several answer a JSON-RPC handshake rather than a listing subcommand. The probe holds the pipe open and stops reading at the first reply that carries models, so a CLI that serves rather than exits is answered and then shut down. No line may be empty. Needs `doctor.models`. |
| `doctor.modelsJSON` | path | no | none | Where the model ids are in that command's output: a dotted path whose `[]` walks an array and whose `[key=value]` walks only the elements a field marks, such as `models[visibility=list].slug` or `result.models.availableModels[].modelId`. The whole output is read as one document first and then line by line, so it fits both a catalog command and an agent that prints a JSON-RPC response per line. Replaces the line reading `doctor.modelPattern` does. Needs `doctor.models`. |
| `doctor.knownModels` | array of string | no | none | Model names the definition itself carries, for a CLI with no way at all to be asked. No built-in declares any — a list written into glorp goes stale — and it is never used to validate `--agent`. Reported as `agent/model` names labelled *known to glorp; the CLI may accept others*. |
| `doctor.modelsNote` | string | no | none | Replaces the label the report puts on the models field: the caveat on a known list that belongs to one provider out of many, or, for a CLI that neither lists its models nor has a list worth freezing, what to write after `--agent` and why the report cannot enumerate it. |
| `doctor.timeout` | duration string | no | `20s` | Bounds one probe. The report is a diagnostic, so a CLI that hangs is reported as unknown rather than allowed to hold the listing up. Must be positive. |

Both probes are optional, and neither is ever run by a dispatch — `glorp agents` is the only caller. An agent that declares nothing here still appears in the report: what it could not answer is shown as `unknown`, and its models come from its `models` allow-list, then from `doctor.knownModels`, then from `doctor.modelsNote`, and otherwise from a note saying the CLI accepts any model, or that it accepts none. A probe that ran and listed nothing is reported as one that could not be listed, naming the command, rather than as an agent that accepts anything: a CLI signed out of its provider answers the handshake and offers no catalog, and the reason is the useful half of that answer. A field belonging to a probe the definition does not declare is rejected rather than ignored, for the same reason the `quota` block rejects one.

Sign-in has a fallback that costs nothing: an agent with no `doctor.auth` whose quota could be read is reported as signed in, because every quota reader asks the CLI something only a signed-in account can answer.

A probe must be non-interactive and must not change anything a run depends on. A CLI whose only sign-in check starts a device-code flow has no usable probe and is better left undeclared, so the report says the state is unknown — which is true — instead of logging somebody in for asking. That is why only `codex` and `opencode` ship an auth probe. A model probe that opens a throwaway protocol session is held to the same bar: it asks for a catalog and nothing else, and the session it opened dies with the process the report shuts down.


## The built-in definitions

These are the shipped documents, verbatim, and they are the best worked examples of the schema.

### `codex`

```json
{
  "name": "codex",
  "binary": "codex",
  "levels": ["low", "medium", "high"],
  "defaultModel": "gpt-5.6-terra",
  "session": {
    "assign": "capture",
    "capture": "(?i)session id:\\s*([0-9a-f]{8}-[0-9a-f-]{27,})",
    "clearOnResumeFailure": true
  },
  "output": {"format": "text"},
  "quota": {"reader": "codex"},
  "skills": {"target": "codex"},
  "doctor": {
    "auth": ["{binary}", "login", "status"],
    "signedIn": "(?im)^\\s*Logged in",
    "models": ["{binary}", "debug", "models"],
    "modelsJSON": "models[visibility=list].slug"
  },
  "args": {
    "run": [
      {"args": ["exec"]},
      {"when": "yolo", "args": ["--dangerously-bypass-approvals-and-sandbox"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["-c", "model_reasoning_effort={level}"]},
      {"args": ["{prompt}"]}
    ],
    "resume": [
      {"args": ["exec", "resume"]},
      {"when": "yolo", "args": ["--dangerously-bypass-approvals-and-sandbox"]},
      {"args": ["{session}", "{prompt}"]}
    ],
    "vision": [
      {"args": ["exec", "--image", "{image}"]},
      {"when": "yolo", "args": ["--dangerously-bypass-approvals-and-sandbox"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["-c", "model_reasoning_effort={level}"]},
      {"args": ["{prompt}"]}
    ]
  }
}
```

### `claude`

```json
{
  "name": "claude",
  "binary": "claude",
  "levels": ["low", "medium", "high"],
  "env": {"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS": "0"},
  "defaultModel": "sonnet",
  "session": {"assign": "glorp"},
  "output": {"format": "stream-json"},
  "quota": {"reader": "claude"},
  "skills": {"target": "claude-code"},
  "doctor": {
    "knownModels": ["opus", "sonnet", "haiku"],
    "modelsNote": "known Claude aliases; the CLI also accepts full model ids"
  },
  "args": {
    "run": [
      {"args": ["-p"]},
      {"when": "session", "args": ["--session-id", "{session}"]},
      {"when": "yolo", "args": ["--dangerously-skip-permissions"]},
      {"when": "!yolo", "args": ["--permission-mode", "auto"]},
      {"when": "remoteControl", "args": ["--settings", "{settings}", "--rc", "{sessionName}"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--effort", "{level}"]},
      {"args": ["--output-format", "stream-json", "--verbose"]},
      {"args": ["{prompt}"]}
    ],
    "resume": [
      {"args": ["-p", "--resume", "{session}"]},
      {"when": "yolo", "args": ["--dangerously-skip-permissions"]},
      {"when": "!yolo", "args": ["--permission-mode", "auto"]},
      {"when": "remoteControl", "args": ["--settings", "{settings}", "--rc", "{sessionName}"]},
      {"args": ["--output-format", "stream-json", "--verbose"]},
      {"args": ["{prompt}"]}
    ],
    "vision": [
      {"args": ["-p"]},
      {"when": "yolo", "args": ["--dangerously-skip-permissions"]},
      {"when": "!yolo", "args": ["--permission-mode", "auto"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--effort", "{level}"]},
      {"args": ["{prompt}"]}
    ]
  }
}
```

### `gemini`

```json
{
  "name": "gemini",
  "binary": "gemini",
  "minVersion": "0.58.0",
  "defaultModel": "gemini-3.5-flash",
  "levels": [],
  "env": {"GEMINI_CLI_TRUST_WORKSPACE": "true"},
  "session": {"assign": "glorp"},
  "output": {"format": "text"},
  "missingSession": ["no previous sessions found", "invalid session identifier"],
  "skills": {"target": "gemini-cli"},
  "doctor": {
    "models": ["{binary}", "--acp"],
    "modelsStdin": [
      "{\"jsonrpc\":\"2.0\",\"id\":0,\"method\":\"initialize\",\"params\":{\"protocolVersion\":1,\"clientCapabilities\":{\"fs\":{\"readTextFile\":false,\"writeTextFile\":false}}}}",
      "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/new\",\"params\":{\"cwd\":\".\",\"mcpServers\":[]}}"
    ],
    "modelsJSON": "result.models.availableModels[].modelId",
    "timeout": "45s"
  },
  "args": {
    "run": [
      {"when": "session", "args": ["--session-id", "{session}"]},
      {"when": "yolo", "args": ["--yolo"]},
      {"when": "!yolo", "args": ["--approval-mode", "auto_edit"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"args": ["--output-format", "text"]},
      {"args": ["-p", "{prompt}"]}
    ],
    "resume": [
      {"args": ["--resume", "{session}"]},
      {"when": "yolo", "args": ["--yolo"]},
      {"when": "!yolo", "args": ["--approval-mode", "auto_edit"]},
      {"args": ["--output-format", "text"]},
      {"args": ["-p", "{prompt}"]}
    ],
    "vision": [
      {"when": "yolo", "args": ["--yolo"]},
      {"when": "!yolo", "args": ["--approval-mode", "auto_edit"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"args": ["-p", "{prompt}"]}
    ]
  }
}
```

### `muse`

```json
{
  "name": "muse",
  "binary": "muse",
  "levels": ["none", "minimal", "low", "medium", "high", "xhigh", "ultra"],
  "session": {"assign": "glorp"},
  "output": {
    "format": "jsonl",
    "jsonl": {
      "type": "payload_type",
      "text": "payload.text",
      "textDelta": true,
      "ignore": ["run.terminal.completed", "run.terminal.failed", "run.terminal.cancelled", "tool.result"],
      "events": {
        "task.lifecycle.proposed": {"toolName": "payload.task.kind", "toolNamePrefix": "tool."}
      }
    }
  },
  "skills": {"target": "universal"},
  "doctor": {
    "models": ["{binary}", "serve"],
    "modelsStdin": [
      "{\"jsonrpc\":\"2.0\",\"id\":0,\"method\":\"initialize\",\"params\":{\"clientInfo\":{\"name\":\"glorp\",\"version\":\"1\"}}}",
      "{\"jsonrpc\":\"2.0\",\"method\":\"initialized\",\"params\":{}}",
      "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"model/list\",\"params\":{}}"
    ],
    "modelsJSON": "result.models[].modelId",
    "timeout": "45s"
  },
  "args": {
    "run": [
      {"args": ["exec"]},
      {"when": "session", "args": ["--session-id", "{session}"]},
      {"when": "yolo", "args": ["--yolo"]},
      {"when": "!yolo", "args": ["--approval-mode", "never"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--reasoning-effort", "{level}"]},
      {"args": ["--user-input-auto-resolve", "--json"]},
      {"args": ["{prompt}"]}
    ],
    "resume": [
      {"args": ["exec", "--session-id", "{session}"]},
      {"when": "yolo", "args": ["--yolo"]},
      {"when": "!yolo", "args": ["--approval-mode", "never"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--reasoning-effort", "{level}"]},
      {"args": ["--user-input-auto-resolve", "--json"]},
      {"args": ["{prompt}"]}
    ],
    "vision": [
      {"args": ["exec", "--image", "{image}"]},
      {"when": "yolo", "args": ["--yolo"]},
      {"when": "!yolo", "args": ["--approval-mode", "never"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--reasoning-effort", "{level}"]},
      {"args": ["{prompt}"]}
    ]
  }
}
```

### `opencode`

```json
{
  "name": "opencode",
  "binary": "opencode",
  "levels": ["low", "medium", "high"],
  "session": {"assign": "none"},
  "output": {
    "format": "jsonl",
    "jsonl": {
      "type": "type",
      "text": "part.text",
      "toolName": "part.tool",
      "toolInput": "part.state.input",
      "ignore": ["step_start", "step_finish"]
    }
  },
  "skills": {"target": "opencode"},
  "doctor": {
    "auth": ["{binary}", "auth", "list"],
    "signedIn": "[1-9][0-9]* credential",
    "models": ["{binary}", "models"],
    "modelPattern": "^[A-Za-z0-9._-]+/[A-Za-z0-9._/-]+$"
  },
  "args": {
    "run": [
      {"args": ["run", "--auto"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--variant", "{level}"]},
      {"args": ["--format", "json"]},
      {"args": ["{prompt}"]}
    ],
    "vision": [
      {"args": ["run", "--auto", "--file", "{image}"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--variant", "{level}"]},
      {"args": ["{prompt}"]}
    ]
  }
}
```

### `cline`

```json
{
  "name": "cline",
  "binary": "cline",
  "levels": ["none", "low", "medium", "high", "xhigh"],
  "session": {"assign": "none"},
  "output": {
    "format": "jsonl",
    "jsonl": {
      "type": "event.type",
      "text": "event.text",
      "toolName": "event.toolName",
      "ignore": ["content_start", "content_update", "iteration_start", "iteration_end", "usage", "done"]
    }
  },
  "skills": {"target": "cline"},
  "doctor": {
    "models": ["{binary}", "--acp"],
    "modelsStdin": [
      "{\"jsonrpc\":\"2.0\",\"id\":0,\"method\":\"initialize\",\"params\":{\"protocolVersion\":1,\"clientCapabilities\":{\"fs\":{\"readTextFile\":false,\"writeTextFile\":false}}}}",
      "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/new\",\"params\":{\"cwd\":\".\",\"mcpServers\":[]}}"
    ],
    "modelsJSON": "result.models.availableModels[].modelId",
    "timeout": "45s"
  },
  "args": {
    "run": [
      {"args": ["--auto-approve", "true"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--thinking", "{level}"]},
      {"args": ["--json"]},
      {"args": ["{prompt}"]}
    ],
    "vision": [
      {"args": ["--auto-approve", "true"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--thinking", "{level}"]},
      {"args": ["{prompt}"]}
    ]
  }
}
```

### `agy`

```json
{
  "name": "agy",
  "binary": "agy",
  "defaultModel": "claude-sonnet-4-6",
  "levels": ["low", "medium", "high"],
  "session": {"assign": "none"},
  "output": {"format": "text"},
  "skills": {"target": "antigravity-cli"},
  "doctor": {
    "models": ["{binary}", "models"],
    "modelPattern": "^([A-Za-z0-9][A-Za-z0-9._/:-]*)\\t"
  },
  "args": {
    "run": [
      {"when": "yolo", "args": ["--dangerously-skip-permissions"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--effort", "{level}"]},
      {"args": ["--output-format", "text"]},
      {"args": ["--print", "{prompt}"]}
    ],
    "vision": [
      {"when": "yolo", "args": ["--dangerously-skip-permissions"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--effort", "{level}"]},
      {"args": ["--print", "{prompt}"]}
    ]
  }
}
```

`agy` has no documented way for a headless caller to capture or set a resumable conversation id (see [google-antigravity/antigravity-cli#7](https://github.com/google-antigravity/antigravity-cli/issues/7)), so it declares `"session": {"assign": "none"}` like `opencode` and `cline`, and its `run` fragments render again on a recovery.

## Tutorial: registering a CLI glorp has never heard of

Say the CLI is called `robo`, it takes a prompt as a positional argument, and it is not one of the six above. Nothing here needs a glorp build.

### 1. Find out what a headless run looks like

Everything the definition says comes from the CLI itself, so answer these five questions first, by running it:

1. **What is the non-interactive invocation?** Most CLIs have a subcommand for it (`robo run`, `robo exec`, `robo -p`). It must not open a TUI and must not need a terminal, because glorp runs it with no one watching.
2. **How does it stop asking for approval?** A dispatched run cannot answer a permission prompt, and a CLI whose non-interactive default is to auto-*reject* will die trying to reach the isolated clone the `gh-fix` skill works in. Find the flag that auto-approves and pass it unconditionally; keep the truly dangerous bypass behind `"when": "yolo"`.
3. **Does it have a resumable session?** Three answers: it accepts an ID you give it, it prints one you can read back, or it has neither.
4. **What does it print?** Plain text, Claude's `stream-json` envelope, or its own line-delimited JSON.
5. **What id does skills.sh know it by?** `universal` if it has no dedicated one.

### 2. Write the minimal definition

`name`, `binary`, `args.run`, `session`, and `output` are all that is required. `robo` has no resumable session, so it leaves `args.resume` out and a recovery re-runs it with the recovery prompt:

```json
{
  "agents": [
    {
      "name": "robo",
      "binary": "robo",
      "session": {"assign": "none"},
      "output": {"format": "text"},
      "args": {
        "run": [
          {"args": ["run", "--auto-approve"]},
          {"args": ["{prompt}"]}
        ]
      }
    }
  ]
}
```

Save it as `.glorp.config.json` and check it loaded:

```sh
glorp agents
```

`robo` appears in the list, and `glorp watch --agent robo owner/repo` dispatches to it. A definition that does not validate stops the command instead, naming the field.

### 3. Add the model and level

`--agent robo/big-model:high` only reaches the CLI if the template renders it. Guard each on its own value so a run that names neither passes neither:

```json
{"when": "model", "args": ["--model", "{model}"]},
{"when": "level", "args": ["--effort", "{level}"]}
```

Add `"levels": ["low", "medium", "high"]` when the CLI accepts a fixed set, so a bad level is rejected by glorp with the list rather than by `robo` one dispatch later. Leave it out when the set is live.

### 4. Handle sessions

If `robo` accepts a session ID you generate, glorp assigns one and hands it over:

```json
"session": {"assign": "glorp"},
"args": {
  "run": [
    {"args": ["run"]},
    {"when": "session", "args": ["--session-id", "{session}"]},
    {"args": ["{prompt}"]}
  ],
  "resume": [
    {"args": ["run", "--resume", "{session}"]},
    {"args": ["{prompt}"]}
  ]
}
```

If it mints its own and prints it, capture it instead, and drop it when a resume fails so the restart takes a fresh one:

```json
"session": {
  "assign": "capture",
  "capture": "(?i)session:\\s*([0-9a-f-]{36})",
  "clearOnResumeFailure": true
}
```

If it has neither, keep `"assign": "none"` and let `resume` restart the work with the recovery prompt, as `opencode` and `cline` do.

### 5. Decode its output

Plain text needs nothing. If `robo` streams line-delimited JSON like this:

```json
{"kind": "chunk", "delta": {"text": "Reading the issue"}}
{"kind": "tool", "call": {"name": "shell", "arguments": {"cmd": "go test ./..."}}}
{"kind": "usage", "tokens": 812}
```

describe where its fields are rather than writing a decoder:

```json
"output": {
  "format": "jsonl",
  "jsonl": {
    "type": "kind",
    "text": "delta.text",
    "toolName": "call.name",
    "toolInput": "call.arguments",
    "ignore": ["usage"]
  }
}
```

### 6. Add quota, if it reports any

If `robo usage --json` prints `{"limits": {"used_pct": 41, "resets": "2026-09-04T00:00:00Z"}}`:

```json
"quota": {
  "reader": "command",
  "command": ["{binary}", "usage", "--json"],
  "percentUsed": "limits.used_pct",
  "resetAt": "limits.resets",
  "format": "{percentLeft}% left, resets {resetAt}",
  "timeout": "10s"
}
```

`{binary}` substitutes whatever the agent was resolved to, so `--agent-binary robo=/opt/robo/bin/robo` reaches the quota call as well as the runs. Leave the block out entirely if there is nothing to read.

### 7. Let `glorp agents` report on it

`glorp agents` already knows `robo`'s binary, version, and quota. Two commands tell it the rest. If `robo whoami` exits non-zero when signed out, and `robo models` prints one model per line:

```json
"doctor": {
  "auth": ["{binary}", "whoami"],
  "models": ["{binary}", "models"]
}
```

`robo/gpt-5.6` and the rest of that list then appear in the report as names you can paste straight into `--agent`. Add `"signedIn"` if `whoami` exits zero while reporting a signed-out account, and `"modelPattern"` if the list comes with headers or decoration. If the CLI prints JSON instead, name the ids with `"modelsJSON": "models[].id"`, and if it only answers over a stdio protocol, write the handshake it expects into `"modelsStdin"` — the probe sends those lines and reads until the reply carries models. Leave the block out if the CLI has no non-interactive way to answer: the report says `unknown`, which is better than a probe that opens a browser every time somebody runs `glorp agents`.

### 8. Point the binary somewhere else, per run

The definition names the default; `--agent-binary` overrides it without editing the file:

```sh
glorp watch --agent robo --agent-binary robo=/opt/robo/bin/robo owner/repo
```

### 9. Install the skills for it

A dispatch sends `/gh-fix ISSUE_NUMBER`, so `robo` needs the skills installed. Declare the target:

```json
"skills": {"target": "universal"}
```

and install them, since the installers cover the built-ins only:

```sh
npx --yes skills add lsegal/glorp@gh-fix --global --agent universal -y
npx --yes skills add lsegal/glorp@gh-discuss --global --agent universal -y
```

`glorp agents -skills` prints the targets in force, including this one.

### 10. Override a built-in instead

The same file overrides a shipped definition field by field. Pointing `claude` at a different install and adding an environment variable is the whole document:

```json
{
  "agents": {
    "claude": {
      "binary": "/opt/claude/bin/claude",
      "env": {"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS": "0", "MY_VAR": "1"}
    }
  }
}
```

Everything else — the argv templates, the session handling, the output decoding — stays the built-in's. Use `null` to remove something rather than replace it:

```json
{"agents": {"codex": {"quota": null}}}
```

## Troubleshooting

**A malformed definition stops the run at startup.** glorp never registers a definition it could not validate, because an agent silently missing from the registry looks exactly like a typo in `--agent`. The message names the file, the agent, and the field:

```text
agent config .glorp.config.json: agent "robo": field "session.assign": "sometimes" must be "glorp", "capture", or "none"
agent config .glorp.config.json: agent "robo": unknown field "arg"
agent config .glorp.config.json: agent "robo": field "args.run"[2].args: unknown placeholder {prmpt}; known placeholders are {image}, {level}, {model}, {prompt}, {session}, {sessionName}, {settings}
```

Run `glorp agents` after every edit; it loads the same registry `glorp watch` does, without watching anything.

**"unknown section" or "looks like a work-state record."** The config file takes one section, `agents`. If glorp reports a work-state record, you pointed `--config` at `.glorp.json` — the file glorp rewrites — instead of `.glorp.config.json`.

**Your agent is not in `glorp agents`.** Either the file is not where glorp looked (it reads `.glorp.config.json` in the working directory unless `--config` says otherwise, and a missing file is not an error) or the `agents` section is empty. Pass `--config` explicitly to be sure.

**A dispatch failed naming a version.** The agent's definition declares a `minVersion` its installed CLI is older than, so glorp stopped before running it rather than letting the CLI reject arguments it does not have. Upgrade the CLI, or point that agent at a newer install with `--agent-binary NAME=PATH`. A warning that the version could not be read is the other half of the same check: the run went ahead, and if it then fails on an unrecognized argument, the version is the first thing to check by hand.

**Telling whether an agent lacks resume support.** Read its definition's `session.assign`: `none` means there is no session, so glorp restarts the work with the recovery prompt rather than resuming it. The dashboard shows a restarted job as a new run on the same issue, and the run log says so. This is expected for `opencode` and `cline`.

**A resume restarted instead of continuing.** Two ordinary causes. The agent no longer holds the session — glorp matches the shared phrases plus the definition's own `missingSession` list, and restarts. Or the agent that ran the issue is no longer configured: work state pinned to an agent the current `--agent` set does not include is dropped, logged as `discarded persisted agent "NAME"; it is no longer configured`, and redispatched to a configured agent with a fresh session.

**Confirming the skills are installed.** `glorp agents -skills` prints the target ids the registry declares. Compare that with what skills.sh has for the agent, and install any missing pair with the two `skills add` commands above. An agent whose definition declares no `skills.target` is skipped by the installers entirely.

**`glorp agents` reports `unknown` or `not installed`.** `not installed` means the definition's `binary` is not on glorp's `PATH`; install the CLI, or point that agent at its install with `--agent-binary NAME=PATH`. `unknown` under `auth` means the definition declares no `doctor.auth` probe and no quota reading was available, so nothing could prove the CLI is signed in either way -- it is not a claim that it is signed out. Under `models` it means the CLI's own model command failed; run that argv by hand to see what it printed.

**`glorp agents` prints a report where a script wanted a list.** Use `glorp agents -names` for one agent name per line and `glorp agents -skills` for the skills.sh targets. Both print exactly what they always did and run no probe.

**The status bar shows no quota.** That is the default: an agent with no `quota` block reports untracked, and costs no process on any poll. If you declared `"reader": "command"` and still see nothing, run the `command` argv by hand and check its stdout is JSON with the paths `percentUsed` and `resetAt` name. A read that fails leaves the previous good value in place rather than blanking the bar.
