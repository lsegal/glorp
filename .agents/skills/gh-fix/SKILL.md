---
name: gh-fix
description: "Fix a numbered GitHub issue end to end from an isolated clone: understand the issue, create a dedicated branch and linked draft pull request, push progress checkpoints, implement and test the fix, update the changelog, mark the pull request ready, monitor and repair CI until it passes, and merge the successful PR. Use when the user invokes `/gh-fix OWNER/REPO#ISSUENUMBER`, `/gh-fix ISSUENUMBER`, `$gh-fix OWNER/REPO#ISSUENUMBER`, or asks for this complete GitHub issue-to-merge workflow."
---

# Fix a GitHub Issue Through Merge

Treat `/gh-fix OWNER/REPO#ISSUENUMBER` as authorization to implement, publish, continuously repair, and merge the fix for that issue. The invocation may append `identity:/glorp:<ID>` to identify the Glorp instance that dispatched it. Work autonomously until the PR merges or a genuine external blocker requires the user. Never alter the user's existing checkout; perform all changes in a separate clone.

## Validate the request

1. Require exactly one positive integer issue number; accept an optional leading `#`. Also accept one optional trailing `identity:/glorp:<ID>` argument and retain that identity for comment handling.
2. Identify the GitHub repository. Prefer `OWNER/REPO` given directly in the command itself (`/gh-fix OWNER/REPO#N`, as glorp always dispatches it) — this is the authoritative form and needs no further detection. If the command was invoked as bare `/gh-fix N` instead, look for a `Repository: OWNER/REPO` line elsewhere in the invocation, or the user otherwise stating `OWNER/REPO` explicitly, and use that. Never run `git remote -v`, `gh repo view`, or any other repository-detection command when a repository was given by any of the above. Only when no repository was given anywhere in the invocation, fall back to the current checkout's GitHub remote to infer `OWNER/REPO`, and only ask the user if that also fails to identify a repository.
3. Require `git`, `gh`, and an authenticated `gh` session with repository and workflow access. If the issue exists but this or any other environment requirement cannot be satisfied, stop without opening a pull request or otherwise mutating the repository — but post a single comment on the issue explaining why no fix was implemented, following the rule below.
4. Read the issue title, body, labels, comments, state, and linked context. When an identity was supplied, also read all issue comments and linked pull-request conversation in chronological order as a threaded conversation: the issue body establishes the initial request and later comments can amend, clarify, or override that context. Comments containing the exact `@/glorp:<ID>` mention for the supplied identity are direct instructions to this agent and must not be ignored; mentions naming a different identity are not instructions to this run. If the issue does not exist, stop; there is nothing to comment on. If it is not actionable, or is already closed, stop without opening a pull request or otherwise mutating the repository — but post a single comment on the issue explaining why no fix was implemented, following the rule below.
5. Read repository instructions, including applicable `AGENTS.md`, contribution guidance, branch/PR rules, CI configuration, and changelog conventions.

**Always comment and track follow-up work before giving up.** Whenever this skill stops for any reason before a pull request has been created for the issue — an invalid or non-actionable issue, a missing environment requirement, an unresolvable ambiguity, a repeated tooling failure, or any other blocker encountered anywhere earlier in this workflow — post exactly one comment on the issue explaining what was attempted and why no fix was implemented before ending the run, unless the user explicitly requested follow-up work instead. Do this even if a draft pull request was never opened. Once a pull request exists, later blockers are reported on the pull request per "Drive CI to completion" instead of as a new issue comment. Before stopping in either case, create or link any actionable follow-up issues and route them as required by "Create follow-up issues." This keeps both the reason and the next concrete work visible on GitHub instead of only in the final report to the caller.

## Resume existing work (re-entrant mode)

`/gh-fix N` must be safe to run again after being interrupted (Ctrl+C, a crashed session, a new session started later) or handed off by the caller from a prior run. Before creating anything new:

