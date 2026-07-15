---
name: gh-fix
description: "Fix a numbered GitHub issue end to end from an isolated clone: understand the issue, create a dedicated branch, implement and test the fix, update the changelog, commit with a standalone `Closes #N` line, push, open a pull request referencing the issue, monitor and repair CI until it passes, and merge the successful PR. Use when the user invokes `/gh-fix ISSUENUMBER`, `$gh-fix ISSUENUMBER`, or asks for this complete GitHub issue-to-merge workflow."
---

# Fix a GitHub Issue Through Merge

Treat `/gh-fix ISSUENUMBER` as authorization to implement, publish, continuously repair, and merge the fix for that issue. Work autonomously until the PR merges or a genuine external blocker requires the user. Never alter the user's existing checkout; perform all changes in a separate clone.

## Validate the request

1. Require exactly one positive integer issue number; accept an optional leading `#`.
2. Identify the GitHub repository from the user's explicit repository or the current checkout's GitHub remote.
3. Require `git`, `gh`, and an authenticated `gh` session with repository and workflow access.
4. Read the issue title, body, labels, comments, state, and linked context. Stop if it does not exist or is not actionable. If it is already closed, explain that and do not mutate the repository unless the user explicitly requested follow-up work.
5. Read repository instructions, including applicable `AGENTS.md`, contribution guidance, branch/PR rules, CI configuration, and changelog conventions.

## Create an isolated clone and branch

1. Resolve the canonical `OWNER/REPO`, clone URL, and default branch.
2. Create a uniquely named sibling or temporary directory outside the current checkout, such as `<repo>-gh-fix-<N>`. Never reuse or modify the user's current working tree, and do not substitute a worktree for the separate clone.
3. Clone the repository normally and verify that the clone's default-branch HEAD matches the remote.
4. Create a new branch from the current remote default branch. Prefer `fix/issue-<N>-<short-slug>` unless repository instructions require another naming scheme.
5. Keep the clone available after completion for inspection. Do not delete it unless the user asks.

## Implement the fix

1. Reproduce or otherwise verify the reported behavior when practical.
2. Inspect the relevant code and history, then implement the smallest complete fix consistent with repository conventions.
3. Add or update focused tests that would fail without the fix when the repository has a relevant test framework.
4. Locate the existing changelog case-insensitively, including project-specific paths and names. Add a concise user-facing note under its current unreleased section and follow its formatting. If the project has no changelog, create `CHANGELOG.md` with `# Changelog`, an `## Unreleased` section, and the note unless repository instructions specify another location or forbid creating one.
5. Run focused tests first, then the repository's broader required checks. Resolve failures caused by the change. Do not publish a known-broken fix.
6. Review status and the complete diff. Include only files needed for the issue, its tests, and changelog note.

## Commit and push

Create a focused commit after local checks pass. Use a concise imperative subject and put the closing keyword on its own line in the body:

```text
Fix <concise issue summary>

Closes #<ISSUENUMBER>
```

For example, `git commit -m "Fix parser handling of empty input" -m "Closes #123"` creates the required separate body line. Use exactly the target issue number and capitalize `Closes` as shown.

Before pushing, verify that the commit contains the intended code, tests, and changelog note; its message contains a standalone `Closes #<ISSUENUMBER>` line; the working tree is clean; and local checks passed. Push the new branch with upstream tracking. Never force-push.

## Open the pull request

1. Open a ready-for-review PR against the current default branch; do not create a draft because CI and merge are part of this workflow.
2. Use a concise title describing the complete fix.
3. Write a real Markdown body that explains the root cause, change, user impact, changelog entry, and tests. Include `Closes #<ISSUENUMBER>` on its own line so the PR itself also references and closes the original issue when merged.
4. Record the PR number and URL, then confirm the head branch, base branch, and changed-file scope are correct.

## Drive CI to completion

Continue until every required check completes successfully:

1. Confirm the expected check suite has registered for the PR before evaluating success. A momentary empty check list immediately after PR creation is not a green build; wait for configured or required checks to appear, or verify that the repository genuinely has no applicable CI.
2. Monitor the PR checks rather than taking a single status snapshot. Prefer `gh pr checks <PR> --watch --interval 10`; use the product's recurring wait mechanism so the user receives periodic progress updates during long builds.
3. When a GitHub Actions check fails, inspect the exact run and failing job logs with `gh pr checks`, `gh run view`, and job-log APIs as necessary. Record the check name, run URL, failing command, and useful error context before changing code.
4. For external checks, follow the check URL and use the provider's available logs or tooling. If the logs are inaccessible, report the access blocker rather than guessing.
5. Classify each failure:
   - For a failure caused by the PR, reproduce it locally when practical, implement the smallest correct repair, run relevant local checks, commit the repair, and push normally.
   - For a merge conflict, update the branch from the latest default branch without force, resolve it, rerun affected checks, commit, and push.
   - For a clearly transient infrastructure or flaky-test failure, rerun the failed job once, then investigate if it repeats.
   - For a clearly unrelated persistent failure, gather evidence and attempt an in-scope repair only when doing so is safe. Otherwise stop at the genuine external blocker.
6. After every push or rerun, monitor the new head SHA's checks from pending through completion. Ignore stale results from earlier SHAs.
7. Repeat diagnosis, repair, local verification, commit, push, and monitoring for as many actionable CI failures as necessary. Do not weaken assertions, skip tests, reduce coverage, or change CI merely to obtain a green result.

## Merge and verify

1. Before merging, fetch the latest PR state and confirm all required checks are successful, the PR is mergeable, no required review or unresolved conversation blocks it, and the head SHA is the one that passed CI.
2. Merge using the repository's required or established merge method. Prefer a normal merge or rebase when allowed because it preserves the commit containing `Closes #N`. If squash merge is required, keep the PR body's standalone closing reference and set the final squash commit body to include `Closes #<ISSUENUMBER>`.
3. Delete the remote issue branch after a successful merge when repository policy permits.
4. After confirming the merge, remove the workflow labels from the originating issue with `gh issue edit <ISSUENUMBER> --repo <OWNER/REPO> --remove-label agent-ready --remove-label agent-started`. Treat a failure to remove either label as an actionable error and retry it; do not remove labels before the PR is merged.
5. Verify the PR is merged, the merged commit is reachable from the remote default branch, and GitHub closed issue `#<ISSUENUMBER>`. Allow for a brief GitHub processing delay, but do not claim closure without checking.

## Report the result

Lead with the merged outcome. Include the issue and PR URLs, branch name, final commit or merge SHA, clone path, changelog file, local tests, and completed CI checks. Confirm both PR merge and issue closure. If genuinely blocked, identify the exact failed step, relevant URL or log evidence, and the remaining requirement; preserve the isolated clone and branch for continuation.
