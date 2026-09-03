---
name: gh-fix
description: "Fix a numbered GitHub issue end to end from an isolated clone: understand the issue, create a dedicated branch and linked draft pull request, push progress checkpoints, implement and test the fix, update the changelog, mark the pull request ready, monitor and repair CI until it passes, and merge the successful PR. Use when the user invokes `/gh-fix OWNER/REPO#ISSUENUMBER`, `/gh-fix ISSUENUMBER`, `$gh-fix OWNER/REPO#ISSUENUMBER`, or asks for this complete GitHub issue-to-merge workflow."
---

# Fix a GitHub Issue Through Merge

Treat `/gh-fix OWNER/REPO#ISSUENUMBER` as authorization to implement, publish, continuously repair, and merge the fix for that issue. The invocation may append `identity:/glorp:<ID>` to identify the Glorp instance that dispatched it. Work autonomously until the PR merges, the issue withholds merge authorization, or a genuine external blocker requires the user. Never alter the user's existing checkout; perform all changes in a separate clone.

## Validate the request

1. Require exactly one positive integer issue number; accept an optional leading `#`. Also accept one optional trailing `identity:/glorp:<ID>` argument and retain that identity for comment handling.
2. Identify the GitHub repository. Prefer `OWNER/REPO` given directly in the command itself (`/gh-fix OWNER/REPO#N`, as glorp always dispatches it) — this is the authoritative form and needs no further detection. If the command was invoked as bare `/gh-fix N` instead, look for a `Repository: OWNER/REPO` line elsewhere in the invocation, or the user otherwise stating `OWNER/REPO` explicitly, and use that. Never run `git remote -v`, `gh repo view`, or any other repository-detection command when a repository was given by any of the above. Only when no repository was given anywhere in the invocation, fall back to the current checkout's GitHub remote to infer `OWNER/REPO`, and only ask the user if that also fails to identify a repository.
3. Require `git`, `gh`, and an authenticated `gh` session with repository and workflow access. If the issue exists but this or any other environment requirement cannot be satisfied, stop without opening a pull request or otherwise mutating the repository — but post a single comment on the issue explaining why no fix was implemented, following the rule below.
4. Read the issue title, body, labels, comments, state, and linked context. When an identity was supplied, also read all issue comments and linked pull-request conversation in chronological order as a threaded conversation: the issue body establishes the initial request and later comments can amend, clarify, or override that context. Comments containing the exact `@/glorp:<ID>` mention for the supplied identity are direct instructions to this agent and must not be ignored; mentions naming a different identity are not instructions to this run. If the issue does not exist, stop; there is nothing to comment on. If it is not actionable, or is already closed, stop without opening a pull request or otherwise mutating the repository — but post a single comment on the issue explaining why no fix was implemented, following the rule below.
5. Determine whether the issue withholds merge authorization, and record that decision before any code is written. Treat either of these as a do-not-merge directive: a label that reads as a merge hold (`donotmerge`, `do-not-merge`, `do not merge`, `no-merge`, `hold`, `needs-review`), matched case-insensitively and ignoring spaces, hyphens, and underscores; or wording anywhere in the issue body or its comments — including a `@/glorp:<ID>` instruction addressed to this run — asking that the fix not be merged, be left open, or stop before merging. Read the thread chronologically: a later comment can impose or lift the hold, so the most recent statement wins. When the directive is present, note the exact label or quote the wording it came from and carry it through the whole run: everything up to and including marking the pull request ready still happens, and "Hold the pull request when merging is withheld" replaces the merge.
6. Read repository instructions, including applicable `AGENTS.md`, contribution guidance, branch/PR rules, CI configuration, and changelog conventions.

**Always comment and track follow-up work before giving up.** Whenever this skill stops for any reason before a pull request has been created for the issue — an invalid or non-actionable issue, a missing environment requirement, an unresolvable ambiguity, a repeated tooling failure, or any other blocker encountered anywhere earlier in this workflow — post exactly one comment on the issue explaining what was attempted and why no fix was implemented before ending the run, unless the user explicitly requested follow-up work instead. A tracking issue that ends the run under "Split a multi-part issue into sub-issues" is not a blocker and is exempt from this rule: it comments once when it is first split and stays silent on every later run that finds nothing new to say. Do this even if a draft pull request was never opened. Once a pull request exists, later blockers are reported on the pull request per "Drive CI to completion" instead of as a new issue comment. Before stopping in either case, create or link any actionable follow-up issues and route them as required by "Create follow-up issues." This keeps both the reason and the next concrete work visible on GitHub instead of only in the final report to the caller.

