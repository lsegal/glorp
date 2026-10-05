---
name: gh-create
description: "File a well-formed GitHub issue from a short user request: expand it into a structured issue, quote the original prompt verbatim at the bottom, apply every matching existing label, assign it to the current gh user by default, upload and embed any referenced screenshots or videos, and scrub private data before filing. Use when the user invokes `/gh-create`, `$gh-create`, or asks an agent to create, file, or open a GitHub issue."
---

# File a GitHub Issue

Treat `/gh-create [OWNER/REPO] <request>` as authorization to file one GitHub issue that describes the request. Every agent that uses this skill files issues the same way: the same structure, the same labels, the same assignee, properly attached media, and no leaked private data. This skill only creates and edits the one issue it files. It never changes code, branches, pull requests, or other issues.

## Identify the target repository

1. Prefer `OWNER/REPO` given in the invocation or stated by the user. Otherwise use the current checkout's GitHub remote. Ask the user only when neither identifies a repository.
2. Require `gh` with an authenticated session that can create issues in the repository.
3. Check the repository's visibility before writing anything:
   ```sh
   gh repo view OWNER/REPO --json visibility,isPrivate,nameWithOwner
   ```
   Record whether the target is public. The privacy rules below depend on it.

## Research, optionally

Research makes the requirements sharper, but it is never required to file, and it must not hold up filing.

1. When the repository is checked out locally or readable through `gh`, you may do light, read-only research: find the files and features involved, read the existing behavior, and search for related or duplicate issues with `gh issue list --repo OWNER/REPO --state all --search "<keywords>"`.
2. If an open issue already covers the request, tell the user and link it instead of filing a duplicate, unless the user asks for a new issue anyway. Link closely related issues in the new issue with `#NUMBER`.
3. Never run builds, tests, or anything that changes state as part of research. When research is slow, unavailable, or inconclusive, file with what the request itself says.

## Write the issue

Expand the request into a clear, structured issue that another person or agent can act on without asking follow-up questions. Keep the user's intent. Do not invent requirements the user did not ask for; label any assumption you add as an assumption.

1. Write a short, specific title in the imperative or as a plain statement of the problem, such as `Add a dark mode toggle to settings` or `Crash when saving an empty file`.
2. Build the body in Markdown with the sections that fit the request:
   - `## Summary`: what is wanted or what is wrong, and why it matters.
   - For a bug: `## Steps to reproduce`, `## Expected behavior`, `## Actual behavior`, and environment details such as version and platform when known.
   - For a feature or change: `## Requirements` as a numbered list, and `## Acceptance criteria` as concrete, checkable outcomes.
   - `## Scope` or `## Out of scope` when the boundary is not obvious.
   - `## Screenshots` or inline media where the user referenced any.
   - `## Notes` for research findings, related issues, and pointers to relevant files written as repository-relative paths.
3. End every issue with the user's original prompt, quoted verbatim, so the issue is reproducible, replayable, and auditable. Put it last, in a collapsed block, with every line of the prompt prefixed by `> `:
   ```markdown
   ---

   <details>
   <summary>Original prompt</summary>

   > the user's request, exactly as written
   > including every line

   </details>
   ```
   Quote the prompt exactly, typos and all. The only allowed change is redacting private data under the rules below, and each redaction is marked `[redacted]` so readers can see that something was removed.
4. Write the body to a file and pass it with `--body-file`. Never pass a multiline body with literal `\n` escapes in `--body`.

## Scrub private data

Leave out anything private or sensitive, from the expanded body and from the quoted prompt alike. This applies to everything you write into the issue, including titles, alt text, and file names of uploaded media.

1. Omit or replace local file paths and home directories (`/Users/name/...`, `C:\Users\name\...`), usernames, real names, email addresses, phone numbers, hostnames, internal URLs, IP addresses, access tokens, API keys, passwords, cookies, and any other credential or secret. Refer to repository files by repository-relative path only.
2. Never mention a private repository, its name, its issues, or its code in an issue filed on a public repository, and never quote or paraphrase code from one. Assume anything that came from a private repository is private, even when it looks harmless.
3. When the target repository is private, you may reference other repositories the same people can already see, but still omit secrets and credentials.
4. Check the visibility of any other repository you are about to mention with `gh repo view OWNER/REPO --json visibility`. When it cannot be read, treat it as private.
5. Inspect media before uploading it. Do not upload a screenshot or recording that shows secrets, tokens, private repositories, personal data, or other people's information. Crop or redact it first when you can; otherwise leave it out and tell the user why.
6. In the quoted original prompt, replace each private value with `[redacted]` instead of dropping the whole prompt.

