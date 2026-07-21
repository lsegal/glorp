---
name: gh-fix
description: "Fix a numbered GitHub issue end to end from an isolated clone: understand the issue, create a dedicated branch and linked draft pull request, push progress checkpoints, implement and test the fix, update the changelog, mark the pull request ready, monitor and repair CI until it passes, and merge the successful PR. Use when the user invokes `/gh-fix ISSUENUMBER`, `$gh-fix ISSUENUMBER`, or asks for this complete GitHub issue-to-merge workflow."
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
4. Immediately after verifying the clone, emit `GLORP_CHECKOUT_DIRECTORY=<absolute clone path>` as an exact, plain-text progress line without Markdown formatting. This lets callers display and persist the real isolated checkout. Emit the line again if a missing checkout is regenerated while resuming.
5. Create a new branch from the current remote default branch. Prefer `fix/issue-<N>-<short-slug>` unless repository instructions require another naming scheme.
6. Register cleanup of every clone directory created by this workflow immediately after it is created. Remove those directories before exiting, including on normal completion, errors, or panics. Do not remove the user's existing checkout or unrelated directories.

The cleanup must be unconditional: use a deferred/finally-style cleanup guard as soon as each clone is created, and make cleanup errors visible while preserving the original failure when one exists.

## Open the draft pull request

Immediately after creating the branch, publish it and open a draft pull request so progress is visible throughout development:

1. Create an empty initial commit such as `Start work on issue #<ISSUENUMBER>`, then push the new branch with upstream tracking. Never force-push.
2. Open a draft PR against the current default branch with a concise title describing the intended fix.
3. Write a real Markdown body that summarizes the issue and planned work. Include `Closes #<ISSUENUMBER>` on its own line so the draft links to and will close the original issue when merged.
4. Record the PR number and URL, then confirm the head and base branches are correct.

## Implement the fix

1. Reproduce or otherwise verify the reported behavior when practical.
2. Inspect the relevant code and history, then implement the smallest complete fix consistent with repository conventions.
3. Add or update focused tests that would fail without the fix when the repository has a relevant test framework.
4. Locate the existing changelog case-insensitively, including project-specific paths and names. Add a concise user-facing note under its current unreleased section and follow its formatting. If the project has no changelog, create `CHANGELOG.md` with `# Changelog`, an `## Unreleased` section, and the note unless repository instructions specify another location or forbid creating one. Only add changelog entries for user-visible changes; do not add internal-only notes. Do not add entry for a fix of another unreleased changelog entry.
5. During active development, create and push a checkpoint commit at least once every five minutes when the working tree has changes. Use a message such as `Checkpoint issue #<ISSUENUMBER> progress`; do not wait for implementation or tests to finish before publishing the next checkpoint to the draft PR. Never include secrets, generated build artifacts, or unrelated changes. If there are no changes at the checkpoint, skip the empty commit and check again after the next development interval.
6. Run focused tests first, then the repository's broader required checks. Resolve failures caused by the change. Do not mark a known-broken fix ready for review.
7. Review status and the complete diff. Include only files needed for the issue, its tests, and changelog note.

## Capture UI screenshots and screen recordings

After implementation is complete, and only then, determine whether any changed file affects a user interface. If so:

1. Capture screenshots that show the completed UI change in representative final states. When the change includes animation, interaction, or a state transition, capture a screen recording that demonstrates the behavior instead of or in addition to screenshots. For animations, plan a set of actions that "use" the full features in their entirety, including any secondary use cases like error handling.
2. For browser-based interfaces, run the UI and use available browser tooling, such as CDP or browser automation, to capture screenshots or a screen recording.
3. For terminal based interfaces, copy output as text if there is no visual, animation, or state change.
4. For all other non-browser interfaces, use an available local application or platform capture tool. If no suitable capture tool is installed, install Loom and use it to create a screen recording.
5. Upload each screenshot or screen recording to the pull request, then embed it directly in the pull request body as Markdown (for example, `![Dashboard after refresh](https://github.com/user-attachments/assets/...)`). Do not add UI screenshots or screen recordings to repository assets or commit them to the branch.

Skip this section only when the completed diff does not affect UI code in any way.
Skip if you run into 2+ errors trying to capture results and mention this in the PR.

## Commit and push