## Resume existing work (re-entrant mode)

`/gh-fix N` must be safe to run again after being interrupted (Ctrl+C, a crashed session, a new session started later) or handed off by the caller from a prior run. Before creating anything new:

1. Determine whether `N` names an issue or an already-open pull request. If `N` is a pull request, treat the issue it closes (from its body's `Closes #<ISSUENUMBER>` line) as the originating issue and drive the PR itself to completion rather than opening a new one.
2. If `N` is an issue, search for a pull request that closes it, using branch naming convention `fix/issue-N-*` and `Closes #N` in the PR body as matching signals. Never reuse a CLOSED pull request, whether or not it was merged — only an OPEN pull request is eligible to resume, regardless of which signal matched it. If a matching open pull request exists, resume that PR and branch instead of creating a new draft; otherwise proceed as if none was found, even if a closed one matched.
3. Ownership of an issue or PR (deciding whether it is safe to pick up versus still claimed by another agent) is resolved by the caller before this skill starts, not by this skill. Do not read or post PR or issue comments to negotiate ownership.
4. When resuming, clone the existing branch (not the default branch) so its commit history, checkpoints, and any partial implementation are preserved. Read the PR body to recover what has already been done, tested, and what remains.
5. Once cloned, continue the rest of this workflow exactly as if the branch were newly created: keep checkpointing, keep the PR draft until it is genuinely ready, and drive CI to completion. Do not restart implementation from scratch when prior work is still valid.

## Split a multi-part issue into sub-issues

An issue that clearly describes several separable pieces of work is a tracking issue, not a unit of work. Decide this once, before cloning anything, and never split the same issue twice.

1. **Read the issue's existing sub-issues first.** When the issue already has one or more sub-issues, it has already been split. Never create further sub-issues for it and never open a pull request for it: the only thing `/gh-fix` does with such an issue is determine whether it is closeable. Read the state of every sub-issue. When all of them are closed and nothing in the parent's own body remains unimplemented, close the parent with a comment naming the completed sub-issues. When any sub-issue is still open, end the run reporting which ones remain, and post no comment — a run that finds nothing new to say must leave the issue untouched so repeated dispatches do not accumulate identical comments. Either outcome ends the run here, with no clone, branch, or pull request.
2. **Never split an issue that already has work in flight.** If "Resume existing work" matched an open pull request for the issue, resume that pull request and skip this section entirely.
3. **Split only when the issue itself plainly states multiple distinct, separable tasks.** Be very conservative. Separability is often not determinable at the start of an issue, and a wrong split costs more than no split. Split only when each part names its own deliverable and could be implemented, tested, and merged on its own without the others: "implement the fix and update the docs website" is two tasks. Do not split when the parts are one change described twice, when one part cannot be verified or even reached without the other, or when the division is your own inference rather than the issue's own words: "implement the feature and fix the related lookup bug" is usually a single task, because the bug is only reachable through the feature. When in doubt, do not split — implement the issue as one fix and continue with the rest of this workflow unchanged.
4. When the split is warranted, create each part as its own issue in the same repository and attach it to the originating issue with GitHub's sub-issue API, using the parent's and the new issue's GitHub issue IDs. Do not settle for a Markdown checklist or a plain mention. Give each sub-issue a focused title and enough context and acceptance criteria to be actionable without rereading the parent.
5. Route every new sub-issue exactly as "Create follow-up issues" routes a follow-up: choose its labels from the sub-issue itself, carry over only still-accurate labels, inherit the parent's milestone and assignees, copy the parent's project membership with a `Todo` status, and otherwise apply the `.glorp.json` assignment. This is what lets the next agent slot pick the sub-issue up. Treat a failure to create, attach, or route a sub-issue as an actionable error and retry it.
6. Once its sub-issues exist, the parent is a tracking or meta issue. It does not require a pull request, code, or a changelog entry to close, and it must not consume an agent slot. Post exactly one comment listing the sub-issues created and stating that the parent closes when they are all complete, then end the run without cloning, branching, or opening a pull request, so the next agent slot is free to pick up a sub-issue.

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
5. When capturing a native window (any capture outside a browser, such as a desktop app or platform capture tool), crop the screenshot or recording to the bounds of the relevant window. Never keep a full-screen or full-display-resolution capture unless the app itself is genuinely full screen at capture time.
6. Upload each screenshot or screen recording, then embed the returned URL directly in the pull request body as Markdown (for example, `![Dashboard after refresh](https://github.com/user-attachments/assets/...)`). Do not add UI screenshots or screen recordings to repository assets or commit them to the branch.

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

## Hold the pull request when merging is withheld

When validation recorded a do-not-merge directive, the run ends with a ready, unmerged pull request. Nothing before this point changes: the fix, its tests, the changelog note, the final push, marking the pull request ready for review, and driving CI to completion all still happen. Only the merge and the follow-up issues do not.

1. Do not merge the pull request, do not delete the branch, and do not close the originating issue. Never merge past a hold because CI came back green or because the fix looks complete.
2. Log the decision where the run's own output shows it: name the label or quote the wording that withheld the merge, and state that the pull request was deliberately left open for a human to merge.
3. Post exactly one comment on the pull request reporting completion status. Say the ticket is **Ready**, summarize the fix, the tests, and the CI result, and say the merge was withheld, naming the label or quoting the instruction that withheld it.
4. Do not create, reuse, or route follow-up issues on a held run — "Create follow-up issues" does not apply, because unmerged work may still change under review. Instead, list every item that would have become a follow-up issue in that same pull-request comment under a `Follow-ups (not filed)` heading, linking any equivalent existing open issue rather than opening anything new, so whoever merges can file them.
5. End the run once that comment is posted, and report the hold as the outcome rather than as a failure or a blocker.

## Merge and verify

Skip this section entirely when the issue withheld merge authorization; follow "Hold the pull request when merging is withheld" instead.

1. Before merging, fetch the latest PR state and confirm all required checks are successful, the PR is mergeable, no required review or unresolved conversation blocks it, required UI screenshots or screen recordings are present, and the head SHA is the one that passed CI.
2. Merge using the repository's required or established merge method. When the repository allows more than one method, prefer them in this order: squash, then merge, then rebase. When squashing, use the PR's title and body as the squash commit message rather than overriding it — the body already carries the standalone `Closes #<ISSUENUMBER>` line. If only merge or only rebase is available, use that method instead, keeping the standalone closing reference intact regardless of method.
3. Delete the remote issue branch after a successful merge when repository policy permits.
4. Verify the PR is merged, the merged commit is reachable from the remote default branch, and GitHub closed issue `#<ISSUENUMBER>`. Allow for a brief GitHub processing delay, but do not claim closure without checking.

## Create follow-up issues

Review for unresolved follow-up work at both terminal boundaries: immediately before stopping a failed or stalled run, whether or not a pull request exists, and after a pull request is merged. Read the originating issue, any pull request body and comments, and the current conversation for every explicit blocker, TODO, or known issue. Do not wait for a merge when the run is about to stop. This section does not apply to a run that ended under "Hold the pull request when merging is withheld": a held pull request files no follow-up issues at all and reports the same items in its completion-status comment instead.

1. Turn each distinct, actionable item into its own issue in the same repository. Do not create issues for completed work, vague observations, or items that already have an equivalent open issue; link the existing issue instead.
2. Give each new issue a focused title and enough context and acceptance criteria to be actionable without rereading the pull request.
3. Associate every new issue with the originating issue and, when one exists, the pull request where the blocker or remaining work was found. Prefer repository or project relationship metadata when it is supported. Otherwise include `Addresses #<ISSUENUMBER>` in the issue body, plus `and #<PRNUMBER>` when a pull request exists.
4. When the originating issue has an unresolved issue dependency that the follow-up addresses, make the follow-up a sub-issue of that related blocking issue. Read the dependency relationship and the new issue's GitHub issue ID, then use GitHub's sub-issue API; do not merely mention the blocker in Markdown. If multiple blockers exist, attach the follow-up only to the specific blocker it addresses; ask for direction rather than guessing when the relationship is ambiguous. Apply the same relationship to an equivalent existing open issue that is reused, unless it is already a sub-issue of that blocker.
5. Build every new follow-up issue's labels from the follow-up itself rather than copying the originating issue's label set. Read the repository's available labels first (`gh label list`) and apply its own equivalent of the kind of work the follow-up describes: a defect gets whatever this repository calls a bug (`bug`, `bugreport`, `defect`), new work gets whatever it calls a feature (`enhancement`, `feature`, `Feature`). Match the repository's actual label names case-insensitively and never invent a label it does not define; when it defines no equivalent, leave the kind unlabelled rather than approximating it.
6. Carry an originating label over only when it is still accurate for the follow-up. A label that describes something the follow-up shares with the originating issue — an area, a component, a release train, a priority, a `milestone123`-style grouping — is carried over. A label that describes the originating issue itself and could contradict the follow-up is not: its kind label (`bug` on an issue whose follow-up is a feature request, and the reverse) and its triage or workflow state (`needs-triage`, `duplicate`, `wontfix`, `good first issue`). A carried label never overrides the kind label chosen for the follow-up above; when the two disagree, the follow-up's own kind wins and the originating one is dropped.
7. Carry the originating issue's milestone and assignees onto every new follow-up issue unchanged. Read them from the originating issue and set them together with the labels decided above when the follow-up is created (`gh issue create --label ... --milestone ... --assignee ...`), or add them immediately afterwards with `gh issue edit`. Skip only a value the repository will not accept — a label that no longer exists, a closed or missing milestone, a user who cannot be assigned — and say which value was skipped rather than dropping the whole step. Do not invent a milestone or an assignee the originating issue does not carry.
8. Inspect the originating issue's project items. When it belongs to one or more GitHub Projects, add every new follow-up issue to the same projects and set each new item's Status to that project's `Todo` option. Match `Todo` case-insensitively, but do not silently choose a different status. Apply this routing before stopping a failed or stalled run as well as after a merge.
9. When the originating issue has no project items and `.glorp.json` was present in the invocation directory, assign every new follow-up issue to the authenticated user, in addition to any assignees inherited above, with `gh issue edit <NEWISSUENUMBER> --repo <OWNER/REPO> --add-assignee @me` so Glorp can dispatch it. Use the presence recorded before cloning; do not assume the ignored state file will exist in the isolated checkout. Do not add the authenticated user to project-backed follow-ups; their inherited assignees still apply.
10. Apply the same project status or assignee routing to an equivalent existing open issue that is reused, unless it is already routed correctly or its current project status shows that work has begun or finished. Add the labels decided above, plus the inherited milestone and assignees, to a reused issue only where it carries none of its own for that field, so its existing triage is never overwritten or removed.
11. Record the URLs of all new or reused follow-up issues for the final report. If no qualifying items exist, explicitly record that no follow-up issues were needed.
12. Treat failures to create an issue, establish its origin links or required sub-issue relationship, label the follow-up or copy the originating issue's milestone or assignees, assign the required user, copy project membership, or set the `Todo` status as actionable errors and retry them. Do not end a failed or stalled run, and do not report a merged workflow complete, while required follow-up issue work is unfinished.

## Report the result

Lead with the merged outcome. Include the issue and PR URLs, branch name, final commit or merge SHA, clone path, changelog file, local tests, completed CI checks, follow-up issue URLs or confirmation that none were needed, and UI screenshots or screen recordings, or confirmation that they were not applicable. Confirm both PR merge and issue closure. If genuinely blocked, identify the exact failed step, relevant URL or log details, the remaining requirement, and every follow-up issue created or reused for it; preserve the isolated clone and branch for continuation. If the run ended by holding the pull request because merging was withheld, lead with that instead of a merge: report the pull request as ready and unmerged, name the label or quote the wording that withheld the merge, give the URL of the completion-status comment, and list the follow-up items reported there as unfiled rather than as issue URLs. If the run ended because the issue is a tracking issue, report that no PR was created, list the sub-issues created or already present with their URLs and states, and say whether the parent was closed or is still waiting on open sub-issues. If the workflow stopped during validation without opening a pull request, report that no PR was created, quote the reason, include every follow-up issue created or reused, and include the URL of the explanatory comment posted on the issue (or confirm none was needed if the issue does not exist).

## Clean up the clone

Remove the isolated clone directory and any temporary files. If the workflow was blocked or failed, leave the clone intact for further investigation, but report its location to the user.
