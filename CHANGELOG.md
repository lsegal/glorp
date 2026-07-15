# Changelog

## Unreleased

- Add a scrubbed autofilled bug-report URL when an agent exits unsuccessfully.
- Preserve agent session IDs after an issue is completed.
- Remove `agent-ready` and `agent-started` labels after the originating PR is merged.
- Ensure the `agent-ready` and `agent-started` labels exist when watching a repository.
- Add issue label filtering with a default `label=agent-ready` filter and an `--all-issues` override.
- Track active agent work with the `agent-started` issue label and persisted session state.
- Allow watching GitHub repository and project URLs as targets.
