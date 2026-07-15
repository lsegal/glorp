# gh-watch

Poll a GitHub repository and start an agent for each newly discovered open issue.

```sh
gh-watch --interval 30s --concurrency 3 --agent codex owner/repo
```

The repository argument may also be a GitHub repository URL or a GitHub project URL, such as `https://github.com/users/owner/projects/3`.

`--concurrency 0` is normalized to 3. Use `--agent claude`, `--codex-binary`, or `--claude-binary` to select and locate the agent executable. Agents are launched with their non-interactive, no-sandbox permission-bypass options. Every open issue not already listed in `.gh-watch.json` is handled, including issues created before `gh-watch` started; issue numbers are persisted to avoid duplicate work after restarts.

Runtime progress is written to stdout with timestamps. It reports each poll's open-issue count, baseline/new-issue detection, queued and running task counts, agent completion/failure totals, poll retries, and shutdown progress.

The installer checks for `gh`, downloads the matching GitHub release, and installs the `gh-fix` skill globally through skills.sh:

```sh
curl -fsSL https://github.com/lsegal/gh-watch/releases/latest/download/install.sh | bash
```

On Windows PowerShell:

```powershell
irm https://github.com/lsegal/gh-watch/releases/latest/download/install.ps1 | iex
```

The public `.agents/skills/gh-fix` directory is the skills.sh package source.
