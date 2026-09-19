#!/usr/bin/env python3
"""ci-scope.py — decide whether a pull request needs the expensive CI jobs.

Reads the changed paths on stdin, one per line, and prints `code=true` or
`code=false` for a workflow to gate jobs on, followed by `image=true` or
`image=false`: whether the change can alter a published image (see
`reaches_image`), which decides whether a release candidate is worth building.

A pull request that only edits prose cannot change what the emulator does, and
the Go suite, the SPA build and the image builds are the bulk of a CI run.

Why this is a classifier and not a `paths-ignore:` on the workflow
------------------------------------------------------------------
Fourteen of test.yml's jobs are in the branch ruleset's required set (the
list is kept in test.yml's own header comment), and a required check that
never reports leaves the pull request waiting on it forever. `paths-ignore:` skips the whole workflow, so those checks would never
report at all.

The obvious repair — a twin "skip" workflow declaring the same job names — is
broken for the mixed case. GitHub skips on `paths-ignore` only when *every*
changed file matches, but runs on `paths` when *any* one does, so a pull
request touching a doc and a handler satisfies both and two different runs
report under the same check name.

So the jobs keep their names and always run; only their expensive steps are
conditional. A prose-only pull request gets the same green checks, in seconds
instead of minutes.

Bias: anything unrecognised counts as code. A false `true` costs a CI run; a
false `false` skips the suite on a change that needed it, which is the failure
this must not have. An empty list is therefore `true` as well — not knowing
what changed is not the same as knowing nothing did.
"""

import os
import sys

# Paths that cannot change emulator behaviour. Deliberately short.
#
# A *published* doc is not one of them. embed.go compiles docs/*.md, docs/cdk
# and docs/services into the binary, and internal/docsindex derives the console
# navigation and the search corpus from exactly that set at runtime — so a
# published doc is an input to the Go build and to tests that assert on it
# (internal/docsindex/corpus_test.go pins search rankings over the real
# corpus). Editing one is a code change and runs everything.
#
# This used to fall out by accident: a docs edit also had to regenerate two
# committed index artifacts, and those were matched as code. The artifacts are
# gone (see internal/docsindex), so the rule is stated directly rather than
# depending on a side effect that no longer happens.
#
# docs/plans/ and docs/dev/ are contributor-only. They are excluded from the
# embed pattern and from the index, so nothing builds or tests them — which is
# what keeps a plan-doc edit, this file's main beneficiary, genuinely free.
#
# compat/baseline/ is output, not input: it is a status ledger (one JSON shard
# per compat suite, recording pass/fail counts) that compat.yml writes after a
# push to main and opens a promotion PR for. Nothing builds or tests against
# those files — the only thing that reads them is `cmd/compat --lint-baseline*`,
# which the promotion PR's own compat `aggregate` job runs regardless of what
# this classifier decides. Without this entry, a promotion PR that touches
# only compat/baseline/*.json is classed as code and runs the entire test.yml
# and compat.yml suites (~90 job-minutes) to validate a ledger file.
PROSE_SUFFIXES = (".md",)
PROSE_PREFIXES = (
    "docs/plans/",
    "docs/dev/",
    ".changelog/",
    "compat/baseline/",
)

# Published docs: read by the build and by the tests, whatever their suffix.
CODE_PREFIXES = ("docs/",)

# Contributor files that sit under a prose prefix but are still read by a check.
# The tells allowlist is an input to `make docs-lint`: an entry that stops
# matching fails the build, so editing it on its own has to run the docs gate or
# the failure lands on whoever pushes next.
CODE_PATHS = ("docs/dev/llm-tells-allowlist.txt",)


def is_prose(path: str) -> bool:
    """Is this a file no build, test or image reads?"""
    if path in CODE_PATHS:
        return False
    if path.startswith(PROSE_PREFIXES):
        return True
    if path.startswith(CODE_PREFIXES):
        return False
    return path.endswith(PROSE_SUFFIXES)


def needs_code_jobs(changed: list[str]) -> bool:
    """Does this change set need the expensive jobs?"""
    if not changed:
        return True
    return any(not is_prose(p) for p in changed)


# What can reach a published image: the build context's content-address inputs
# from test.yml's "Name the release candidate" step, restated as a predicate.
# That step's own comment holds the reasoning (the four routes from the context
# into the image, and why each narrowing is safe), so it is not repeated here.
# Keep the two in step: ci_scope_test.py fails when they drift.
#
# VERSION is on this list although it is not part of the context: the hash
# takes it as a literal line, because it names the candidate, and a change to
# it is the release PR itself.
IMAGE_FILES = (
    "go.mod",
    "go.sum",
    "embed.go",
    "embed_slim.go",
    "Dockerfile",
    ".dockerignore",
    "VERSION",
)
IMAGE_PREFIXES = (
    "cmd/overcast/",
    "internal/",
    "web/",
    "docker/",
    "docs/cdk/",
    "docs/services/",
)


def reaches_image(path: str) -> bool:
    """Can this file change the bytes of a published image?"""
    # The toolchain drops both from `go build`, and non-test code cannot
    # reference them, so neither can reach the binary.
    if path.endswith("_test.go") or "/testdata/" in path:
        return False
    if path in IMAGE_FILES or path.startswith(IMAGE_PREFIXES):
        return True
    # `docs/*.md`: the shell glob in the hash, one level deep.
    return path.startswith("docs/") and path.count("/") == 1 and path.endswith(".md")


def changes_image(changed: list[str]) -> bool:
    """Does this change set alter what an image would contain?

    A pull request that does not builds bit-identical images to main, so a
    release candidate for it says nothing. The bias is the same as
    `needs_code_jobs`, for the same reason: a candidate built for nothing costs
    a run, but a release PR that failed to get one ships untested. An empty
    list is therefore `True`.
    """
    if not changed:
        return True
    return any(reaches_image(p) for p in changed)


def main() -> int:
    # Anything that is not a pull request — a push to main, a release, a manual
    # dispatch — runs the lot. This exists to make review cheaper, not to
    # decide what main is allowed to skip.
    if os.environ.get("EVENT_NAME") != "pull_request":
        print("code=true")
        print("image=true")
        return 0

    paths = [line.strip() for line in sys.stdin if line.strip()]
    print(f"code={'true' if needs_code_jobs(paths) else 'false'}")
    print(f"image={'true' if changes_image(paths) else 'false'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
