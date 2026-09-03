# Repository instructions for agents

## Do not test `CHANGELOG.md`

Never write a test that reads `CHANGELOG.md` or asserts on anything in it, including
its `## Unreleased` section. The changelog is release prose: entries get reworded,
reformatted, and promoted under a version heading at release time, so any assertion
about its text fails for reasons that have nothing to do with the behavior under test,
and the release itself has to edit the test suite to go out.

Test the behavior instead. When a fact matters enough to guard, assert it against the
source, the flag set, the workflow, or the README section that documents it -- the
places that are supposed to stay in step with the code. A changelog entry that is
worth keeping is worth keeping there too.

This does not change how the changelog itself is written: user-visible changes still
get an entry under `## Unreleased`, following the surrounding format.
