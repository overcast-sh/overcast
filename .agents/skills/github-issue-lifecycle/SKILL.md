---
name: github-issue-lifecycle
description: GitHub issue lifecycle management for Overcast. Use when creating, reading, triaging, labeling, updating, linking, or closing GitHub issues and issue-backed project work.
compatibility: opencode
metadata:
  audience: contributors
  workflow: github-issues
argument-hint: "Issue title, issue number, label query, or work description"
license: MIT
---

# GitHub Issue Lifecycle

Use this skill when managing actionable Overcast work in GitHub Issues. Issues are the source of truth for work to do. GitHub Projects are views over issues. Markdown docs in the repository record durable implementation status after work lands; they are not the active task queue.

## Core Rules

- Search existing issues before creating a new one.
- Prefer one issue per independently actionable unit of work.
- Keep issue titles specific enough that another agent can pick them up without reading unrelated context.
- Apply service, type, and lifecycle labels consistently.
- Comment with material findings, blockers, verification results, and handoff notes.
- Link PRs to issues with `Fixes #123`, `Closes #123`, or `Refs #123` as appropriate.
- Close issues only when implementation, tests, and required docs/status updates are complete.
- Do not use issues for secrets, credentials, private endpoints, or unreleased security details.

## Required Commands

Use the GitHub CLI for GitHub operations:

```sh
gh issue list --label compat --state open
gh issue view 123 --comments
gh issue create --title "Compat review: SQS SendMessage" --label compat --label service/sqs --body-file /tmp/issue.md
gh issue edit 123 --add-label status/in-progress --remove-label status/ready
gh issue comment 123 --body "..."
gh issue close 123 --comment "Completed in #456."
```

When filtering work, use labels instead of free-text title matching whenever possible.

## Label Taxonomy

Every actionable issue should have at least one type label, one area label, one service label when applicable, and one lifecycle label.

Type labels:

- `compat` - AWS compatibility review or fidelity work.
- `bug` - implemented behavior is wrong or regressed.
- `enhancement` - missing or expanded supported behavior.
- `docs` - documentation/status-only work.
- `tests` - test-only or coverage-focused work.

Area labels:

- `area/emulation` - AWS-compatible service behavior, handlers, wire formats, SDK compatibility, and service state transitions.
- `area/web-ui` - React UI, dashboard behavior, UX, frontend routing, styling, and visual regressions.
- `area/bff` - Go BFF routes, SPA serving, `/api/*` proxy routes, and UI-facing backend endpoints.
- `area/docs` - README, service docs, status files, compatibility docs, and generated capability tables.
- `area/devex` - build tooling, CI, local workflow, release scripts, and contributor automation.
- `area/storage` - memory/sqlite store behavior, persistence, migrations, and state serialization.

Service labels:

- `service/s3`
- `service/sqs`
- `service/dynamodb`
- `service/lambda`
- `service/sns`
- `service/cloudformation`
- Add other `service/<name>` labels as needed, matching repository service names.

Compatibility labels:

- `aws-fidelity`
- `needs-aws-verification`
- `needs-tests`
- `implementation-mismatch`
- `intentionally-partial`

Lifecycle labels:

- `status/ready`
- `status/in-progress`
- `status/blocked`
- `status/needs-triage`
- `status/needs-review`
- `status/done`

Priority labels:

- `priority/p0`
- `priority/p1`
- `priority/p2`
- `priority/p3`

