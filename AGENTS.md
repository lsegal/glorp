# Repository instructions for agents

- Write every instruction in this file as one short line in simple English.
- Add new rules as more one-liners; do not write paragraphs or multi-line entries.
- Never write a test that reads `CHANGELOG.md` or asserts on its text, including `## Unreleased`.
- Releases reword, reformat, and promote changelog entries, so those tests break for no real reason.
- Test the behavior instead: assert against the source, the flags, the workflow, or the README.
- Still add a `## Unreleased` changelog entry for user-visible changes, matching the surrounding format.