1. Determine whether `N` names an issue or an already-open pull request. If `N` is a pull request, treat the issue it closes (from its body's `Closes #<ISSUENUMBER>` line) as the originating issue and drive the PR itself to completion rather than opening a new one.
2. If `N` is an issue, search for an open, non-merged pull request that closes it (by branch naming convention `fix/issue-N-*` and by `Closes #N` in open PR bodies). If one exists, resume that PR and branch instead of creating a new draft.
3. Ownership of an issue or PR (deciding whether it is safe to pick up versus still claimed by another agent) is resolved by the caller before this skill starts, not by this skill. Do not read or post PR or issue comments to negotiate ownership.
4. When resuming, clone the existing branch (not the default branch) so its commit history, checkpoints, and any partial implementation are preserved. Read the PR body to recover what has already been done, tested, and what remains.
5. Once cloned, continue the rest of this workflow exactly as if the branch were newly created: keep checkpointing, keep the PR draft until it is genuinely ready, and drive CI to completion. Do not restart implementation from scratch when prior work is still valid.

## Create an isolated clone and branch

1. Resolve the canonical `OWNER/REPO`, clone URL, and default branch.
2. Before changing directories, record whether the invocation directory contains `.glorp.json`. Preserve this fact for follow-up routing because the state file is normally ignored and will not appear in the isolated clone.
3. Create a uniquely named sibling or temporary directory outside the current checkout, such as `<repo>-gh-fix-<N>`. Never reuse or modify the user's current working tree, and do not substitute a worktree for the separate clone.
4. Clone the repository normally. If resuming per the section above, clone and check out the existing branch and verify its HEAD matches the remote; otherwise verify the clone's default-branch HEAD matches the remote.
5. Immediately after verifying the clone, emit `GLORP_CHECKOUT_DIRECTORY=<absolute clone path>` as an exact, plain-text progress line without Markdown formatting. This lets callers display and persist the real isolated checkout. Emit the line again if a missing checkout is regenerated while resuming.
6. When not resuming an existing branch, create a new branch from the current remote default branch. Prefer `fix/issue-<N>-<short-slug>` unless repository instructions require another naming scheme.
7. Register cleanup of every clone directory created by this workflow immediately after it is created. Remove those directories before exiting, including on normal completion, errors, or panics. Do not remove the user's existing checkout or unrelated directories.

The cleanup must be unconditional: use a deferred/finally-style cleanup guard as soon as each clone is created, and make cleanup errors visible while preserving the original failure when one exists.

## Open the draft pull request

Immediately after creating the branch, publish it and open a draft pull request so progress is visible throughout development. Skip this section entirely when resuming an existing draft PR per "Resume existing work" above — it already has an open PR.

1. Create an empty initial commit such as `Start work on issue #<ISSUENUMBER> [skip ci]`, then push the new branch with upstream tracking. The `[skip ci]` marker keeps CI from running on a tree identical to the default branch; never add it to any later commit. Never force-push.
2. Open a draft PR against the current default branch with a concise title describing the intended fix.
3. Write a real Markdown body that summarizes the issue and planned work. Include `Closes #<ISSUENUMBER>` on its own line so the draft links to and will close the original issue when merged.
4. End the body with a `**Agents:**` footer line naming the current agent CLI and model handling the issue (for example `**Agents:** claude-code (claude-sonnet-5)`), identified from your own runtime context. This is the contributing-agents footer described below.
5. Record the PR number and URL, then confirm the head and base branches are correct.

### Maintaining the contributing-agents footer

Every time you write or replace the PR body (draft creation, checkpoint updates, and marking ready), preserve the `**Agents:**` footer instead of overwriting it:

1. Read the current PR body first and locate its `**Agents:**` line, if any.
2. Determine your own current agent CLI and model from your runtime context.
3. If the footer is missing, add it with just your own entry. If it already lists one or more agent/model entries, keep every distinct existing entry and append your own only if it is not already present, comma-separated in the order encountered (for example `**Agents:** codex (gpt-5), claude-code (claude-sonnet-5)`).
4. This makes the footer accumulate every agent and model that has touched the issue, which matters when a different agent or model resumes a draft mid-flight.

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
5. Upload each screenshot or screen recording, then embed the returned URL directly in the pull request body as Markdown (for example, `![Dashboard after refresh](https://github.com/user-attachments/assets/...)`). Do not add UI screenshots or screen recordings to repository assets or commit them to the branch.

GitHub has no documented public API for attaching files to issues, pull requests, or comments, but the same endpoint the web UI uses for drag-and-drop accepts a bearer token non-interactively. Upload with it directly:

```sh
REPO_ID=$(gh api repos/<OWNER>/<REPO> --jq .id)
curl -sf -X POST "https://uploads.github.com/user-attachments/assets?name=<FILENAME>&content_type=<MIME_TYPE>&repository_id=$REPO_ID" \
  -H "Authorization: Bearer $(gh auth token)" \
  -H "Accept: application/json" \
  --data-binary @<LOCAL_FILE_PATH>
```

The response is JSON with a `url` field (`https://github.com/user-attachments/assets/<uuid>`); embed that URL in the pull request body. This endpoint is undocumented and unofficial, so treat a failed or unexpected response as one of the capture errors below rather than retrying indefinitely.

Skip this section only when the completed diff does not affect UI code in any way.
Skip if you run into 2+ errors trying to capture or upload results and mention this in the PR.

## Commit and push

After local checks pass, create a final implementation commit with a concise imperative subject and put the closing keyword on its own line in the body. If the latest checkpoint already contains every final change, use an empty commit so the checked implementation still has this unambiguous closing commit:

```text
Fix <concise issue summary>

Closes #<ISSUENUMBER>
```

For example, `git commit -m "Fix parser handling of empty input" -m "Closes #123"` creates the required separate body line. Use exactly the target issue number and capitalize `Closes` as shown.

Before the final push, verify that the branch contains the intended code, tests, and changelog note; the final implementation commit contains a standalone `Closes #<ISSUENUMBER>` line; the working tree is clean; and local checks passed. Push normally. Never force-push.

## Mark the pull request ready

1. Update the draft PR's title and body to describe the completed fix, including the root cause, change, user impact, changelog entry, tests, and any required UI screenshots or screen recordings. Preserve `Closes #<ISSUENUMBER>` on its own line and update the `**Agents:**` footer as described above rather than dropping or overwriting it.
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

Review for unresolved follow-up work at both terminal boundaries: immediately before stopping a failed or stalled run, whether or not a pull request exists, and after a pull request is merged. Read the originating issue, any pull request body and comments, and the current conversation for every explicit blocker, TODO, or known issue. Do not wait for a merge when the run is about to stop.

1. Turn each distinct, actionable item into its own issue in the same repository. Do not create issues for completed work, vague observations, or items that already have an equivalent open issue; link the existing issue instead.
2. Give each new issue a focused title and enough context and acceptance criteria to be actionable without rereading the pull request.
3. Associate every new issue with the originating issue and, when one exists, the pull request where the blocker or remaining work was found. Prefer repository or project relationship metadata when it is supported. Otherwise include `Addresses #<ISSUENUMBER>` in the issue body, plus `and #<PRNUMBER>` when a pull request exists.
4. Inspect the originating issue's project items. When it belongs to one or more GitHub Projects, add every new follow-up issue to the same projects and set each new item's Status to that project's `Todo` option. Match `Todo` case-insensitively, but do not silently choose a different status. Apply this routing before stopping a failed or stalled run as well as after a merge.
5. When the originating issue has no project items and `.glorp.json` was present in the invocation directory, add the `agent-ready` label to every new follow-up issue so Glorp can dispatch it. Use the presence recorded before cloning; do not assume the ignored state file will exist in the isolated checkout. Do not add `agent-ready` to project-backed follow-ups.
6. Apply the same project status or `agent-ready` routing to an equivalent existing open issue that is reused, unless it is already routed correctly or its current project status shows that work has begun or finished.
7. Record the URLs of all new or reused follow-up issues for the final report. If no qualifying items exist, explicitly record that no follow-up issues were needed.
8. Treat failures to create an issue, establish its origin links, add the required label, copy project membership, or set the `Todo` status as actionable errors and retry them. Do not end a failed or stalled run, and do not report a merged workflow complete, while required follow-up issue work is unfinished.

## Report the result

Lead with the merged outcome. Include the issue and PR URLs, branch name, final commit or merge SHA, clone path, changelog file, local tests, completed CI checks, follow-up issue URLs or confirmation that none were needed, and UI screenshots or screen recordings, or confirmation that they were not applicable. Confirm both PR merge and issue closure. If genuinely blocked, identify the exact failed step, relevant URL or log details, the remaining requirement, and every follow-up issue created or reused for it; preserve the isolated clone and branch for continuation. If the workflow stopped during validation without opening a pull request, report that no PR was created, quote the reason, include every follow-up issue created or reused, and include the URL of the explanatory comment posted on the issue (or confirm none was needed if the issue does not exist).

## Clean up the clone

Remove the isolated clone directory and any temporary files. If the workflow was blocked or failed, leave the clone intact for further investigation, but report its location to the user.