After local checks pass, create a final implementation commit with a concise imperative subject and put the closing keyword on its own line in the body. If the latest checkpoint already contains every final change, use an empty commit so the checked implementation still has this unambiguous closing commit:

```text
Fix <concise issue summary>

Closes #<ISSUENUMBER>
```

For example, `git commit -m "Fix parser handling of empty input" -m "Closes #123"` creates the required separate body line. Use exactly the target issue number and capitalize `Closes` as shown.

Before the final push, verify that the branch contains the intended code, tests, and changelog note; the final implementation commit contains a standalone `Closes #<ISSUENUMBER>` line; the working tree is clean; and local checks passed. Push normally. Never force-push.

## Mark the pull request ready

1. Update the draft PR's title and body to describe the completed fix, including the root cause, change, user impact, changelog entry, tests, and any required UI screenshots or screen recordings. Preserve `Closes #<ISSUENUMBER>` on its own line.
2. Confirm the head branch, base branch, and changed-file scope are correct.
3. Mark the draft PR ready for review only after implementation, local checks, the final push, and any required UI screenshots or screen recordings are complete.

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
   - For a clearly unrelated persistent failure, gather diagnostic details and attempt an in-scope repair only when doing so is safe. Otherwise stop at the genuine external blocker.
6. After every push or rerun, monitor the new head SHA's checks from pending through completion. Ignore stale results from earlier SHAs.
7. Repeat diagnosis, repair, local verification, commit, push, and monitoring for as many actionable CI failures as necessary. Do not weaken assertions, skip tests, reduce coverage, or change CI merely to obtain a green result.

## Merge and verify

1. Before merging, fetch the latest PR state and confirm all required checks are successful, the PR is mergeable, no required review or unresolved conversation blocks it, required UI screenshots or screen recordings are present, and the head SHA is the one that passed CI.
2. Merge using the repository's required or established merge method. Prefer a normal merge or rebase when allowed because it preserves the commit containing `Closes #N`. If squash merge is required, keep the PR body's standalone closing reference and set the final squash commit body to include `Closes #<ISSUENUMBER>`.
3. Delete the remote issue branch after a successful merge when repository policy permits.
4. After confirming the merge, remove the workflow labels from the originating issue with `gh issue edit <ISSUENUMBER> --repo <OWNER/REPO> --remove-label agent-ready --remove-label agent-started`. Treat a failure to remove either label as an actionable error and retry it; do not remove labels before the PR is merged.
5. Verify the PR is merged, the merged commit is reachable from the remote default branch, and GitHub closed issue `#<ISSUENUMBER>`. Allow for a brief GitHub processing delay, but do not claim closure without checking.

## Create follow-up issues

Only after the pull request is merged, review its body, review comments, and conversation for every explicit TODO or known issue that remains unresolved by the merged change:

1. Turn each distinct, actionable item into its own issue in the same repository. Do not create issues for completed work, vague observations, or items that already have an equivalent open issue; link the existing issue instead.
2. Give each new issue a focused title and enough context and acceptance criteria to be actionable without rereading the pull request.
3. Associate every new issue with both the originating issue and merged pull request. Prefer repository or project relationship metadata when it is supported; otherwise include `Addresses #<ISSUENUMBER> and #<PRNUMBER>` in the issue body.
4. Inspect the originating issue's project items. When it belongs to one or more GitHub Projects, add every new follow-up issue to the same project or projects. If it has no project, leave the new issues in the repository without adding them to a board.
5. Record the URLs of all new or reused follow-up issues for the final report. If no qualifying items exist, explicitly record that no follow-up issues were needed.
6. Treat failures to create an issue, establish its origin links, or copy its project membership as actionable errors and retry them. Do not report the workflow complete while required follow-up issue work is unfinished.

## Report the result

Lead with the merged outcome. Include the issue and PR URLs, branch name, final commit or merge SHA, clone path, changelog file, local tests, completed CI checks, follow-up issue URLs or confirmation that none were needed, and UI screenshots or screen recordings, or confirmation that they were not applicable. Confirm both PR merge and issue closure. If genuinely blocked, identify the exact failed step, relevant URL or log details, and the remaining requirement; preserve the isolated clone and branch for continuation.

## Clean up the clone

Remove the isolated clone directory and any temporary files. If the workflow was blocked or failed, leave the clone intact for further investigation, but report its location to the user.