Priority is a derived value, not a judgment call — see [RICE Scoring](#rice-scoring) below.

Effort labels:

- `effort/small`
- `effort/medium`
- `effort/large`

Agent labels:

- `good-first-agent-task`
- `agent-ready`

## Creating Issues

Before creating an issue:

1. Search for duplicates by service, operation, and label.
2. Confirm the work is actionable and not already covered by an open issue.
3. Choose the matching issue template from `.github/ISSUE_TEMPLATE/`.
4. Apply labels during creation, not as a later cleanup step.
5. Score it and set its priority label per [RICE Scoring](#rice-scoring) — do this at filing time, not as a follow-up.

Title formats:

- `Compat review: <service> <operation/resource>`
- `Bug: <service> <operation> <short symptom>`
- `Implement endpoint: <service> <operation>`
- `Docs: <service/topic> <short change>`
- `Web UI: <page/component> <short symptom or change>`

Minimum issue body sections:

- Scope.
- References.
- Current behavior or status.
- Checklist or acceptance criteria.
- A `RICE:` scoring line (see below).
- Handoff notes if created from an interrupted task.

## RICE Scoring

Priority labels (`priority/p0`–`p3`) are not assigned by feel — they are derived from a RICE score,
computed on filing. This applies to every issue an agent files, not only compat-audit issues.
`docs/plans/backlog-rice.md` is the canonical model; read it before scoring anything non-obvious.
The short version:

```
RICE = (Reach x Impact x Confidence) / Effort
```

- **Reach** (0–1000): `service_reach x path_factor`. `docs/plans/backlog-rice.md` §2 has the full
  per-service reach table (`s3` 900, `iam` 750, `lambda` 800, down to `shield` 10, plus cross-cutting
  surfaces like `protocol` 700). `path_factor` (0.02–0.75) is your own estimate of what share of that
  service's users hit the specific path the issue describes — a default CDK property every stack
  sets scores high; a single boundary-validation case or a narrow, newer feature scores low.
- **Impact**: `3` silent wrong behavior on a path applications depend on, `2` a real divergence that
  fails visibly (or a silent one on a narrow path), `1` a correctness/honesty gap with a workaround,
  `0.5` fidelity polish nothing is misled by, `0.25` documenting an already-decided gap.
- **Confidence**: `1.0` cites file/line or a reproduction, `0.8` scope is well specified but behavior
  is inferred, `0.5` explicitly unverified (`needs-aws-verification`, a single unreproduced sighting).
- **Effort**: person-weeks from the `effort/*` label — `small` 0.5, `medium` 1.5, `large` 4.0.

Bands: `p0` >= 300, `p1` 100–299, `p2` 25–99, `p3` < 25.

Put the inputs in the issue body as a `RICE:` line — three numbers, one sentence of basis each, and
the resulting score and label, for example:

```
RICE: R=60 (s3 900 × ~0.07 — narrow but real path), I=2 (silent wrong behavior on that path),
C=1.0 (exact handler line cited), E=0.5 (S — reuses an existing helper). -> **240 -> priority/p1**.
```

A generated compat-audit issue inheriting its service's tier label is a different, coarser claim
("how important is this service") than a per-issue RICE score ("how important is this task") — see
`docs/plans/backlog-rice.md` §1 for why conflating the two once put 106 issues at `p0`. Don't read a
tier-inherited label as a considered per-issue judgment, and don't skip scoring a hand-filed issue
just because a generated sibling nearby already has a label.

## Reading And Selecting Work

When asked to pick up work:

1. List ready issues by label, for example `gh issue list --label compat --label status/ready --state open`.
2. Prefer issues with `agent-ready` or `good-first-agent-task` when the user has not named a specific issue.
3. Open and read the full issue, including comments, before making changes.
4. Check linked PRs or references if the issue mentions them.
5. Move the issue to `status/in-progress` only when you are actually starting implementation or review.

## Updating Lifecycle State

Use lifecycle labels as a state machine:

- `status/ready` means the issue is actionable.
- `status/in-progress` means an agent or human is actively working it.
- `status/blocked` means work cannot proceed without a named dependency or decision.
- `status/needs-triage` means scope, labels, priority, or acceptance criteria need maintainer review.
- `status/needs-review` means code or findings are ready for maintainer review.
- `status/done` means the issue is complete and should normally be closed.

When changing lifecycle labels, remove the previous lifecycle label in the same operation where practical:

```sh
gh issue edit 123 --remove-label status/ready --add-label status/in-progress
```

Always comment when marking an issue blocked, including the exact blocker and the next unblocking action.

## Progress Comments

Comment when there is durable information another agent needs:

- AWS docs or real AWS evidence used.
- Reproduction steps and observed behavior.
- Tests added or missing.
- Implementation files touched.
- Docs/status updates needed.
- Blockers and decisions required.
- Verification commands and results.

Avoid noisy comments for routine local steps that do not affect handoff.

## Compatibility Review Issues

For AWS compatibility work, combine this skill with `aws-compatibility-review`.

Required labels:

- `compat`
- `aws-fidelity`
- `area/emulation`
- `service/<name>`
- One lifecycle label.

Do not use `compat` for purely visual or UX bugs. If a web UI issue exposes an emulation bug, keep the original UI issue labeled `area/web-ui` and create or link a separate `area/emulation` issue for the backend behavior.

Use `needs-aws-verification` only when docs and existing evidence are insufficient and real AWS verification may be required. Real AWS calls require explicit user permission in the current conversation.

Completion requires:

- AWS docs referenced.
- Request parsing reviewed.
- Response shape reviewed.
- Error behavior reviewed.
- Tests added or updated for supported behavior.
- Implementation drift fixed or explicitly documented as partial/unsupported.
- Service docs/status updated when support claims changed.

## Web UI Issues

Use `area/web-ui` for React UI, page behavior, client-side data loading, frontend routing, styling, accessibility, and visual regressions.

Use `area/bff` instead of `area/web-ui` when the problem is in the Go BFF, including embedded SPA serving, `/api/*` routes, proxy behavior, or UI-specific backend responses.

Use both labels only when the issue genuinely requires coordinated frontend and BFF changes.

Web UI issues should include:

- Page or route.
- Component or feature if known.
- Expected behavior.
- Actual behavior.
- Reproduction steps.
- Browser and viewport/device details when relevant.
- Screenshots or logs when useful.

## Closing Issues

Close an issue only when all applicable completion criteria are satisfied.

Before closing, verify:

- The implementation or docs PR has merged, or the user explicitly asked you to close based on local work.
- Tests were added or updated when behavior changed.
- `docs/services/*`, `STATUS.md`, or compatibility trackers were updated when support status changed.
- The closing comment references the PR, commit, or reason.

Do not close as duplicate without linking the canonical issue.

## Projects

GitHub Projects are planning views. Do not store unique task details only in a Project field. Any work item that needs implementation, review, discussion, or closure should have a GitHub Issue.

If a Project is used, keep issue labels as the portable source of lifecycle state so agents can still manage work with `gh issue list` and `gh issue edit`.
