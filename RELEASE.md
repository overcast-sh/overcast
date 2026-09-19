# Release Process

This document describes how to cut an Overcast release using the repository's
GitHub release workflow.

Overcast is still pre-1.0. Treat every release as a local-development and CI
tool release, not a production readiness claim.

## Automation Overview

The normal release path is a release-prep pull request that updates `VERSION`
and is merged to `main`. Never push release-prep changes directly to `main`.
When `.github/workflows/release.yml` sees `VERSION` change on `main`, it
automatically builds, tests, creates or updates the matching GitHub release,
uploads native binaries, and publishes Docker images.

The same workflow can also run from:

- a published GitHub release event
- manual `workflow_dispatch`

In every trigger path, the workflow reads `VERSION`, derives the tag as
`v<VERSION>` unless a release event supplied a tag, and rejects mismatches between
the tag and `VERSION`.

## End To End

Day to day, PRs merge to `main` and each records its release notes as entry
lines in a fragment file under `.changelog/` — written with
`python3 scripts/changelog.py new`, documented in
[.changelog/README.md](.changelog/README.md). `CHANGELOG.md`'s `[Unreleased]`
section stays empty; nothing accumulates there, so concurrent PRs never
conflict over the changelog.

When a release is wanted:

1. **Branch** (never `main`) and set `VERSION` to the new version.
2. **Curate.** `changelog.py assemble` prints a draft section built from every
   fragment currently in `.changelog/`, grouped by category then area, with
   breaking entries flagged `**BREAKING**` and their `migration:` notes
   attached. Edit that draft into `CHANGELOG.md` in the house style, then
   delete every fragment file.