## Apply labels

Apply every existing label that fits, and never invent one.

1. Read the repository's labels with their descriptions:
   ```sh
   gh label list --repo OWNER/REPO --limit 200 --json name,description
   ```
2. Choose every label that matches the issue: its kind, such as `bug`, `enhancement`, `documentation`, or `question`; platform labels such as `windows`, `macos`, `linux`, `ios`, or `android`; area or component labels such as `ui`, `cli`, or `api`; and any other label whose name or description clearly applies. Match the repository's actual label names case-insensitively and use the exact spelling the repository defines.
3. Never create a label, and never apply a label the repository does not define. When nothing fits a category, leave that category unlabelled.
4. Apply labels the user explicitly asks for when they exist. Tell the user about any requested label that does not exist instead of creating it.

## Assign the issue

1. Assign the issue to the current `gh` user by default with `--assignee @me`.
2. When the user names a different assignee, assign that user instead. When the user asks for no assignee, leave the issue unassigned.
3. If the assignment is rejected, file the issue anyway, and report that the assignment failed.

## Attach referenced media

Screenshots, videos, and other files the user mentions or provides must be uploaded to GitHub and embedded in the issue. Never leave a local file path, a `file://` URL, or a placeholder where the media belongs: nobody else can open it.

1. Collect every media file the user referenced, attached, or pointed to. Confirm each file exists and scrub it as described above.
2. Write each one into the body where it belongs, using its local path, so the issue reads correctly before upload:
   ```markdown
   ![Settings page after clicking Save](./screenshots/settings.png)

   ![](./recordings/crash.mp4)
   ```
   Put a video reference alone in its own paragraph with empty alt text.
3. Prefer the GitHub CLI's built-in upload. Check for it with `gh issue create --help`; when it lists `--attach`, pass one `--attach` per file:
   ```sh
   gh issue create --repo OWNER/REPO --title "TITLE" --body-file BODY.md \
     --label LABEL --assignee @me \
     --attach ./screenshots/settings.png --attach ./recordings/crash.mp4
   ```
   GitHub CLI uploads each file and replaces matching local paths in the body with the uploaded URLs. It appends any attached file the body does not reference to the end of the body, in the order the flags were given. Add alt text to an image with `--attach 'PATH#ALT TEXT'`; videos do not take alt text. The same flag works on `gh issue edit` and `gh issue comment`. Uploading needs push access to the repository, and only image and media files are accepted.
4. When `gh` has no `--attach` flag, or it fails, upload each file to GitHub's user-attachments endpoint and put the returned URL in the body yourself:
   ```sh
   REPO_ID=$(gh api repos/OWNER/REPO --jq .id)
   curl -sf -X POST "https://uploads.github.com/user-attachments/assets?name=FILENAME&content_type=MIME_TYPE&repository_id=$REPO_ID" \
     -H "Authorization: Bearer $(gh auth token)" \
     -H "Accept: application/json" \
     --data-binary @PATH/TO/FILE
   ```
   The response is JSON whose `url` field is a `https://github.com/user-attachments/assets/...` address. Replace the local path in the body with that URL: `![alt text](URL)` for an image, and the bare URL alone on its own line for a video. This endpoint is undocumented, so treat an error or an unexpected response as a failed upload.
5. If you have already created the issue and an upload failed, add the media later with `gh issue edit NUMBER --body-file BODY.md --attach PATH` or `gh issue comment NUMBER --attach PATH`, rather than filing a second issue.
6. When a file still cannot be uploaded after these fallbacks, remove its local path from the body, describe what the media showed in words, and tell the user which file could not be attached and why. Never commit media to the repository to work around a failed upload.

## File and verify

1. Create the issue with the title, body file, labels, assignee, and attachments decided above, in one `gh issue create` command when possible.
2. Read the issue back and check that it rendered as intended:
   ```sh
   gh issue view NUMBER --repo OWNER/REPO --json url,title,body,labels,assignees
   ```
   - The body has real line breaks, the structured sections, and the quoted original prompt at the bottom.
   - Every referenced file appears as a `https://github.com/user-attachments/...` URL or another uploaded GitHub URL, and no local path, `file://` URL, home directory, or placeholder remains.
   - The labels and assignee are the ones you chose.
   - Nothing private slipped through, checked against the scrub rules above.
3. Fix any problem you find with `gh issue edit` on the same issue. Never file a second issue to correct the first.

## Report the result

Give the issue URL, its title, the labels applied, the assignee, and the media that was attached. Name any requested label, assignee, or media file that could not be applied, and why. When you linked an existing issue instead of filing a duplicate, say so and give its URL.
