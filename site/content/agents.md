---
title: Agent definitions
description: "Every coding agent glorp dispatches to is a JSON document: the reference for .glorp.config.json, the agent-definition schema, and how to register a CLI glorp has never heard of."
---

# Agent definitions

glorp does not know how to talk to any agent in Go. Every CLI it dispatches to — the executable, the argv for a fresh run, a resume, and a vision call, the environment its child process gets, how its session ID comes to exist, how its output is decoded, where its quota is read, and which skills.sh target its skills install for — is described by a JSON **definition**. glorp ships definitions for six agents and reads more from a config file, so supporting another CLI is a JSON document rather than a new glorp release.

This page is the reference for that file and that schema.

## The agents glorp ships

`glorp agents` prints the agents in force for the current configuration, and `glorp agents -skills` prints the skills.sh target ids they install skills for. The built-in set is:

| `--agent` | CLI | Levels accepted | Session resume | Quota | skills.sh target |
| --- | --- | --- | --- | --- | --- |
| `codex` | [Codex CLI](https://developers.openai.com/codex/cli/) | `low`, `medium`, `high` | yes — Codex prints the ID, glorp reads it back | `codex` | `codex` |
| `claude` | [Claude Code](https://docs.anthropic.com/en/docs/claude-code) | `low`, `medium`, `high` | yes — glorp assigns the ID | `claude` | `claude-code` |
| `gemini` | [Gemini CLI](https://github.com/google-gemini/gemini-cli) | any | yes — glorp assigns the ID | none | `gemini-cli` |
| `muse` | Meta Muse Code | `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `ultra` | yes — glorp assigns the ID | none | `universal` |
| `opencode` | [opencode](https://opencode.ai) | `low`, `medium`, `high` | no — recovery restarts the work | none | `opencode` |
| `cline` | [Cline](https://cline.bot) | `none`, `low`, `medium`, `high`, `xhigh` | no — recovery restarts the work | none | `cline` |

An agent with no resume support is not a degraded one. glorp's recovery prompt asks the agent to pick the work back up from the branch and the open draft pull request, and the `gh-fix` skill is re-entrant by design, so a restarted run adopts what the previous one left behind instead of starting over.

Models are not listed because most of these CLIs take a live catalog. A definition may name a `models` allow-list; the built-ins that do not pass whatever `--agent NAME/MODEL` was given straight through to the CLI.

## `.glorp.config.json`

Agent definitions live in `.glorp.config.json` in the directory glorp is started from. `--config PATH` reads a different file. A missing file is not an error — glorp runs on the built-in definitions alone.

**glorp never writes this file.** That is the whole reason it exists separately from the state file:

| File | Flag | Who writes it | What it holds |
| --- | --- | --- | --- |
| `.glorp.config.json` | `--config PATH` | you, by hand | agent definitions |
| `.glorp.json` | `--state PATH` | glorp, on every dispatch | handled issues and active sessions |

Putting a definition in `.glorp.json` is the mistake this split exists to prevent: the next state save rewrites that file and the definition is gone. glorp catches the mix-up in the other direction too — hand it a work-state file as `--config` and it reports the state record it found by name rather than a pile of unknown fields.

The file's top level is an object with one section defined so far:

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
| `args` | object | yes | — | The argv templates. See [`args`](#args). |
| `env` | object of string→string | no | none | Extra environment for the child process, layered on top of glorp's own environment. |
| `session` | object | yes | — | How the session ID is established. See [`session`](#session). |
| `levels` | array of string | no | any | Allow-list for the `:level` part of `--agent`. An empty list accepts anything; a value outside the list is rejected by glorp with the list, instead of by the CLI one dispatch later. |
| `models` | array of string | no | any | Allow-list for the `/model` part of `--agent`, with the same behaviour. |
| `output` | object | yes | — | How stdout is decoded. See [`output`](#output). |
| `missingSession` | array of string | no | none | Extra phrases that mean "the session you asked me to resume is gone". See [`missingSession`](#missingsession). |
| `quota` | object | no | `{"reader": "none"}` | Where the status bar's quota reading comes from. See [`quota`](#quota). |
| `skills` | object | no | none | The skills.sh target the agent's skills install for. See [`skills`](#skills). |

### `args`

`args` carries one argv template per shape of invocation glorp makes. `run` and `resume` are required; `vision` is optional and an agent without one is simply never asked to read a screenshot.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `args.run` | array of fragment | yes | A fresh dispatch. |
| `args.resume` | array of fragment | yes | Continuing an earlier session. An agent with no resumable session gives a template that restarts the work with the recovery prompt. |
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

A decoder that reads neither `text` nor `toolName` renders nothing, so at least one is required.

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

## The built-in definitions

These are the shipped documents, verbatim, and they are the best worked examples of the schema.

### `codex`

```json
{
  "name": "codex",
  "binary": "codex",
  "levels": ["low", "medium", "high"],
  "session": {
    "assign": "capture",
    "capture": "(?i)session id:\\s*([0-9a-f]{8}-[0-9a-f-]{27,})",
    "clearOnResumeFailure": true
  },
  "output": {"format": "text"},
  "quota": {"reader": "codex"},
  "skills": {"target": "codex"},
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
  "session": {"assign": "glorp"},
  "output": {"format": "stream-json"},
  "quota": {"reader": "claude"},
  "skills": {"target": "claude-code"},
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
  "env": {"GEMINI_CLI_TRUST_WORKSPACE": "true"},
  "session": {"assign": "glorp"},
  "output": {"format": "text"},
  "missingSession": ["no previous sessions found", "invalid session identifier"],
  "skills": {"target": "gemini-cli"},
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
  "output": {"format": "text"},
  "skills": {"target": "universal"},
  "args": {
    "run": [
      {"args": ["exec"]},
      {"when": "session", "args": ["--session-id", "{session}"]},
      {"when": "yolo", "args": ["--yolo"]},
      {"when": "!yolo", "args": ["--approval-mode", "never"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--reasoning-effort", "{level}"]},
      {"args": ["--user-input-auto-resolve"]},
      {"args": ["{prompt}"]}
    ],
    "resume": [
      {"args": ["exec", "--session-id", "{session}"]},
      {"when": "yolo", "args": ["--yolo"]},
      {"when": "!yolo", "args": ["--approval-mode", "never"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--reasoning-effort", "{level}"]},
      {"args": ["--user-input-auto-resolve"]},
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
  "output": {"format": "text"},
  "skills": {"target": "opencode"},
  "args": {
    "run": [
      {"args": ["run", "--auto"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--variant", "{level}"]},
      {"args": ["{prompt}"]}
    ],
    "resume": [
      {"args": ["run", "--auto"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--variant", "{level}"]},
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
  "output": {"format": "text"},
  "skills": {"target": "cline"},
  "args": {
    "run": [
      {"args": ["--auto-approve", "true"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--thinking", "{level}"]},
      {"args": ["{prompt}"]}
    ],
    "resume": [
      {"args": ["--auto-approve", "true"]},
      {"when": "model", "args": ["--model", "{model}"]},
      {"when": "level", "args": ["--thinking", "{level}"]},
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

`name`, `binary`, `args.run`, `args.resume`, `session`, and `output` are all that is required:

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
        ],
        "resume": [
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

### 7. Point the binary somewhere else, per run

The definition names the default; `--agent-binary` overrides it without editing the file:

```sh
glorp watch --agent robo --agent-binary robo=/opt/robo/bin/robo owner/repo
```

### 8. Install the skills for it

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

### 9. Override a built-in instead

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

**Telling whether an agent lacks resume support.** Read its definition's `session.assign`: `none` means there is no session, so glorp restarts the work with the recovery prompt rather than resuming it. The dashboard shows a restarted job as a new run on the same issue, and the run log says so. This is expected for `opencode` and `cline`.

**A resume restarted instead of continuing.** Two ordinary causes. The agent no longer holds the session — glorp matches the shared phrases plus the definition's own `missingSession` list, and restarts. Or the agent that ran the issue is no longer configured: work state pinned to an agent the current `--agent` set does not include is dropped, logged as `discarded persisted agent "NAME"; it is no longer configured`, and redispatched to a configured agent with a fresh session.

**Confirming the skills are installed.** `glorp agents -skills` prints the target ids the registry declares. Compare that with what skills.sh has for the agent, and install any missing pair with the two `skills add` commands above. An agent whose definition declares no `skills.target` is skipped by the installers entirely.

**The status bar shows no quota.** That is the default: an agent with no `quota` block reports untracked, and costs no process on any poll. If you declared `"reader": "command"` and still see nothing, run the `command` argv by hand and check its stdout is JSON with the paths `percentUsed` and `resetAt` name. A read that fails leaves the previous good value in place rather than blanking the bar.