3. **Open the PR, then test the candidate.** CI treats any same-repo PR whose
   `VERSION` carries no tag as a **release candidate** — **RC** throughout this
   document (when it changes something an image is built from): it publishes RC
   images and the native binaries and maintains one
   bot comment linking them. Test those bits — they are what CI built, not a
   local rebuild. **Whoever prepares the release runs that testing and posts the
   evidence on the PR**, rather than handing a checklist to the approver; green
   CI proves the build is sound, not that the changelog's claims are true. What
   to test, and how, is the `release` skill under
   [.agents/skills/release](.agents/skills/release/SKILL.md); what the evidence
   has to contain is [Compatibility Evidence](#compatibility-evidence) below.
4. **Re-curate whenever `main` moves.** A PR merged after this branch last
   changed does not re-trigger anything here, and its fragments would ride
   onto `main` through a clean union merge. The changelog gate fails the PR
   until the new entries are folded into the release section — see
   [Keeping The Release PR Current](#keeping-the-release-pr-current).
5. **Merge.** The `Release` workflow builds and tests unattended, then pauses
   at the `release` environment for a one-click approval of the exact SHA.
6. **Approve.** Binaries, `SHA256SUMS`, both Docker images and the GitHub
   release publish, with release notes generated from the `CHANGELOG.md`
   section.

## Release Artifacts

The release workflow:

- verifies the release tag matches `VERSION`
- verifies `CHANGELOG.md` has a non-empty section for the release version
- verifies `[Unreleased]` is empty before publishing
- verifies no unconsumed changelog fragments remain in `.changelog/`
- runs `go vet ./...`
- runs `go test -race -count=1 -coverprofile=coverage.out -timeout=600s ./...`
- runs `pnpm run typecheck` for the web UI (both `tsconfig.app.json` and `tsconfig.node.json`)
- builds the web UI
- uploads native binaries for Linux, macOS, and Windows
- uploads `SHA256SUMS`
- publishes Docker images to GHCR:
  - `ghcr.io/overcast-sh/overcast:<version>`
  - `ghcr.io/overcast-sh/overcast-slim:<version>`
- moves whichever floating tags this release is entitled to move:
  - prereleases: `:<channel>` such as `:alpha` — plus `:latest` until the
    first stable release ships, so a plain `docker pull` works while
    everything published is an alpha
  - stable releases: `:latest`, plus the line tags `:<major>` and
    `:<major>.<minor>` so a user can pin a line
  - never backwards — see [Floating tags only move
    forward](#floating-tags-only-move-forward)
- creates or updates the GitHub release
- replaces the GitHub release notes with generated notes from the versioned
  `CHANGELOG.md` section

## Version Format

Use SemVer tags with a leading `v` for GitHub releases.

Examples:

- Alpha: `v0.0.1-alpha.0`
- Later alpha: `v0.0.1-alpha.1`
- Beta: `v0.0.1-beta.0`
- Stable: `v0.1.0`

The release workflow strips the leading `v` and requires the result to exactly
match the contents of `VERSION`.

For example:

```text
VERSION = 0.0.1-alpha.0
GitHub release tag = v0.0.1-alpha.0
```

For prereleases, the Docker channel tag is derived from the prerelease suffix.
`0.0.1-alpha.0` publishes `ghcr.io/overcast-sh/overcast:alpha` and
`ghcr.io/overcast-sh/overcast-slim:alpha`. The version string is the only thing that
decides this — not the prerelease checkbox on the GitHub release, which the tag
check above has already reconciled with `VERSION`. Whether the channel tag then
*moves* is a separate question, answered in [Floating tags only move
forward](#floating-tags-only-move-forward).

**Choosing the number is not a judgement call while in alpha.** The prerelease
counter increments and the base version stays put, whatever the release
contains — a breaking change does not move it. Nothing is guaranteed stable
yet, and although we do not break things on purpose, things are changeable
enough that changing them may break you. So `0.0.1-alpha.27` is followed by
`0.0.1-alpha.28` whether the release adds a service, fixes a bug, or removes
an endpoint.

That does not make breakage unimportant, it makes *saying so* the job: a
breaking entry is marked in its fragment and carries a `migration:` note, and
curation must land both in the release section (see below). The versioning
rules in [CHANGELOG.md](./CHANGELOG.md) — where breaking means MAJOR — take
effect at 1.0, and the markers accumulated through alpha are what will let
them be applied mechanically rather than reconstructed from memory.

## Branch Safety

Release prep must happen on a dedicated branch and be merged through a pull
request. Do not commit release-prep changes while the current branch is `main`,
even if you do not intend to push directly.

Create or switch to the release branch before making release-prep edits. This is
the first step of the workflow, not something to defer until commit time.

Before editing `VERSION` or `CHANGELOG.md`, verify the branch:

```sh
git branch --show-current
```

If the command returns `main`, stop and create/switch to a release branch first:

```sh
git switch -c release/x.y.z-alpha.n
```

For agents: if you discover you are already on `main` during release prep, stop
and ask before committing. The protected workflow is branch PR -> merge to
`main` -> release automation, not local commits on `main`.

## Preflight Checklist

Before merging the release-prep PR to `main`:

1. Confirm `main` is green for the standard test workflow. (The required
   status checks on `main` enforce most of this per merge already.)
2. Confirm **Aggregate Compatibility Results** is green on the latest `main`
   run — the baseline gate does the regression comparison automatically and
   lists any `pass -> fail` as annotations. Two readings of a red matter:
   regressions in areas your release touches are blockers; a red whose only
   cause is new `compat/flaky.json` entries is the quarantine sign-off from
   the flake flow, not a regression (see compat/AGENTS.md § Stabilising a
   flaky test).
3. Assemble the release notes from the changelog fragments:
   ```sh
   python3 scripts/changelog.py assemble x.y.z-alpha.n
   ```
   Curate the printed draft into a versioned section that exactly matches
   `VERSION`, for example `## [x.y.z-alpha.n] - YYYY-MM-DD` — merge same-area
   entries into single bullets per the house style (see
   `.changelog/README.md`) — then delete every consumed fragment file
   (everything in `.changelog/` except `README.md`).

   Entries the draft flags `**BREAKING**` need care: each carries a
   `migration:` note, and both the fact of the break and what to do about it
   must survive into the release section. They are the part of the notes a
   user cannot work out for themselves, so lead with them rather than letting
   them dissolve into a merged bullet.
4. Set `VERSION` to the exact release version without the leading `v`.
5. Ensure `[Unreleased]` exists but has no entries, and `.changelog/` holds
   only `README.md`. The workflow fails if `[Unreleased]` contains release
   notes or a fragment was left unconsumed. This is checked twice: once
   against the PR as pushed, and once against the tree the PR would produce
   if merged into `main` as it stands right now. The second check exists
   because a PR merged after this branch was last pushed does not re-trigger
   anything here — a fragment that lands mid-window would otherwise ride onto
   `main` through a clean union merge and fail the release after the merge.
   If it trips, re-curate the new fragment into the release section and push;
   the update re-runs the gate.
6. Run local scoped checks for release metadata changes:
   ```sh
   go test -count=1 ./cmd/compat
   go vet ./cmd/compat ./compat
   ```
7. Open a PR containing the release-prep change and merge it through GitHub.
   The merge commit on `main` starts the automated release. Do not push the
   release-prep commit directly to `main`.

## Creating An Alpha Release

### Automated prep

The **Release Prep** workflow
([`.github/workflows/release-prep.yml`](.github/workflows/release-prep.yml),
`workflow_dispatch`) does the mechanical half of steps 1-5 below: derives the
version, assembles and inserts the release section, repoints both compare
links, writes `VERSION`, deletes the consumed fragments, opens the PR, and
comments a summary that lists any breaking changes with their migration notes.

It stops there by design. It never merges — `VERSION` is owned in
[CODEOWNERS](.github/CODEOWNERS) — and publishing still waits on the `release`
environment's required reviewer.

What it cannot do is **curate**. The generated section is one bullet per entry;
the house style merges same-area entries into single bullets and tightens the
prose. Treat the PR as a draft to edit, not as finished notes.

Re-running it against an open release PR **does not rewrite the section**. It
comments with the entries that have landed on `main` since, rendered as they
will read in the changelog, because regenerating would discard curation already
done. Pass `regenerate: true` only when you genuinely want the section replaced
wholesale. `dry_run: true` prints the summary and diffstat without pushing or
opening anything.

**Exactly one release may be in flight.** Dispatching while a release PR is
open for a different version fails rather than opening a second: two would race
for the same fragments and produce two sections claiming the same entries.

### Automatic refresh

The same workflow runs on **every push to `main`**. When a release PR is open
it merges `main` into the release branch, **folds any new changelog entries
into the `## [x.y.z-alpha.n]` section of `CHANGELOG.md`**, deletes the fragment
files they came from, pushes, and comments saying what it added. When no
release PR is open it exits immediately.

**This needs nothing from you.** The release PR stays mergeable on its own: the
entries are appended to the section that already exists, so the changelog gate
goes green without anyone editing anything.

Folding is strictly additive. No bullet already in the section is rewritten,
reordered or removed, so curation done by hand survives untouched — which is
what makes it safe to run unattended. Reword the new bullets or merge them into
neighbouring ones whenever it suits; the next refresh will not undo it.

A new bullet is placed **next to bullets about the same area** when the section
already has any, so it stays grouped as it grows rather than collecting at the
bottom. Area is read from the `[sqs]` prefix, or from a curated display heading
like `**SQS (long polling)**`. When nothing matches, it goes at the end of its
category.

Merging `main` in matters for a second reason: a `pull_request` run is not
re-triggered by base movement, so without it the PR would sit green while going
stale. Refreshing the branch re-runs its checks against what will actually
merge.

Each push gets its own comment, because each reports a distinct event on `main`
— unlike the release summary, which is a sticky comment describing the whole
release.

If `main` conflicts with the release branch, nothing is changed and the comment
says so; resolve it locally and push.

It requires the `RELEASE_APP_CLIENT_ID` and `RELEASE_APP_PRIVATE_KEY` secrets and
fails without them, deliberately: a PR opened with the default `GITHUB_TOKEN`
triggers no `pull_request` workflows, so it would arrive with no CI, no RC
images and no changelog gate. The App is also what makes the PR author someone
other than the maintainer, so the CODEOWNERS review on `VERSION` is a check a
human can actually satisfy.

### Nothing merges into the release branch

The refresh job is the release branch's only writer, and a second one breaks it.
So a pull request whose **base** is a `release/**` branch is refused by the
`Release branch base` check (`.github/workflows/release-branch-base.yml`), which
fails it and puts the reason in the job summary from
`.github/release-bot/pr-into-release-branch.md`.

That shape is worse than it looks. `test.yml`, `compat.yml`,
`changelog-required.yml` and `release-hold.yml` are all `pull_request:
branches: [main]`, so a PR based elsewhere gets no Go tests, no web build, no
changelog gate and no breaking-change hold — the code closest to shipping would
be the least checked in the repository. Its changelog fragment is never folded
either: folding runs when `main` moves *into* the release branch, so a fragment
arriving on the branch itself trips nothing and fails the release later, on
`main`, as `Changelog fragments remain in .changelog`. And its diff never
appears in a pull request into `main` at all, so it reaches `main` under the
release PR's approval rather than its own.

The fix is always the same: rebase onto `main` and retarget. The fragment stays
exactly as written — while the release PR is open the bot folds it into the
section on the next push, so the change still ships in that release. Pushing
onto the release branch itself remains the escape hatch for a late fix that has
to be *in* the release: it is reviewed as part of the release PR and checked by
its CI, and its release note goes straight into the `## [x.y.z]` section rather
than into a fragment — see [Breaking Changes During A Release
Window](#breaking-changes-during-a-release-window) below.

The steps below remain the manual path, and describe what the workflow does.

For an alpha release:

1. Create and work on a release branch, not `main`:
   ```sh
   git switch -c release/x.y.z-alpha.n
   ```
2. Update `VERSION`:
   ```text
   x.y.z-alpha.n
   ```
3. Assemble and curate the changelog fragments into:
   ```markdown
   ## [x.y.z-alpha.n] - YYYY-MM-DD
   ```
   using `python3 scripts/changelog.py assemble x.y.z-alpha.n` as the starting
   draft, then delete the consumed fragment files from `.changelog/`. Carry
   every `**BREAKING**` entry and its `migration:` note into the section — see
   Preflight step 3.
4. Leave the `[Unreleased]` section present and empty (it stays empty between
   releases; fragments are the only unreleased record).
5. Update the compare links at the bottom of `CHANGELOG.md` — add a
   `[x.y.z-alpha.n]` reference and repoint `[Unreleased]` at the new tag:
   ```markdown
   [Unreleased]: https://github.com/overcast-sh/overcast/compare/vx.y.z-alpha.n...HEAD
   [x.y.z-alpha.n]: https://github.com/overcast-sh/overcast/compare/v<previous>...vx.y.z-alpha.n
   ```
   Then confirm steps 2-5 with the same validator the `Release` workflow runs,
   rather than discovering a missing link reference in CI:
   ```sh
   python3 scripts/check-release-changelog.py x.y.z-alpha.n
   ```
6. Commit the release-prep changes on the release branch and open a PR.
   CI treats any same-repo PR that changes what an image is built from
   (`scripts/ci-scope.py`) and whose `VERSION` has no `v<VERSION>` tag yet as
   a **release candidate** (`scripts/release-candidate-check.sh` — this also
   covers follow-up PRs after a failed release workflow, when the unreleased
   version already sits on `main`). Each candidate build publishes
   `ghcr.io/overcast-sh/overcast[-slim]:<version>-rc.<n>` (linux/amd64 and
   linux/arm64, the same platforms the release publishes, so the candidate
   runs natively wherever it is smoke tested; `<n>`
   increments per build), uploads the ten native binaries as workflow
   artifacts, and maintains one bot comment on the PR with pull commands,
   image digests, and the artifact table. Test the RC image — the
   exact bits CI built — rather than a local rebuild, and post the evidence
   on the PR before it is merged: see
   [Compatibility Evidence](#compatibility-evidence).
7. Merge the release-prep PR to `main`.
8. Watch the `Release` workflow. It builds and tests unattended, then
   **pauses at the `release` environment** before publishing anything —
   approve the deployment (one click, shows the exact SHA) to let the
   publish jobs run. Then watch until all jobs pass.
9. Verify the GitHub release `v<VERSION>` exists and contains native
   binaries plus `SHA256SUMS`.
10. Verify the Docker images exist. `:latest` is on this list because while in
   alpha it tracks the alpha channel — the release notes say exactly which
   moving tags this release wrote, and a tag they name must have moved:
   ```sh
   docker pull ghcr.io/overcast-sh/overcast:<version>
   docker pull ghcr.io/overcast-sh/overcast:alpha
   docker pull ghcr.io/overcast-sh/overcast:latest
   docker pull ghcr.io/overcast-sh/overcast-slim:<version>
   docker pull ghcr.io/overcast-sh/overcast-slim:alpha
   docker pull ghcr.io/overcast-sh/overcast-slim:latest
   ```
11. Smoke test the slim image. Published on remapped ports so this does not
   collide with your own instance on 4566/4567:
   ```sh
   docker run --rm -d --name overcast-smoke -p 4576:4566 -p 4577:4567 ghcr.io/overcast-sh/overcast-slim:<version>
   curl -sf http://localhost:4576/_overcast/health

   # It is a *slim* image only if both of these hold. /_overcast/mcp is registered in
   # !slim builds only, so anything but 404 means the console binary shipped
   # under the slim tag — which is what #798 was, undetected for two releases.
   curl -s -o /dev/null -w '%{http_code}\n' http://localhost:4576/_overcast/mcp   # 404
   curl -s http://localhost:4577/                                        # web UI not included in slim build

   docker stop overcast-smoke
   ```

   Both are asserted automatically as well — statically against the compiled
   binary in the `Dockerfile`'s builder stages, and at runtime by the
   `Docker build (slim)` CI job — so this step is a confirmation, not the only
   line of defence.
12. **Required: route reachability against the RC image itself.**
    `scripts/route-reachability.go` probes every capability Overcast declares
    Supported/Partial/Inert/WIP at its AWS-modeled wire binding and reports any
    that no SDK could actually reach — the fault class
    [docs/plans/route-reachability-audit.md](docs/plans/route-reachability-audit.md)
    exists to close, and its own residual "keep this in the RC process" item.
    `TestAllDeclaredCapabilitiesAreReachable` already runs the same sweep
    in-process on every PR (`go test -tags slim,dev ./tests/integration/router/...`),
    so this step's live-instance requirement — the one thing the in-process test
    cannot cover — is satisfied by the RC container already running from step 11,
    or from [Smoke](.agents/skills/release/SKILL.md#1-smoke--is-it-alive-and-correctly-wired)
    in the `release` skill:
    ```sh
    go run -tags dev ./scripts/route-reachability.go -endpoint http://localhost:4576
    ```
    `0 declared operations are unreachable at their modeled binding.` is the
    clean report. Any `unreachable` row is a release blocker.

## Keeping The Release PR Current

A release PR is a snapshot of `.changelog/` as it stood when the branch last
changed, and `main` keeps moving underneath it. This is the normal case, not
an edge one: late fixes are exactly what a release window attracts.

GitHub does not help here. A `pull_request` run is triggered by pushes to the
*head* branch, so merging something else into `main` re-runs nothing and the
PR's green checks stay green while going stale. The failure that follows is
quiet: the release PR deleted one set of fragments and the new PR added
another, so the merge is a clean union, the new fragment rides onto `main`
next to the `VERSION` bump, and the release fails *after* merging.

Two mechanisms catch it:

- The `Release` workflow re-runs `check-release-changelog.py` against the tree
  the PR **would produce if merged into `main` as it stands now**, not against
  the PR's stale base. A fragment that landed mid-window fails the PR by name.
- The compat and baseline gates compare against `origin/main` fetched at job
  time, so a `compat/flaky.json` or baseline change on `main` shows up as a
  spurious diff on any branch that has not caught up.

When either trips, the fix is the same:

1. Update the branch from `main` (merge or rebase — the repository
   squash-merges, so the branch's own history does not reach `main`).
2. Curate the newly arrived entries into the release section and delete their
   fragment files.
3. Push. That re-runs every check and re-publishes the RC images and binaries,
   so the bot comment describes the candidate you would actually ship.

A release PR that has been open across several merges is often cheaper to
recut than to reconcile: `assemble` regenerates the whole draft, and the
curation already done can be pasted back over it.

## Breaking Changes During A Release Window

A minor or patch release promises that nothing in it breaks. Because every
push to `main` is folded into the open release PR automatically, a breaking
change merged during the window lands in a section that was already written
and reviewed, and ships under a version number that said it was safe. So
while such a release is in flight, breaking changes wait.

**`0.x` is exempt.** Pre-1.0 makes no compatibility promise, and Overcast
spends its whole alpha there bumping a prerelease counter, so a hold would fire
on every release without protecting a promise anyone was given. Breaking
entries are still marked and still carry a `migration:` note — the marker is
there to tell users what broke, not to move a version number
(`.changelog/README.md`). The policy is one function,
`holds()` in `scripts/release-hold.py`, with unit tests next to it.

Two gates cover the window, and only one is ever red at a time:

| While | Gate | Catches |
| --- | --- | --- |
| the release PR is open | `Breaking-change hold` (`release-hold.yml`) | breaking entries only |
| it has merged, tag not yet published | `Check release version` (`release.yml`) | any unconsumed fragment |

Both rows describe PRs merging into `main` around the release. The release PR
itself is covered separately and by the same `Check release version` job, which
validates it twice — as pushed, and against the tree it would produce if merged
into `main` as it stands now. See
[Keeping The Release PR Current](#keeping-the-release-pr-current).

The hold reads the fragments a PR **adds**, so an entry already on `main` never
holds anything, and the marker decides: `!`, or a `-` (Removed) line that does
not say `-.`.

A third check, `Changelog entry` (`changelog-required.yml`), asks every PR that
adds no fragment to say the omission was deliberate. It is not a release gate,
but it tracks the same windows, because the right answer differs between them
and a late fix is exactly what a release window attracts. There are three
cases, not two — the release PR is one of them:

- **Row 1, release PR open.** A fragment is the answer and nothing else is
  needed — the `refresh` job folds it into the `## [x.y.z]` section on the next
  push to `main` and deletes the fragment as it goes. On such a PR
  `release-candidate-check.sh` is false, because its `VERSION` comes from
  `main`, which still carries the last tagged version. So the check asks for a
  fragment exactly as usual. If that fragment is a **breaking** one and the
  release in flight is not `0.x`, `Breaking-change hold` then blocks the PR
  until the release goes out — the fragment is still right and still gets
  folded in; it is the merge that waits, not the note.
- **The release PR itself.** Exempt, and silently: it consumes other PRs'
  fragments into the version section and adds none by construction, so there
  is no fragment it could add and no reason it could give. `release_pr()` in
  `scripts/changelog-required.py` recognises it by shape — `VERSION` and
  `CHANGELOG.md` both changed, nothing touched outside those and `.changelog/`,
  the new version untagged, and a non-empty `## [x.y.z]` section present. Push
  anything else onto the release branch and the check asks again, because a
  late fix landing there ships like any other change.

  Note that `release-candidate-check.sh` **is** true on this PR — its checked-out
  `VERSION` is the new, untagged one. That is why the exemption tests the whole
  shape rather than the predicate alone: read on its own it puts the release PR
  in Row 2, which was exactly the misfire on
  [#563](https://github.com/overcast-sh/overcast/pull/563), where the release PR was
  told the release had already merged.
- **Row 2, merged and untagged.** There is no release PR left to fold into and
  an unconsumed fragment fails the release, so the ask changes wording: wait for
  the tag, or waive and add the fragment once it is out. It does not go quiet —
  this is the window where a shipping bug fix is most likely.
- **Row 2b, merged and untagged, and the PR is what unblocks the release.**
  The release-prep PR merged, the `Release` workflow failed, and the fix for
  that failure is itself release-note-worthy. Neither Row 2 answer works:
  waiting for the tag is circular, because this change *is* what the tag is
  waiting on; and a fragment would sit unconsumed in `.changelog/`, which the
  workflow fails on — so it would block the very release it exists to unblock.
  Waiving and adding the fragment afterwards is not equivalent either: the code
  ships **in this release**, so filing its note under the next one describes
  behaviour users already have.

  **Write the note straight into the `## [x.y.z]` section** of the release
  being unblocked, add no fragment, and waive the `Changelog entry` check with
  a reason saying so. Then confirm with
  `python scripts/check-release-changelog.py x.y.z` before pushing.

  This is the one case outside the release PR itself where editing
  `CHANGELOG.md` is right, and it is safe for a specific reason: the release PR
  has **merged**, so `release-prep.yml`'s refresh job — the second hand the
  no-`CHANGELOG.md`-edits rule exists to protect — is no longer running. There
  is no branch left for it to fold into and nothing for the edit to conflict
  with. Do not generalise it to Row 1, where the bot is live and a second
  writer aborts its merge.

  Keep it rare and keep it minimal: one bullet describing the fix, in the
  section being released, in the same PR as the fix. Everything else about the
  window still holds — only changes needed to get the release out should be
  merging at all.

**No PR merging into `main` may answer with a `CHANGELOG.md` edit**, and the
check will not take one in place of a fragment. Only the release PR touches that
file: the `refresh` job merges `main` into its branch on every push, and a second
hand editing the same section aborts that merge (`conflict.md`) and stops the
release PR keeping itself current. That is the whole reason fragments are one
file per PR — and it is why the release PR's exemption is its shape and not its
`CHANGELOG.md` diff, which any PR could produce.

The release PR is the one exception, and only because there is no second hand:
it *is* the hand that owns the file. So a change pushed onto the release branch
that does need a note writes it into the `## [x.y.z]` section directly — a
fragment left in `.changelog/` would fail the release the PR is trying to cut.
That is what `changelog-missing-release-branch.md` says when the check asks. The
`refresh` job only ever appends to the section, so nothing written there is
rewritten under you. This is rare and it should stay rare: a fix that could go
through `main` on its own PR gets its own fragment, its own review, and folds
itself into the section unattended.

See [.changelog/README.md § When a change needs no
fragment](./.changelog/README.md#when-a-change-needs-no-fragment).

A held PR gets one comment from the bot listing the entries and the ways out —
wait, `/retarget` onto a next-major branch, split the compatible part out, or
correct an entry that is not really a break. There is no override label. If a
hold is genuinely wrong and cannot wait, that is an admin merge, deliberately:
the alternative is a label whose whole purpose is to be applied under time
pressure.

### Lifting the hold

Nobody has to remember to unblock anything. When the release PR closes —
merged or abandoned — `release-hold-lift.yml` re-runs the hold check on every
PR whose run is failing, and that check re-evaluates from scratch. One place
decides, one place speaks, so a lift cannot leave a PR green on a stale answer.

Held PRs are found by looking for a failed hold run rather than by a label: a
fork PR's run has no write token and could never be labelled, so half of them
would be invisible to a label sweep.

This needs **Actions: write** on the release App, which is a permission it did
not originally have (`.github/release-bot/README.md`). Without it the lift
workflow fails loudly and held PRs stay red until someone re-runs their checks
by hand — a delay, not a hole.

### `/retarget`

Commented on any open PR by the repository owner, a member or a collaborator:

```
/retarget v2.0.0
```

The bot repoints the PR at that branch, **creating it off `main` if it does not
exist yet** — which is usually the case, since the next-major branch tends not
to be there when the first breaking change wants it. It refuses `release/*`
targets: moving a breaking change onto the release branch would put it in the
release, which is the thing the hold exists to prevent. `/retarget main` puts
it back.

### Branch protection

`Breaking-change hold` and `Changelog entry` must both be required status checks
on `main`. Each runs on every PR — no path filter — so they always report, and
renaming either silently stops enforcing it.

`Release branch base` must **not** be one of them. It fires only on PRs whose
base is a `release/**` branch, so on `main` it would never report at all — and a
required check that never reports leaves every PR waiting for it forever. It
enforces itself by failing where it does run.

`Lockfile freshness` should be required as well. It runs on every PR (passing
without a word on the ones that do not touch `web/pnpm-lock.yaml`) and fails a
PR whose lockfile was generated against a `main` that has since changed its
own — the same rule as "require branches to be up to date before merging",
confined to the one file where merging two regenerated copies silently breaks
`main` (#1340). Its sweep job posts a check run under the same name onto open PRs when a
lockfile push lands on `main`, which is what makes it bite *before* the merge;
the two names have to stay identical. It is a gate on the merge, not a publishing
safeguard, and it is not a merge queue: two lockfile PRs that auto-merge in
the same instant are not caught.

## Manual Release Trigger

Manual GitHub release creation is optional. Use it only when the PR-merge
automation did not run or a maintainer intentionally wants to republish the
release from the existing commit. Do not use a direct push to `main` as a manual
release trigger.

If creating a GitHub release manually:

1. Use tag `v<VERSION>`, for example `v0.0.1-alpha.0`.
2. Target the release-prep commit on `main`.
3. Mark prerelease versions as prereleases.
4. Keep notes brief; the workflow replaces them with generated notes from
   `CHANGELOG.md` after assets and Docker images publish.

## Maintenance Releases

Everything above describes one line moving forwards. This section is about the
other shape: a fix that has to reach users who are **not** on the newest
version, because upgrading to it would cost them more than the bug does.

### While in alpha, the next alpha is the hotfix

**Pre-1.0 Overcast supports exactly one version: the latest alpha.** There is
no older line to patch, because there is no promise attached to an older line
that would make staying on it reasonable. Every release is
`0.0.1-alpha.<n+1>`, the counter moves whatever the release contains, and the
answer to "there is a bad bug in alpha.31" is alpha.32 — cut from `main`, by
the process above, today.

That is not a shortcut around the process, it is the process working. `main`
carries required status checks and a compat gate that fails on *any* failing
compat test, not merely on a regression against the baseline — see
[Preflight Checklist](#preflight-checklist) and the `--max-failures 0` step in
`.github/workflows/compat.yml`. So `main` is expected to be shippable at any
commit, and "cut a release from `main`" is a normal operation rather than a
risk. A hotfix branch would buy isolation from changes that have already passed
the same gates the hotfix would have to pass.

The rest of this section is therefore **the exception, not the default**, and
reaching for it below 1.0 means something has gone wrong that is worth naming
out loud: `main` is not shippable. Perhaps a large in-flight change is
half-landed, or a regression is known but not yet understood. In that state a
support branch off the last tag is the right tool — but the incident is that
`main` is red, and it should be handled as one rather than routed around
permanently.

### After 1.0: one branch per minor line

From 1.0, versions carry a promise and staying on an older minor becomes a
reasonable thing for a user to do. A fix then has somewhere else to go, and it
goes to a branch named:

```text
support/<major>.<minor>
```

`support/1.2`, never `support/1.2.4` — the branch outlives every patch on it,
so numbering it after one of them would leave a branch per patch and no branch
per line. Its `VERSION` walks 1.2.4, 1.2.5, 1.2.6 in place.

**Created lazily, from the tag, the first time a backport is actually
needed.** Nothing is pre-created:

```sh
git switch -c support/1.2 v1.2.3
git push -u origin support/1.2
```

A branch created in advance is a branch nobody has looked at since it was cut.
It accumulates no CI history, drifts silently out of date with the tooling
around it, and the first thing anyone does with it is discover what broke while
it sat there. Cutting it from the tag at the moment of need means its first
push is also its first full CI run, on the change that needs it.

**The namespace is deliberate.** `support/**` is not `release/**`, which
already belongs to the ephemeral prep branches from [Nothing merges into the
release branch](#nothing-merges-into-the-release-branch) — and
`.github/workflows/release-branch-base.yml` refuses every pull request whose
base matches it. A backport is reviewed like any other change and therefore
arrives as a pull request into its line, so putting maintenance branches in
that namespace would refuse the one thing they exist for. The refusal stays
scoped to `release/**` and does not catch `support/**`; that distinction is
load-bearing, not incidental.

**The supported window is the current minor and the one before it.** When 1.4.0
ships, 1.2 stops being supported and its branch stops being cut from. Two lines
is the most that can be tested honestly with one compat suite and one
maintainer.

### The fix lands on `main` first. Always.

A backport is a **cherry-pick out of `main`**, never a change written on the
support branch and merged the other way.

The reason is the release nobody would think to check: fix 1.2.4 on the support
branch, ship it, and then ship 1.3.0 from a `main` that never received the
fix — and the upgrade path regresses the bug. Users who did the responsible
thing and moved to the current minor get the fault back. Fixing on `main` first
makes that impossible by construction: every line that will ever be released
after the fix already contains it.

So the order is always:

1. Fix on `main` through a normal pull request, with its own review, its own
   CI and its own changelog fragment.
2. Merge it.
3. Cherry-pick the resulting commit onto `support/1.2` through a pull request
   into that branch.

If the fix cannot land on `main` — the code it touches no longer exists there,
say — that is not a backport, it is a change that exists only on the older
line. Write it as such, say so in its release note, and expect it never to be
represented on `main` at all.

### Nothing ever merges a support branch back into `main`

Its unique content is exactly two things, and both are wrong on `main`:

- **`VERSION`**, reading `1.2.4` while `main` is on `1.3.x`. A merge clobbers
  it, and the next push to `main` reads a version lower than the one already
  released.
- **A `## [1.2.4]` changelog section**, which a merge inserts wherever the
  merge driver puts it.

Everything else on the branch is by rule already on `main`. So there is nothing
to merge and something to break, and the branch is simply never merged. It is
cut from a tag, it receives cherry-picks, it is tagged, and eventually it is
left alone.

The one thing that *should* reach `main` is the release note, because
`CHANGELOG.md` on `main` is the record of everything ever released and a
version missing from it looks like a version that never existed. It goes as a
**docs-only pull request** — `CHANGELOG.md` and nothing else — adding the
`## [1.2.4]` section and its compare link.

**`CHANGELOG.md` sections are ordered by semver, descending — not by date.**
1.2.4 released *after* 1.3.0 still sits *below* it, because a reader scanning
for "what is in 1.3" reads down a version-ordered file and gets a wrong answer
from a date-ordered one. The dates in the headings remain the release dates and
will therefore not be monotonic; that is the honest rendering of what happened.

Two things about that pull request, both of which will otherwise surprise you:

- **Merge it only while no release PR is open on `main`.** It edits
  `CHANGELOG.md`, and while a release is in flight `release-prep.yml` merges
  `main` into the release branch on every push — a second hand in that file
  does not merely conflict, it aborts the merge and stops the release PR
  keeping itself current (`conflict.md`). This is the same rule as [No PR
  merging into `main` may answer with a `CHANGELOG.md`
  edit](#breaking-changes-during-a-release-window); the maintenance section is
  not an exception to it, it is just late enough that waiting costs nothing.
- **It needs a `/no-changelog` waiver.** The `Changelog entry` check asks every
  PR for a fragment and will not take a `CHANGELOG.md` edit instead, which is
  precisely what this PR is. The reason writes itself:
  `/no-changelog release-notes record for 1.2.4, which shipped from support/1.2
  and has its own section`.

One consequence of the ordering is worth knowing too:
`scripts/check-release-changelog.py` pins the compare link of the section being
released to the section directly below it, but checks older sections only
loosely — the base has to be *some* older section, not a specific one. With
1.2.4 inserted between 1.3.0 and 1.2.3, document order stops meaning "released
after": `[1.3.0]` correctly compares from `v1.2.3`, which is no longer the
section beneath it. Which release preceded which in time is not recoverable
from the file, so the check does not pretend to know.

### Changelog fragments do not work on a support branch

The fix's fragment was consumed by the release that shipped it on `main`, and
was deleted in that commit. A cherry-pick of the fix commit onto the support
branch therefore either brings a fragment back from the dead or brings nothing,
depending on which commit you picked — and a fragment left in `.changelog/`
fails the very release it would be describing (`Changelog fragments remain in
.changelog`). There is also nothing to fold it in: folding is `release-prep.yml`
merging `main` into a release branch, which never happens here.

So **the release note is written straight into `CHANGELOG.md` on the support
branch**, and any fragment a cherry-pick dragged along is deleted in the same
pull request:

```sh
git cherry-pick <sha>
git rm .changelog/20260808-whatever-it-was.md
```

This is not a new rule. It is the same one that already applies to a late fix
pushed onto a release branch — see [Breaking Changes During A Release
Window](#breaking-changes-during-a-release-window) and
[.changelog/README.md § During a release
window](./.changelog/README.md#during-a-release-window) — and for the same
reason: a fragment is a message to a future release-prep run, and on a branch
where no release-prep run will ever happen it is a message to nobody.

The `Changelog entry` check (`changelog-required.yml`) does not run on pull
requests into `support/**` for exactly this reason: it asks for a fragment and
refuses a `CHANGELOG.md` edit in its place, which is the wrong question and the
wrong answer on this branch. Nothing enforces the note here — write it.

### CI on a support branch

`test.yml` and `compat.yml` both run on `push` and `pull_request` for
`support/**`, on the same terms as `main`.

Compat in particular is not optional here, and the instinct that a small
backport hardly warrants nine suites has it backwards: **a cherry-pick is the
change least like the code it lands on.** It was written against a `main` that
has since moved by a whole minor, reviewed in that context, and is then applied
to a branch that has not moved at all. The compat baseline it is measured
against is the one the branch carries from its own tag, so the comparison is
against what that line promised, not what `main` promises now.

Baseline **promotion** stays on `main` alone — its step in `compat.yml` is
guarded on `github.ref == 'refs/heads/main'`. A support branch's baseline
describes an older line and must not drift.

Not everything follows. `release-prep.yml`, `release-hold.yml` and
`changelog-required.yml` remain `main`-only, each for its own reason, and the
next section says what that costs.

### Cutting the release

A maintenance release uses the **manual/tag path**, not `release-prep.yml`.
That workflow enforces one release in flight and refreshes an open release PR
from `main` on every push, both of which describe a `main` release; and its
single-flight lock exists so two prep runs cannot claim the same fragments,
which is not a hazard here because there are no fragments to claim. A support
branch cannot merge to `main` either, so the "`VERSION` changed on `main`"
trigger will never fire for it. Publishing comes from the `release: published`
event, which `release.yml` already supports.

1. Land every backport on `support/1.2` through pull requests, `main` first.
2. On a branch off `support/1.2`, set `VERSION` to `1.2.4` and write the
   `## [1.2.4]` section and its compare link directly into `CHANGELOG.md`.
   Confirm with the same validator CI runs:
   ```sh
   python3 scripts/check-release-changelog.py 1.2.4
   ```
   Open it as a pull request **into `support/1.2`**. CI treats it as a release
   candidate exactly as it would on `main` — its `VERSION` carries no tag —
   so it publishes `…:1.2.4-rc.<n>` images to smoke test.
3. Merge it. Nothing publishes: `release.yml`'s push trigger is `main` only.
4. Create the GitHub release for tag `v1.2.4`, **targeting the tip of
   `support/1.2`**, marked as a prerelease only if the version says so. That
   fires `release: published` and the workflow runs as it does for any other
   release, pausing at the `release` environment for approval.
5. Verify the images. `…:1.2.4` will exist; `:latest` will **not** have moved,
   and neither will `:1` — only `:1.2` follows a maintenance release. That is
   the point, and the release notes list exactly the moving tags that were
   written.
6. Open the docs-only pull request adding the `## [1.2.4]` section to `main`.

**Tag every maintenance release, without exception.** A tag is what makes the
commit reachable independently of the branch, and reachability is the whole of
what protects this history — see below.

### Floating tags only move forward

`:<version>` is written once and never moves. Every other tag the release
publishes is a pointer: `:latest`, `:alpha`, and the line tags `:1` and `:1.2`.
A pointer can move the wrong way.

Publishing `:latest` for any stable release — which is what the workflow did
before there was a maintenance path — sends `:latest` **backwards** the first
time 1.2.4 ships after 1.3.0, silently downgrading everyone who pinned it. The
same hazard already existed for `:alpha`: republishing an earlier alpha would
take the channel back with it.

The rule is one sentence:

> a floating tag moves only when the version being released is at least the
> highest version already released under that tag.

"At least" rather than "greater than", because equality means the tag already
points here — a republish of the same release, which
[If The Release Workflow Fails](#if-the-release-workflow-fails) allows — and
refusing to write a tag its own value is how a re-run silently loses `:latest`.

What each tag is compared against:

| Tag | Compared against | So that |
| --- | --- | --- |
| `:latest` | every stable release | a maintenance release of an older line cannot claim it |
| `:latest`, before any stable exists | every prerelease, all channels | a plain `docker pull` works while everything shipped is an alpha, and an older alpha cannot drag `:latest` back past a newer beta |
| `:alpha`, `:beta` | prereleases in that channel only | shipping 1.3.1 does not hold back `:alpha`, and vice versa |
| `:1.2` | stable releases on the 1.2 line | pinning a line gets that line's newest patch |
| `:1` | stable releases with major 1 | pinning a major does not go back a minor |

The second row exists because `docker pull ghcr.io/overcast-sh/overcast` with
no tag asks for `:latest`, and while every release is an alpha the honest
answer is the newest alpha rather than a 404 — which is what forced the
website's copy-paste commands onto `:alpha`. The first stable release retires
that row for good: from then on `:latest` is stable-only, and no prerelease
can claim it however new — `1.1.0-alpha.1` outranks `1.0.0` by SemVer
precedence, but it is not what an unqualified pull is asking for.

Line tags are **stable-only**. `:1.2` means the 1.2 line as it ships, not
whatever prerelease is being tried out on it — and below 1.0 the alternative
would be `:0` quietly meaning "the current alpha", which `:alpha` already says
out loud. So nothing publishes a line tag today.

The decision is [`scripts/release-channel-tags.py`](scripts/release-channel-tags.py),
with unit tests beside it, following the `release-hold.py` precedent: release
policy expressed as an `enable=` expression inside a workflow is neither
testable nor reviewable, and this one has to be right on a path that runs
perhaps twice a year. `release.yml` asks it once, in `check-version`, and
carries the answer to both publish jobs and to the release notes — which
therefore list the tags that actually moved rather than the ones a
stable/prerelease flag implies.

### Reachability is what protects the history

The risk on a maintenance line is not a wrong version number, which someone
would notice. It is an **unreachable commit**: a tag cut against a commit that
no branch contains, or a support branch deleted after its last release. Git
garbage-collects what nothing points to, and the failure appears months later
as a release whose source cannot be checked out.

Discipline does not survive a year of not thinking about it, so three things
enforce it, in descending order of how much they help.

**1. Branch rules on the support namespace, which a maintainer must
configure.** These are not implied by anything in this repository and no
workflow can apply them. In **Settings → Rules → Rulesets → New branch
ruleset**:

- Name: `support branches`
- Enforcement status: **Active**
- Target branches: **Include by pattern** → `support/**`
- Rules, all enabled:
  - **Restrict deletions** — the branch cannot be deleted, so the commits its
    tags point at stay reachable even after the line leaves support.
  - **Block force pushes** — history cannot be rewritten under a published tag,
    which is the same failure as deletion but harder to spot.
  - **Require linear history** — a maintenance line is a sequence of
    cherry-picks; a merge commit here means something was merged in that should
    not have been, most likely `main`.

Leave **Require a pull request before merging** to preference — backports
should go through review, and the `main`-first rule already means every one of
them has been reviewed once. Do **not** add required status checks to this
ruleset without first checking which ones actually run on `support/**`:
`Changelog entry` and `Breaking-change hold` do not, and a required check that
never reports blocks every pull request forever — the same failure mode
documented under [Branch protection](#branch-protection).

**2. The release workflow asserts reachability before publishing.** The
existing check compares the tag to `VERSION`, and `VERSION` is a file: it would
pass a tag cut against any commit that happens to carry the right contents — an
abandoned backport branch, a detached `HEAD`, a stale local checkout.
`release.yml` now additionally requires the commit being released to be
reachable from `main` **or** from the `support/<major>.<minor>` branch that
owns its line, and fails the release with instructions when it is neither. The
two cases cannot overlap: a support branch exists only for a line `main` has
already left.

**3. A scheduled audit reports unreleased backports.**
[`support-branch-audit.yml`](.github/workflows/support-branch-audit.yml) runs
weekly and lists every `support/**` branch whose tip is not contained in any
tag — the "backport merged, release never cut" state, where the work sits on a
branch nobody is looking at and in no release. It **reports and never fails**:
whether an unreleased backport should be released now is a maintainer's
judgement, sometimes deliberately waiting on a second fix, and a red X here
would train someone to ignore it.

### What is still manual

Named rather than smoothed over, because the gap between what is automated on
`main` and what is automated here is where a maintenance release will go wrong:

- **No `release-prep.yml` equivalent.** The version bump, the changelog
  section and its compare link are written by hand on the support branch.
  `check-release-changelog.py` checks them; nothing generates them.
- **No `Changelog entry` gate on backport PRs.** Fragments cannot work here,
  so the check does not run, so nothing asks whether a release note was
  written. Write it at release-prep time from the commits on the branch.
- **No breaking-change hold.** `release-hold.yml` is `main`-only and reads the
  release in flight from `main`'s `VERSION`. A maintenance release is a patch
  release and must not break anything; nothing enforces that here beyond the
  fact that its content is cherry-picks of changes already reviewed for
  `main`.
- **Dropping a line is a decision, not an event.** Nothing announces that 1.2
  has left the support window when 1.4.0 ships. Say so in the 1.4.0 release
  notes.

## If The Release Workflow Fails

Do not reuse a published tag for a different commit.

While a release is pending or failed — that is, while `VERSION` on `main`
has no `v<VERSION>` tag — every same-repo PR is treated as a release
candidate (`scripts/release-candidate-check.sh`): changelog validation runs,
RC images publish, and the merge discipline in AGENTS.md applies. A PR that
changes nothing an image is built from (a compat baseline promotion, tests, CI,
contributor docs) gets no RC images or summary comment, since they would be
bit-identical to main's and named for a version they are not a candidate of;
the changelog validation still applies to it. Re-running
the release workflow pauses at the `release` environment for approval again,
like any other publish.

If the workflow created a GitHub release but artifacts failed:

1. Fix the issue on `main`.
2. Create a new prerelease version, for example `0.0.1-alpha.1`.
3. Move the failed release notes forward, update `VERSION`, and merge to `main`
   so the workflow creates the next tag/release, for example `v0.0.1-alpha.1`.
4. Mark the failed release as superseded in its notes, or delete it if no
   artifacts were consumed.

If the failure is only transient infrastructure and the release tag still points
at the intended commit, rerunning the failed workflow jobs is acceptable.

## Post-Release Checklist

After the release workflow succeeds:

1. Confirm release notes were generated from `CHANGELOG.md`.
2. Confirm native binaries download and checksums match `SHA256SUMS`.
3. Confirm both Docker image families are published.
4. Confirm `README.md` quick-start commands work with the version tag.
5. Open a follow-up PR that:
   - sets `VERSION` to the next development version if needed
   - restores an empty `[Unreleased]` section in `CHANGELOG.md` if it was
     removed (it must always exist, and stays empty — unreleased changes are
     fragments under `.changelog/`)
   - updates compare links at the bottom of `CHANGELOG.md`

## Compatibility Evidence

Compatibility tests are not a 100% AWS parity gate. They are a regression and
coverage signal.

Before release, **post these as a comment on the release PR**, naming the exact
RC tag and image digests tested. Keeping them in a session nobody else saw means
the approval is made against nothing:

- merged `compat-results.json`
- GitHub workflow summary from Compatibility Tests
- any baseline comparison output
- a claim-by-claim account of the release section: every bullet, whether it was
  verified, and the observed request and response — not an assertion that it
  works
- screenshots for any console change, captured headlessly (the `release` skill
  § Console)
- what was **not** tested, and why. The gaps matter more than the passes,
  because they are the part nobody can infer from a green check

Known unsupported APIs may remain as `fail` or `unimplemented`. A previously
passing compat result becoming `fail` or `unimplemented` should block the
release unless a maintainer explicitly accepts the regression.

**Running the suite is two commands.** `--max-failures`, like
`--compare-baseline`, `--report` and `--check-parity`, is a *gate mode*: it
reads an existing `--results-file` and exits without running a single test. Pass
it alongside the run flags and nothing executes — the gate then reads whatever
`compat-results.json` is already on disk and prints `failure gate passed`,
exit 0. A release can be signed off against a stale file having run nothing. Run
first, gate second, and check the results file is freshly written before
believing a pass. See `compat/AGENTS.md` § "Flags that read a results file
instead of producing one".

**Anything found that is neither a regression nor a blocker becomes a GitHub
issue**, with the reproduction and an explicit pre-existing-versus-new
determination — made by running the same probe against the previous release's
image, and where it matters by dating the code with `git log -S` and
`git tag --contains`. A fault present in every tag is not this release's problem
and must not hold it up; a fault this release introduced is a blocker. Deciding
which, in writing, is what makes the release notes trustworthy.

## Docs-only candidates

There is no docs-only release path. Every release bumps `VERSION` once and
publishes both images and every binary together, so their versions and the
`:latest`/`:alpha` tags stay in sync — a release cut for a docs fix ships the
same artefacts as any other.

When the curated section contains only `[docs]` entries, the candidate still
gets a full RC build, but the slim image needs no separate scrutiny: it
differs from the previous release by nothing but its version string, since
slim embeds no docs. The `release` skill's testing pass is reduced
accordingly — see [.agents/skills/release/SKILL.md § Docs-only
candidates](.agents/skills/release/SKILL.md#docs-only-candidates). Anything
non-docs in the section restores the full pass.
