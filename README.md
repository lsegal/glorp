# gh-watch

Watch a GitHub repository and start an agent for each newly discovered open issue.

By default, `gh-watch` starts an ngrok tunnel, configures its public URL as a
GitHub webhook for every watched repository, and listens for `issues`, `push`,
and `ping` deliveries on `:8080/webhook`. Old ngrok webhook URLs are removed
when the tunnel host changes. Use `--ngrok-binary` and `--ngrok-api` to locate
ngrok, or use `--webhook-secret` to verify GitHub's HMAC signature. Use
`--listen` and `--webhook-path` when the local endpoint needs a different
address.

```sh
gh-watch --interval 30s --concurrency 3 --agent codex owner/repo https://github.com/users/owner/projects/3
```

One or more repository or project targets may be provided. Each target may be an `OWNER/REPO`, a GitHub repository URL, or a GitHub project URL, such as `https://github.com/users/owner/projects/3`. Targets are polled together, while the concurrency limit applies across all targets.

For repositories where webhooks are not available, enable the previous polling
mode explicitly:

```sh
gh-watch --poll --interval 30s --concurrency 3 --agent codex owner/repo
```

Each target may also be a GitHub repository URL or a GitHub project URL.

Watching a project requires a GitHub CLI token with the `read:project` scope. Because `gh-watch` updates each issue's project status while an agent runs, it also requires the `project` scope. If project polling reports a missing scope, run `gh auth refresh -s read:project`; if a project status update reports a missing scope, run `gh auth refresh -s project`, then restart `gh-watch`.

`--concurrency 0` is normalized to 3. Use `--agent claude`, `--model`, and `--model-level low|medium|high` to configure the agent, or use `--codex-binary` and `--claude-binary` to locate its executable. Agents are launched with their non-interactive, no-sandbox permission-bypass options. Every open issue not already listed in `.gh-watch.json` is handled, including issues created before `gh-watch` started; issue numbers are persisted to avoid duplicate work after restarts.

Runtime progress is written to stdout with timestamps. It reports each poll's open-issue count, baseline/new-issue detection, queued and running task counts, agent completion/failure totals, poll retries, and shutdown progress.

While an agent is running, gh-watch applies the `agent-started` label to its issue and removes it when the agent exits. Persisted session state lets a restarted watcher reclaim issues whose label was left behind by an interrupted process.

The installer checks for `gh`, downloads the matching GitHub release, and installs the `gh-fix` skill globally through skills.sh:

```sh
curl -fsSL https://github.com/lsegal/gh-watch/releases/latest/download/install.sh | bash
```

On Windows PowerShell:

```powershell
irm https://github.com/lsegal/gh-watch/releases/latest/download/install.ps1 | iex
```

The public `.agents/skills/gh-fix` directory is the skills.sh package source.
