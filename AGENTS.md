# AGENTS.md

> **For AI agents** (Claude, Copilot, Cursor, etc.) and agent-assisted workflows.
>
> This file is the canonical agent instructions file. Other filenames are symlinks to it
> so that different agents pick it up automatically (e.g. `CLAUDE.md` → `AGENTS.md`,
> `.github/copilot-instructions.md` → `../AGENTS.md`).
> If your agent expects a different filename, create a local symlink rather than copying:
> `ln -s AGENTS.md GITHUB.md` / `ln -s AGENTS.md COPILOT.md` / etc.
>
> **Read [CONTRIBUTING.md](./CONTRIBUTING.md) first** — it has coding standards,
> core principles, error handling, logging, performance, design patterns, service package
> structure, the endpoint/service checklists, and web UI standards.
> Everything there applies to you too — this file adds agent-specific guardrails only.
>
> For test conventions, see [tests/AGENTS.md](./tests/AGENTS.md).
> For smoke testing by hand — and for why the AWS CLI alone cannot verify an endpoint change —
> see [docs/dev/manual-testing.md](./docs/dev/manual-testing.md).
> For current implementation status and what to build next, see [STATUS.md](./STATUS.md).
> For cutting a release — versioning, changelog curation, and testing the release candidate —
> see [RELEASE.md](./RELEASE.md).

## Repo-local skills

This repo includes skills under `.agents/skills`. The project `opencode.json`
registers that directory explicitly, and `.claude/skills` is a symlink to it, so
opencode and Claude Code both discover them without prompting.

- `aws-compatibility-review`: Use for AWS fidelity audits, compatibility tests, wire-format drift, and service behaviour parity.
- `bug-fix`: Use for diagnosing and fixing bugs with reproducing tests and full verification.
- `code-review`: Use for PR/code reviews, AWS parity checks, regression risk, performance, leaks, DRY/SOLID, and maintainability.
- `commit`: Use for clean commit creation, staging review, and commit-message hygiene.
- `documentation-audit`: Use for keeping documentation true — verifying counts, configuration references, behavioural claims and plan-doc statuses against the code, and reporting what has drifted.
- `documentation-writing`: Use for writing readable documentation — what the reader can be assumed to know, how to structure and pace it, which claims you can make, and how to explain a mechanism or a limitation.
- `git-worktrees`: Use for every mutating task so each agent works in an isolated, task-owned worktree.
- `github-issue-lifecycle`: Use for creating, triaging, updating, linking, and closing GitHub issues.
- `issue-coordination`: Use before putting more than one writer on the repo — selecting a batch of issues and deciding whether the pieces run in parallel, need a scope fence, or must be stacked.
- `new-feature`: Use for adding AWS endpoints, services, CloudFormation resources, or other product features.
- `pull-request`: Use for preparing PRs, PR descriptions, commit hygiene, screenshots for visual changes, and CHANGELOG decisions.
- `release`: Use for cutting a release — curating changelog fragments, bumping `VERSION`, and smoke/regression testing the release candidate before it ships.
- `stacked-prs`: Use when a PR depends on another PR that has not merged yet — building, linking, syncing and landing a chain of dependent PRs.

### Repo-local MCP servers

The repo declares one MCP server, `chrome-devtools`, in two places — [.mcp.json](./.mcp.json) for
Claude Code and the `mcp` block of [opencode.json](./opencode.json) for opencode. The two clients
read different files with different schemas, so the definition is duplicated on purpose.

**The two are deliberately not identical, and the difference is the point.** Claude Code's runs
with `--headless --isolated`: Chrome starts with no window and a throwaway profile, which is what
lets a background agent — which has no display to composite a window onto — drive a browser and
capture screenshots at all. opencode's is left headful because a human is sitting in front of it
and watching the browser is often the reason they reached for it. Match the flags to whether
there is a display, not to each other.

Adding or changing either file does not affect a session already running: the client reads it at
startup, so restart (and approve the server if prompted) before concluding the tools are
unavailable. Screenshot workflow and the judgement about which themes and widths to capture are
in the [`pull-request` skill § Visual Evidence](./.agents/skills/pull-request/SKILL.md#visual-evidence).

### Worktree policy

All repository mutations must happen in a dedicated, task-owned git worktree. Derive the default
worktree root portably as `<primary-checkout-parent>/.worktrees/<repo-name>`; an absolute
clone-local `overcast.worktreeRoot` Git config value may override it. Never commit a
machine-specific absolute path. The primary checkout is for read-only inspection and worktree
management unless the user explicitly asks an agent to edit it. If an agent is already running in
a dedicated worktree, it may continue there.

Before editing, use the `git-worktrees` skill and inspect the current repository root, branch,
and registered worktrees. Every write-capable sub-agent must receive its own uniquely named
worktree and branch under the resolved worktree root; two write-capable agents must never share a
checkout. Read-only sub-agents may share a checkout. If an isolated worktree cannot be created,
perform the work sequentially or ask for direction instead of allowing concurrent writers.

Claim a write-capable worktree with Git's portable lock metadata while it is active:
`git worktree lock --reason "owner=<agent-or-user>; task=<task>; claimed=<ISO-8601>" <path>`.
Use a stable task or session identifier when one is available. A host and process ID may be added
as diagnostics, but they are not proof that the claim is live: processes end, PIDs are reused, and
another host cannot inspect them. `git worktree list --porcelain` exposes the lock and its reason.
The lock is an advisory ownership marker and removal/pruning guard, not an exclusive filesystem
lease; agents must still obey the one-writer-per-worktree rule. Only the owner (or a user after
confirming that the owner is gone) may unlock a claimed worktree.

The parent agent owns integration and cleanup of any worktrees it creates for sub-agents. Never use
`git stash` in a linked worktree: the stash stack is shared by the entire repository, so concurrent
agents can restore or drop one another's entries. The Claude and Codex `PreToolUse` hooks enforce
this rule. Prefer a temporary WIP commit, then amend or squash it before review; alternatively,
move the changes explicitly to the intended task branch or worktree.

This repository lands PRs with squash merge. A worktree branch's commit SHA therefore normally
does not appear in `main`; verify integration from the PR's merged state and `mergeCommit` (for
example, `gh pr view <pr> --json state,mergedAt,mergeCommit`) rather than relying on
`git merge-base --is-ancestor`, `git branch --merged`, or finding the branch commit in `main`.

When a task is complete or handed off, first ensure its commits are recoverable from another
checkout (pushed or integrated), verify the task worktree is clean, then remove it from another
checkout with `git worktree remove <absolute-path>` and run `git worktree prune`. After a squash
merge is verified, delete the local branch with `git branch -D <branch>`; `-d` rejects it because
its pre-squash commits are not ancestors of `main`. Never force-remove a dirty worktree or
force-delete a branch without verified integration; report retained work instead. Only clean up
worktrees owned by the current task—other registered worktrees may belong to users or agents
still working.

Completed or merged worktrees should not be retained for convenience. Up to three clean, pushed,
explicitly claimed worktrees may remain only for paused, unmerged tasks that are genuinely likely
to resume. Record why each is retained in its lock reason. Housekeeping must skip every locked or
dirty worktree, and must never infer abandonment from an old timestamp or dead PID alone. For an
unlocked, clean candidate, confirm recoverability and the associated PR state first; because this
repository squash-merges, only a PR reported as `MERGED` with a `mergeCommit` proves integration.
Before removal, unlock a task-owned worktree, remove it from a different checkout, delete its local
branch only after that proof, and prune stale administrative records. If more than three paused
worktrees would remain, ask their owners or the user which tasks to retire rather than deleting the
oldest automatically.

### Cross-platform guardrail

Overcast and its contributor tooling must work on Windows, macOS, and Linux. Treat scripts, hooks,
paths, setup steps, and agent instructions as product-quality cross-platform surfaces. Never
commit a personal absolute path, assume one shell or path separator, or replace a portable workflow
with a platform-only one. Prefer platform-neutral implementations; when shell-specific behavior is
unavoidable, provide and verify equivalent POSIX and PowerShell entry points or a tested platform
dispatcher. See
[CONTRIBUTING.md § Cross-platform development contract](./CONTRIBUTING.md#cross-platform-development-contract).

Before finishing a tooling or documentation change, check every command and path from all three
platforms' perspective. If support cannot be kept equivalent, stop and surface the tradeoff rather
than silently narrowing the supported development stack.

---

## What belongs here vs elsewhere

- **AGENTS.md (this file):** AI-agent-specific workflow rules and guardrails — things that only matter because an agent is executing work autonomously (e.g. "never leave the workspace broken", "run `go vet` before finishing"). If a human contributor wouldn't need to read it, it belongs here.
- **[CONTRIBUTING.md](./CONTRIBUTING.md):** Everything relevant to all contributors, human or AI — coding standards, architecture decisions, checklists, design patterns, web UI conventions. If a human opening a PR would benefit from reading it, it belongs there.
- **[README.md](./README.md):** Everything relevant to users of Overcast — installation, configuration, supported services, quickstart. Not for contributors.

---

## Non-goals — decision guide for agents

Before implementing anything, check these constraints. If a request conflicts, push back.

- **Not a staging environment.** No 100% API parity. Do not base production go/no-go decisions on Overcast tests.
- **Not a security boundary.** Credentials accepted but not validated. Never expose on a public network. `OVERCAST_LISTEN` defaults to `0.0.0.0` when containerised (required for Docker's `-p` publishing) and to `127.0.0.1` natively (#761) — an explicit `OVERCAST_LISTEN` always wins over either default, in both directions.
- **Not a performance testing tool.** No latency emulation, no request-rate limits, no per-service quotas. Lambda concurrency is the one place Overcast does refuse work, in two forms:
  - **Reserved concurrency** — set explicitly per function. Exceeding it throttles immediately with AWS's 429 `TooManyRequestsException` (`Reason: ReservedFunctionConcurrentInvocationLimitExceeded`), because that is behaviour applications are written against: retry policies, DLQs, ESM back-off, and the "set it to 0 to disable a function" idiom.
  - **Instance limits** (`LAMBDA_MAX_INSTANCES`, `LAMBDA_MAX_INSTANCES_PER_FUNCTION`) — host protection, not quota emulation. An invocation that cannot get a container first reclaims an idle one, then queues; **if it is still queued when the function's timeout expires it is throttled** with `Reason: ConcurrentInvocationLimitExceeded`, the same reason AWS gives when the account pool is exhausted. Raise the limits if you are hitting this rather than treating it as an AWS behaviour.

  Asynchronous invocations are never throttled back to the caller: they were already answered 202, so a throttle is retried internally, as on AWS. Do not add account-wide concurrency pools or request-per-second limits.
- **CloudFormation/IAM are partial.** Both services are implemented at a partial level. CloudFormation supports `CreateStack`/`DeleteStack`/`DescribeStacks`/`ListStacks` and provisions 130+ resource types (the `resourceHandlers` map in `internal/services/cloudformation/provisioner.go` is the source of truth — 132 entries at last count, spanning EC2/VPC, API Gateway, AppSync, ECS, EKS, RDS, ElastiCache, EFS, Route 53, IAM, EventBridge, KMS, Lambda, S3, SQS, SNS, DynamoDB, Logs, SSM, SecretsManager, Step Functions and more; full list in `docs/cdk/resource-types.md`) via internal dispatch to the emulated services. IAM supports roles, policies, users, groups, and instance profiles. `cdk deploy` works for stacks using supported resource types. Coverage is not exhaustive — continue ensuring all service implementations remain compatible with CloudFormation (standard ARN formats, required response fields, etc.).
- **Not a production dependency, ever.** Local dev and CI only. No durability guarantees, no security model.
- **Not a perfect replica.** We emulate the most-used 20% with high fidelity. Edge cases may differ.

---

## Repository layout

```
cmd/overcast/main.go         <- unified CLI: `overcast serve` (daemon) + bridge/status/trust subcommands
internal/
  bff/                       <- Go BFF: serves embedded SPA + /api/* proxy routes
  config/                    <- typed env-var config
  hostbridge/                <- mDNS publisher + port-80 reverse proxy (`overcast bridge`)
  router/                    <- chi router, middleware, health + debug endpoints
  middleware/                <- RequestID, Logger, Recovery, SigV4 stub
  protocol/                  <- AWS wire format (XML/JSON errors, ARNs, request IDs)
  state/                     <- Store interface + MemoryStore + SQLiteStore
  serviceutil/               <- shared helpers (request, pagination, validation, logging, lazy init)
  services/
    s3/                      <- 27 service packages; see STATUS.md for coverage
    sqs/
    dynamodb/
    ...
tests/
  AGENTS.md                  <- test conventions (GWT, mocks, helpers)
  helpers/                   <- TestServer, assertions, MockStore
  integration/               <- HTTP-level tests per service
docs/services/               <- per-service landing pages; <key>/operations.md holds the generated support matrix
```

---

## Code quality — quick reference

All coding standards are in [CONTRIBUTING.md](./CONTRIBUTING.md). This section is a lookup table for common agent decisions.

### Error format by service

| Service                    | Format                     | Helper                                    |
| -------------------------- | -------------------------- | ----------------------------------------- |
| S3                         | XML (bare `<Error>`)       | `protocol.WriteXMLError(w, r, aerr)`      |
| Route 53                   | XML (`ErrorResponse` env.) | `protocol.WriteQueryXMLError(w, r, aerr)` |
| SQS, SNS, DynamoDB, Lambda | JSON                       | `protocol.WriteJSONError(w, r, aerr)`     |
| Unimplemented              | Same format as the service | `protocol.NotImplementedXML/JSON(w, r)`   |

501 responses get `x-emulator-unsupported: true` and all responses get a request ID — both automatically. Never set either manually.

### Other rules not to forget

- **State:** all through `state.Store`; JSON serialisation in `store.go` only. Update both implementations when changing the interface.
- **Malformed persisted state must be isolated.** A single corrupt or stale record in `state.Store` must not make list/scan operations, unrelated resources, or the whole service return HTTP 500. When reading many records, skip malformed records and log/track the gap where practical; when reading one named resource, prefer a modeled AWS-style not-found/invalid-resource error if the record cannot be safely decoded. Only return `InternalError` for actual infrastructure failures (store unavailable, query failed, marshal failed), not for one bad persisted payload that can be isolated without breaking AWS-facing semantics.
- **Clock:** `clock.Clock` only — never `time.Now()`. See [CONTRIBUTING § Clock](./CONTRIBUTING.md#time--clock-injection).
- **Client-facing URLs:** mint through `serviceutil.ClientBaseURL` / `ClientBaseURLFromOrigin` (configured hostname, **caller's** port) — never from `r.Host` directly, never from config alone. Two deliberate divergences exist (SQS wire echo, ECR's registry URI) and are commented in place; do not "unify" them without reading [docs/plans/client-facing-url-minting.md](./docs/plans/client-facing-url-minting.md), which records each service's constraints and why.
- **Hostnames that point at a container, not at Overcast** (RDS `Endpoint.Address`, ElastiCache node addresses) follow the same minting rule *and* need something more: the container must answer to that name. **Two different resolvers are involved and picking the wrong one is the classic mistake here** — read [docs/dev/container-networking.md](./docs/dev/container-networking.md) before touching either:
  - `internal/dns` (started by `internal/router/container_dns.go`) answers **"where is Overcast"**. It claims every subdomain of the split-horizon hostnames and answers with Overcast's own address. It is not a general name server for emulated resources, and adding per-resource records to it is not how a container finds another container.
  - **Docker's embedded resolver** (`127.0.0.11`) answers **"where is that other container"**, from the **network aliases** on containers attached to the caller's network — before forwarding anything upstream to `internal/dns`. So a resource name is made resolvable by attaching the container with `docker.Client.ConnectNetworkWithAliases` on *every* network a caller might be on (`LambdaNetwork`, `ECSNetwork`, the VPC network), carrying *every* hostname the name could be minted under (`containerendpoint.ResourceHostnames`).
  - The failure mode when an alias is missing is not a clean `NXDOMAIN`: the query reaches `internal/dns`, which owns the domain and answers with Overcast's address, so the client connects to Overcast on the database port and hangs. `internal/services/rds/endpoint.go` is the worked example.
- **Shared helpers:** use `serviceutil` — see [CONTRIBUTING § Utilities](./CONTRIBUTING.md#shared-utilities--use-serviceutil-never-duplicate).
- **CloudFormation handlers stay thin:** translate CloudFormation properties to the underlying service API, return AWS-shaped physical IDs/`Ref`/`GetAtt`, and encode replacement/delete semantics. Do not duplicate service validation, defaulting, persistence, lifecycle, or execution behavior; dispatch through the emulator router whenever possible. Add CloudFormation-specific validation or error translation only when it makes observable behavior closer to real AWS. See [CONTRIBUTING § CloudFormation integration](./CONTRIBUTING.md#cloudformation-integration).
- **S3 is the final routing fallback, after generated AWS operation ownership.** S3's broad bucket/object routes live on a private router rather than the main chi router. Explicit service routes run first; then the generated AWS operation registry may claim a modeled non-S3 request and return a protocol-correct `501`; only traffic without sufficient non-S3 ownership evidence delegates to S3. The explicit Smithy RPC v2 route similarly delegates to S3 only when `Smithy-Protocol` is absent. This ordering is deliberate because S3 has no distinguishing header or path prefix. Consequences:
  - When you add a service that uses **versioned REST paths** (e.g. `/2018-10-31/...`, `/v3/foo`) or any non-S3 root path, you must (a) register the routes in `RegisterRoutes`, and (b) add the path prefix to `detectService` in [internal/middleware/logger.go](./internal/middleware/logger.go). Otherwise every request to that service will appear in logs as `service=s3` and bypass IAM/region/SigV4 middleware that branches on service name.
  - If you see `service=s3` in logs for a request that clearly isn't S3 (e.g. `POST /2018-10-31/layers/.../versions`), verify both `detectService` and the actual route. The label alone no longer proves the request reached S3.
  - **The operation label needs no code at all.** Once `detectService` names your service, `detectOperation` resolves the operation from the pinned Smithy `@http` bindings via `awsapi.Registry.ClaimRESTQuery`, scoped to that service — see [internal/middleware/restoperation.go](./internal/middleware/restoperation.go). Do not add a method+path switch for it; the generated table already knows your routes, and a hand-written second copy is what let Lambda's label drift into S3's object shapes. The same resolution feeds the IAM action, so a wrong label is a wrong authorization decision, not just a wrong log line. Only an endpoint that exists in **no** AWS model (an emulator-only route) needs an entry, in `overcastRESTOperation`.
  - **Bugs cause fallback too.** A typo in a route path, a missing `RegisterRoutes` entry, a misnamed `chi.URLParam`, or middleware that mutates the URL can make a supported request miss its service handler. Depending on its method, path, and SigV4 scope, the symptom may now be either an S3 response or a generated non-S3 `501`. Confirm the explicit route matched before changing `detectService` or the generated registry. Logging classification, chi route matching, and generated fallback ownership are separate decisions.
  - 501s under `service=s3` for paths like `/<bucket>/?encryption=` or `/<bucket>/?policy=` are real S3 sub-resource calls and belong to S3.

---

## Checklists

The full checklists are in CONTRIBUTING.md:

- [How to add an endpoint](./CONTRIBUTING.md#how-to-add-an-endpoint)
- [How to add a service](./CONTRIBUTING.md#how-to-add-a-service)
- [Service package structure](./CONTRIBUTING.md#service-package-structure)
- [Web UI standards](./CONTRIBUTING.md#web-ui-standards)
- [Writing docs](./CONTRIBUTING.md#writing-docs)

---

## Writing published docs

Everything under `docs/` except `docs/dev/` and `docs/plans/` ships on
overcast.sh. Read [docs/dev/content-charter.md](./docs/dev/content-charter.md)
before writing or editing a published doc — it's one page. The rules agents trip
on most, from a 2026-08 content audit of the live site:

- **One concern per page. No info dumps.** Open with what the reader came for —
  the command, the decision, the answer — then the table or the short list, then
  the exceptions. Exhaustive detail (every flag, every field) belongs in a
  reference table or a `<details>` block, never in paragraphs. Link instead of
  repeating another page. A guide too big for one page becomes a short landing
  page plus one sub-page per concern, mirroring `docs/services/<key>/`;
  splitting a dump into four dumps is not a split. **This applies to a new page
  as much as to a rewrite.** `make docs-lint` fails a page over **6,000
  characters of prose or 12,000 characters of page** — both excluding the
  generated capability block, so a generated operations page passes on its own
  measurements rather than by being named in an exemption list. Going over needs
  a stated reason on the page,
  `<!-- docs-length-review: <why this page is legitimately long> -->`, and the
  marker itself fails if the reason is empty or if the page later comes inside
  the budget.
- **No LLM tells.** "This isn't a proxy — it's a full emulator" corrects an idea
  the reader never held: cut the negated half and say what the thing is. Same
  for "it's not about X", "seamless(ly)", "delve", "it's worth noting that", and
  the three-adjective slogan. `make docs-lint` fails on the fixed shapes and
  prints the line to paste into the allowlist beside the linter when the wording
  is genuinely right.
- **Never cite a file the public site doesn't publish.** No links into
  `docs/dev/**` or `docs/plans/**`, no bare `internal/` Go path standing in
  for an explanation — the audit found this in eleven published files, e.g.
  "(...service-metrics-platform.md)" left in six service docs as an
  unresolvable reference. `go run ./scripts/docs-index.go --check` (part of
  `make docs-check`) rejects both a literal `docs/dev/`/`docs/plans/` path
  fragment and a Markdown link that resolves into either tree.
- **No banned-genre asides.** Build-machinery narration ("fetched at build
  time"), internal editorial-policy explanation ("stays out of the public
  site"), or process narration about the docs team's own review — all true,
  none of it for the reader. Say the behavior, not how it was decided or
  where it comes from.
- **A table cell is not a paragraph.** A cell over ~25 words or with a
  nested clause belongs in prose below the table with its own heading, not
  crammed into the row.
- **Every page ends with `## Related`, and opens with a sentence.** The footer
  is the only route out of a page somebody reached from search, so `make
  docs-lint` requires one on every published page (a directory `README.md`
  aside) and checks that a page's own sub-pages come first and links off the
  site last. It also rejects a first body line that is nothing but a link:
  `Back to [ECS](../ecs.md).` becomes `Symptom, cause and fix for tasks that
  will not start behind [ECS](../ecs.md).`

Editing anything under `docs/services/` means following
[docs/dev/service-doc-template.md](./docs/dev/service-doc-template.md) as well:
a fixed section order, the generated capability block last, and the
per-operation tables on their own `docs/services/<key>/operations.md` page
rather than at the bottom of the landing page. `internal/docslint` fails `make
docs-check` on the structure; the template says what belongs in each section
and which of the page's three readers it is for.

`operations.md`, `limitations.md`, `troubleshooting.md` and `examples.md` keep
fixed meanings and come first in the landing page's `## Related`. A service
directory may also hold **concern pages** — `lambda/concurrency.md`,
`ecs/scheduler.md` — but write one **only when a canonical page would otherwise
exceed the length budget or mix two concerns a reader looks for separately**.
The name is a lowercase hyphenated slug; it may not respell one of the four
(`limitation.md`, `examples-advanced.md` are refused); the landing page must
link it; and its own `## Related` opens with `../<key>.md`. `internal/docslint`
enforces all of it. Material that belongs to a guide rather than to the service
goes under `docs/networking/` or `docs/cli/` and is linked from there instead.

---

## Claiming an issue

Work on this repo runs in parallel. Before writing anything for an issue, claim
it, and check nobody else already has:

```bash
bash scripts/issue-claim.sh 1325
```

That reports any conflict and, if clear, assigns you and sets
`status/in-progress`. In the same edit it removes whichever of `status/ready`,
`status/needs-triage` or `status/blocked` the issue had, because lifecycle
labels are a state machine. Pass `--check` to look without claiming. With no
argument it takes the issue number from the branch name, which is why agent branches are named
`claude/issue-<n>-<slug>`.

**Why a script rather than a habit.** `gh issue view <n>` renders title, body
and comments — not timeline events. GitHub cross-references a pull request onto
an issue the moment the PR body names it, so the strongest available signal is
one `gh issue view` does not show. On 2026-08-22 two agents both worked #1325:
the second opened [PR #1351](https://github.com/overcast-sh/overcast/pull/1351) with
auto-merge armed 1h43m after the first was cross-referenced onto the issue, and
25 minutes after that first PR had already merged. The issue was open,
unassigned and uncommented the whole time. `status/in-progress` existed in the
label taxonomy throughout and neither agent set it — a convention nothing
enforces is not a convention, which is why this is a script wired into a hook.

**A reference is not a claim.** A PR may contribute a piece of an issue, or
cite it for context, without owning it. Only a *closing* reference counts —
GitHub's `closingIssuesReferences`, populated solely by a `Closes`/`Fixes`
keyword. The script keeps the two apart and reports mere mentions as context.

**It warns, it never blocks.** `scripts/hooks/duplicate-work-warning.sh` runs on
`git push` and `gh pr create` and reports a live claim by someone else. Deciding
whether two changes are the same change needs both diffs read, so that call is
yours. If your work genuinely is distinct — a different window, a follow-up, a
piece the other PR left — name the other PR in your body and say what it does
not cover, then carry on.

**If it turns out to be a duplicate**, do not open the PR. Check first whether
your work still applies to `main` at all: restore main's version of the file you
changed (`git checkout origin/main -- <file>`) and run only your new tests. A
test that passes against `main` is not a regression test, it is a duplicate.
Then close the branch and report what you found, including anything of yours
worth keeping as a follow-up.

Being unable to check — no `gh`, no auth, no network — is not evidence of being
clear. The script says so and exits 0 rather than failing a push.

---

## Agent workflow — before and after every task

### Before editing

1. **Claim the issue** — `bash scripts/issue-claim.sh` (see [Claiming an issue](#claiming-an-issue))
2. Identify the **service** and **AWS protocol** (Query, JSON 1.1, REST JSON, REST XML)
3. Locate an **existing implementation** to mirror — copy the pattern, not the temptation to invent
4. Check **config impact** — does it need a new field in `*config.Config`?
5. Check **storage impact** — does it need `state.Store` changes? Both implementations?
6. Check **docs impact** — capability tables, service docs, STATUS.md, changelog fragment under `.changelog/` (never edit `CHANGELOG.md`'s `[Unreleased]` directly — see `.changelog/README.md`)
7. Define a **minimal useful test plan** — failing test first

### Before finishing

1. Run **scoped tests** (`go test -count=1 ./internal/services/x/... ./tests/integration/x/...`)
2. Run **`gofmt -w`** then **`go vet`** over changed packages
3. Run **`make docs`** if you changed capabilities or service behavior
4. Run **`make aws-models-check`** if you changed capabilities, protocol dispatch, generated AWS ownership, or operation routing
5. Verify **no custom endpoints** were introduced — everything must match real AWS wire format. This is mechanically checked, not only self-reported: `go test -tags slim,dev ./tests/integration/router/...` runs `TestAllDeclaredCapabilitiesAreReachable`, which probes every Supported/Partial/Inert/WIP capability at its AWS-modeled wire binding and fails, naming the operation, if none of them reach a handler — the exact fault every finding in [docs/plans/route-reachability-audit.md](./docs/plans/route-reachability-audit.md) was: an implementation mounted on an Overcast-invented path or `X-Amz-Target` prefix, unreachable from any SDK, that this checklist step alone did not catch even once. It runs as part of the normal `-tags slim,dev` test job, so a service addition that repeats that mistake fails CI rather than passing review unnoticed.
6. Verify **CloudFormation handlers** are registered for any new resource types (or stubbed)
7. Widen to `go build ./...` and `go vet ./...` — these work on a bare checkout; see [Generated files](#generated-files) for the two things they don't cover (a real `web/dist` and a real Lambda init)
8. Run **`make lint-go`** (golangci-lint). Build, vet and tests do **not** cover it: CI runs `Lint` as its own required job, and staticcheck findings it reports (unused variables, redundant declarations, merged decl/assign) pass all three of the above. If you touched `web/`, run **`make lint-web`** and **`pnpm run typecheck`** there too. `make lint-web` is `pnpm run lint`, which is **`oxlint .`** — the only linter here. `web/.oxlintrc.json` owns the entire rule set, and new lint rules belong in it. There is no ESLint in this repository (retired in #1330 step 3; the config file's header says why).
9. **Re-check the ground you started from**, immediately before `gh pr create`: `git fetch origin main` and `bash scripts/issue-claim.sh --check`. A fetch at the start of a session proves nothing about `main` at the end of one — see [Claiming an issue](#claiming-an-issue). A `mergeStateStatus` of `DIRTY` on a freshly-opened PR is the same news arriving late; diagnose it with `git merge-tree --write-tree origin/main HEAD` before assuming a lockfile.

If the host has no Go toolchain, use `scripts/docker-go.sh` (Git Bash/macOS/Linux)
or `scripts/docker-go.ps1` (PowerShell) for every `go` command; both work from
linked worktrees. On Windows hosts that block local PowerShell scripts, invoke
the wrapper with `powershell -ExecutionPolicy Bypass -File
scripts\docker-go.ps1 <go-subcommand> ...`. For a `make` target that is only a
Go invocation (for example `docs-lint`), read the target in `Makefile` and run
its underlying `go` command through the wrapper. Do not hand-edit generated
files because the host lacks `go` or `make`. Both wrappers also pin
`GOTOOLCHAIN` to go.mod's toolchain line, so a `go run`-launched tool such as
`make lint-go`'s golangci-lint targets this repo's Go version rather than the
image's, even when the image lags behind.

Both wrappers cap the container at **half the available CPUs** and set
`GOMAXPROCS` and `go test -p` to match, so a long run leaves the user's machine
usable instead of pinning every core. You do not have to add your own `-p`: one
is injected for `go test` unless you passed one, and an explicit `-p` always
wins. Raise or lower it with `OVERCAST_GO_CPUS` / `OVERCAST_GO_TEST_P`, or set
`OVERCAST_GO_CPUS=0` for the old uncapped behaviour — but do not do that on
someone else's machine without asking.

`make check` runs `fmt vet lint test` in one go and is the safest final gate — prefer it over assembling your own subset. Every command CI runs is in [.github/workflows/test.yml](./.github/workflows/test.yml); if your final check is narrower than that file, you have not verified the change.

A `git push` from Claude Code or Codex runs [scripts/verify-changed.sh](./scripts/verify-changed.sh) first (wired as a `PreToolUse` hook in [.claude/settings.json](./.claude/settings.json) and [.codex/hooks.json](./.codex/hooks.json)) and blocks the push if it fails. It scopes to what the branch changed. When a **root-module** `.go` file changed it runs two Go passes: `golangci-lint run ./...`; then `go test -run='^$' -count=1 -tags <set> ./...` under each of the three build-tag sets CI's `build-tags` matrix uses (`slim`, `slim,nosqlite`, `slim,dev`), which compiles every package in the root module *and its test files* under each set while running no test. Both stop at a nested `go.mod`, so a change confined to a compat suite skips them. Then, for **any** `.go` change, it runs `go test -count=1 -tags slim ./...` inside each module that owns one — the root module, or one of the suite modules under `compat/suites/` — which does run their tests. When `web/` changed it runs `pnpm run typecheck` and `pnpm run lint`. The tag matrix is most of the runtime: compiling test binaries costs roughly 3.8× the `go vet` it replaced (12.1s → 46.0s per set on a 5900X, so ~140s for the three rather than ~36s), so budget a few minutes for a Go push. On a Windows-flavoured host (Git Bash/MSYS/Cygwin, or WSL) the scoped test run defaults to `go test -p 2`: left at the NumCPU default (24 on this class of machine), the concurrently executing test binaries exhaust Windows' ephemeral-port range and the network-heavy tests fail with `ADDRINUSE` on a perfectly green tree. That is deterministic parallelism-induced overload, not test flake — re-running the push does not help, capping does. The tag-matrix compiles stay uncapped (they bind no sockets, and `-p` bounds build actions too). An explicit `OVERCAST_GO_TEST_P` overrides the default and applies to both steps, with `0` meaning uncapped everywhere. It is still a backstop, not a substitute for running the checks yourself — the only tests it runs are those of the modules you edited, under one tag set, which is far narrower than CI's suite — and it stays out of the way (exit 0, with a warning) when a toolchain is unavailable. Run it directly any time: `make verify` (or `bash scripts/verify-changed.sh`).

It will not re-run a check you have already passed. Each check's result is cached in the git dir against a content fingerprint of that check's own inputs — the files it reads (`.gitignore` honoured, working-tree content, staging state irrelevant) plus the command and pinned tool version, so a hit only ever stands in for a like-for-like run. Touching `web/` therefore does not invalidate the Go result, and pushing twice with nothing edited in between costs seconds instead of minutes. `make lint-go` records its pass the same way, so running it by hand makes the push free. Use `make verify ARGS=--force` to check anyway. The scoped test run is the exception: it is not fingerprinted and runs on every push that touched a `.go` file.

### Self-review the diff before committing or pushing

Green checks prove the code compiles and passes tests. They say nothing about whether the diff is the change you meant to make. **Read your own diff end to end before every commit, and read the whole branch diff before every push.**

```sh
git diff                    # unstaged
git diff --staged           # exactly what the commit will contain
git status --short          # untracked files you may have forgotten or never meant to add
git diff main...HEAD        # the whole branch, as the reviewer will see it
git diff --stat main...HEAD # scan for files you did not expect to have touched
```

Read it as the reviewer, not as the author: judge each hunk on whether it earns its place in *this* change, not on whether you remember writing it. If you cannot say why a hunk is there, it does not belong — revert it or split it out.

**This matters most in long sessions.** The longer a session runs, the more the branch diverges from anything you still hold in context: approaches tried and abandoned, debugging aids added under pressure, a helper written twice because the first one was forgotten, a file edited early against assumptions that later changed. None of it fails a build. All of it lands in the PR unless you look. Do a full self-review pass before the first commit of a long session even if you have been careful throughout — after several hours of churn your memory of the branch is a summary, and the diff is the fact.

Check for, at minimum:

- **Debug leftovers** — stray `fmt.Println`/`log.Printf`, `console.log`, commented-out code, `t.Skip`, hardcoded endpoints or credentials, temporary fixtures, scratch files, capture harnesses under `web/public/`, a `-run TestOne` narrowing left in a Makefile target.
- **Dead ends** — code from an approach you abandoned, now unreferenced. Helpers with no callers, config fields nothing reads, a test for behaviour that no longer exists. `go vet` and golangci-lint catch some of this; they do not catch an exported function nobody calls.
- **Accidental duplication** — a helper you wrote in service A that already existed in `serviceutil`, or that you wrote twice under two names in the same branch. Long sessions are where this happens.
- **Churn that nets to nothing** — files that appear in the diff only as reordered imports, reflowed comments, or a change made then substantially undone. Revert them; they cost the reviewer attention and prove nothing.
- **Unrelated edits** — changes to files outside the task, and other agents'/the user's uncommitted work swept in by `git add .`. Stage explicit paths. See the [`commit` skill § Staging Discipline](./.agents/skills/commit/SKILL.md).
- **Stale comments and docs** — a comment describing what the code did two iterations ago is worse than no comment. Same for a plan doc under `docs/plans/` that must be accurate as of this commit, a service doc's *both* tables, and STATUS.md.
- **Completeness against your own claims** — every `state.Store` change in both implementations, every new resource type registered in `provisioner.go`, the changelog fragment under `.changelog/`, the test that was supposed to fail first. Re-read the task and the [Before finishing](#before-finishing) list against the diff, not against your recollection.
- **Commit coherence** — if the branch has grown several unrelated reasons, split it into commits that each stand alone rather than pushing one commit that does four things.

**A review that finds something after the commit is not too late — amend.** Reviewing before you commit is the ideal, but a commit is cheap to correct while the branch is still yours, so never let "I have already committed it" become the reason a leftover ships. Fix it and `git commit --amend`, which is what [What agents must NOT do](#what-agents-must-not-do) already asks for mistakes you find in your own work: amending is fine even after pushing, on a branch you own that nobody shares, with `git push --force-with-lease`. Use a follow-up commit instead once the change is merged, the branch is genuinely shared, or unrelated commits sit on top — and note that a PR with auto-merge enabled can merge underneath you mid-task, which turns an intended amend into a follow-up whether you like it or not. A rejected `--force-with-lease` is that situation announcing itself; re-fetch and look before overriding it. What is never acceptable is committing something you know does not belong and leaving it for the reviewer, or narrating it as a known issue in the PR body.

For a substantial or long-running branch, run the [`code-review` skill](./.agents/skills/code-review/SKILL.md) over the diff before pushing — it applies the full AWS-parity, regression-risk, and maintainability checklists that a quick read-through will not. Delegating the read to a sub-agent is a good use of one: it reviews the diff without the anchoring bias of having written it. Fix what it finds before the push, not in a follow-up commit.

---

## Common mistakes

Agents most often trip on these — check before finishing:

- **Committing without reading the diff** — green checks do not tell you the diff is the change you meant to make. See [Self-review the diff](#self-review-the-diff-before-committing-or-pushing)
- **Creating non-AWS endpoints or custom response fields** — the AWS SDK must work unmodified
- **Inventing a path** — every route is either a binding the pinned manifest models (copy the `URI` from `internal/awsapi/manifest.gen.go`) or lives under `/_overcast/`. Never nest an invented sub-resource inside a modeled prefix: `/2015-03-31/functions/{name}/source` reads as an AWS binding, is not one, and collides the day AWS models it. `TestNoRouteIsRegisteredOutsideTheNamespace` fails the build on either mistake — see [CONTRIBUTING.md § How to add an endpoint](./CONTRIBUTING.md#how-to-add-an-endpoint)
- **Changing wire formats without tests** — request/response shapes are the compatibility contract
- **Forgetting `make docs`** after capability changes — generated tables will drift
- **Forgetting `make aws-models-check`** after capability or operation-routing changes — the AWS operation coverage CI job will fail
- **Updating only one store implementation** — `MemoryStore` and `SQLiteStore` must stay in sync
- **Forgetting CloudFormation resource handlers** — every resource-creating endpoint needs an entry in `provisioner.go`
- **Using `time.Now()` instead of `clock.Clock`** — makes tests untestable
- **Bypassing `serviceutil` / duplicating helper logic** — DRY across services
- **Returning bare `404`** — unimplemented operations must return `501`
- **Using subfolders as sub-packages inside a service** — all service files live in one flat package
- **Hand-rolling a `<Table>` in the web UI when `ResourceTable` fits** — any list of resources goes through `ResourceTable` (index pages via `ResourceListPage`/`ResourceListSection`, detail pages via `variant="embedded"`); a bespoke table needs a reason in a comment at the call site. See [CONTRIBUTING § Tables](./CONTRIBUTING.md#tables--reach-for-resourcetable-before-composing-table-yourself) and #1327
- **Testing only with raw HTTP** — prefer AWS SDK clients for management-plane validation where possible
- **Reaching for a docs regeneration step after editing `docs/`** — there isn't one any more. The console derives its docs navigation and search index from the embedded docs at runtime ([internal/docsindex](./internal/docsindex/docsindex.go)); `make docs-lint` checks the docs themselves
- **Adding a compat test to one suite only** — every SDK/CLI suite tests the same operations; add to `compat/suites/registry.json` first, then implement everywhere. `go run ./cmd/compat --check-parity` fails the build otherwise. See [compat/AGENTS.md § Baseline & uniformity policy](./compat/AGENTS.md#baseline--uniformity-policy)
- **Leaving the changelog question unanswered** — a PR that adds no fragment under `.changelog/` fails the `Changelog entry` check until someone says the omission was deliberate. Add the fragment, or comment `/no-changelog <reason>` on the PR (`gh pr comment <number> --body '/no-changelog test-only: new fixtures for the SQS suite'`). The reason is required and is kept; `/needs-changelog` puts the question back. A PR whose every file is in an area that never ships (`compat/`, `cmd/compat/`, `tests/`, test files anywhere, `docs/plans/`, `docs/dev/`, `.agents/`, `.claude/`, `.vscode/`, `.devcontainer/`, contributor docs) is passed without a word — the authoritative list is `EXEMPT_*` in [scripts/changelog-required.py](./scripts/changelog-required.py), and a single file outside it puts the whole PR back in scope. **`scripts/` is not exempt as a directory**, and deliberately so: it holds things that do change shipped output, `docs-index.go` and the release scripts among them. Only `scripts/*_test.py` is exempt, by suffix. So a PR adding a developer-only script still has to answer the question — comment `/no-changelog tooling-only: …` and post it *before* you start waiting on checks, or `pr-wait.sh --fail-fast` will trip on a gate that is about to waive itself. Never edit `CHANGELOG.md` to clear it — that file belongs to the release PR, and a second hand in it aborts the bot's merge and stops the release PR refreshing itself. While a release PR is open just add the fragment; the bot folds it in for you. See [.changelog/README.md § When a change needs no fragment](./.changelog/README.md#when-a-change-needs-no-fragment)
- **Merging a lockfile PR whose base has moved** — `web/pnpm-lock.yaml` is regenerated, never merged: its peer-resolution keys refer to each other, so two copies regenerated independently combine into a lockfile that fails `pnpm install --frozen-lockfile` even though git reports no conflict (#1340 — every PR red for 28 minutes). The `Lockfile freshness` check fails a PR whose lockfile was generated against a `main` that has since changed its own, and goes red on open PRs the moment such a push lands. The fix is always the same: rebase (or merge `main` in), run `pnpm install` in `web/`, push. Never resolve it by merging the two texts. Renovate's PRs rebase themselves
- **Leaving a compat test failing** — the baseline is at zero failures and CI enforces that absolutely: *any* failing test fails the build, as does a result that gets worse than `compat/baseline/` (one JSON shard per suite) records. Never hand-edit the baseline to record a fix; improvements are promoted automatically on `main`

---

## Generated files

[docs/dev/generated-files.md](./docs/dev/generated-files.md) is the full inventory — every generated artefact, whether it is committed, build output, or derived at runtime, and why. The short version:

These generated sources are **committed** and must be regenerated through their owning command:

| File | Regenerate with |
| --- | --- |
| `internal/capabilities/all.gen.go` | `make generate-caps` |
| `web/src/types/api.gen.ts` | `make generate-ts` |
| `web/src/routeTree.gen.ts` | `pnpm dev` / `pnpm build` (the TanStack Router vite plugin writes it) |
| `internal/awsapi/manifest.gen.go` | `make generate-aws-operations` |
| `internal/awsshapes/index.gen.go` and `internal/awsshapes/tables/*.txt` (runtime SDK shape tables) | `make generate-aws-operations` |
| `docs/README.md` service index, `docs/services/<key>/operations.md`, `docs/generated/service-support.json` | `make docs` |
| `internal/services/dynamodb/reserved_words.txt` | `make generate-ddb-reserved-words` (needs network; no CI gate on its freshness) |

There is **no docs index to regenerate**. `web/src/docs-nav.gen.ts` and
`internal/docssearch/index.gen.jsonl` used to be here; both were sorted
manifests with one entry per page, so every docs pull request rewrote part of
them and two concurrent docs branches conflicted on a file nobody had written
by hand. They are derived at runtime now, from the docs the binary already
embeds — see [internal/docsindex](./internal/docsindex/docsindex.go).

- **After changing a Go response struct the web UI consumes, run `make generate-ts` and commit the result.** `web/src/types/api.gen.ts` is rendered by [cmd/tsgen](./cmd/tsgen/main.go) from the structs listed in its manifest (`/_overcast/health`, `/_overcast/metrics`, `/_overcast/debug/metrics`, the SSE envelope, the topology graph, the inbox, the request-tracing payloads, …); `make check-ts` — part of `make docs-check`, and `go test ./cmd/tsgen` — fails when the committed file is stale. Never write a server type by hand in `web/src/types/common.ts`; to expose a new one, add it to the manifest. A struct that refers to a type the manifest does not list is an error naming the field, so the generated set grows only on purpose; a type with its own `MarshalJSON` is refused too — name the package-level struct the method encodes instead (`trace.Entry` → `trace.entryJSON`).
- **After editing a published doc under `docs/`, run `make docs-lint`.** Nothing to regenerate and nothing to commit beyond the doc — the check is on the doc itself: frontmatter, in-page anchors, the service page template, the 220-character description budget, the page length budget (6,000 prose / 12,000 page characters) and the house-style tells.
- **`docs/plans/` and `docs/dev/` are neither published nor indexed.** [internal/docsindex](./internal/docsindex/docsindex.go) skips both directories outright, as `embed.go`'s pattern does, so they are invisible to the console. They are working documents, not user-facing pages — and a published doc may not cite them.
- **A published doc *is* a build input.** `embed.go` compiles `docs/*.md`, `docs/cdk` and `docs/services` into the binary and the console indexes them at runtime, so `internal/docsindex`'s tests assert on the real corpus (search rankings included). Editing one runs the full CI suite; editing `docs/plans/` or `docs/dev/` does not.
- **Never hand-edit or hand-merge generated files.** Regenerate `internal/awsapi/manifest.gen.go` with `make generate-aws-operations`, using an `api-models-aws` checkout at the revision pinned in `models/aws/VERSION` and setting the `AWS_MODELS_DIR` and `AWS_MODELS_REVISION` variables required by the target. `go run ./scripts/aws-models.go --ensure` produces that checkout for you — see below.
- **Reproduce the AWS operation coverage CI job with `make aws-models-check`.** It validates, without network access: the committed manifest and its runtime ownership indexes; protocol identifiers; that every declared operation name is real in AWS — **including a `DocOnly` row named like an operation**; that a REST-bound operation is **registered at the HTTP method and URI the model binds it to**; that **the service that registered a route is one the model actually gives that path to** — the third route-side axis, between the declaring service's own bindings and "is this path modeled by *someone*" (`internal/router/routeownership_dev_test.go`, #1227); that an operation is reachable over every protocol its service answers on; that a row declaring Supported is one a client can actually call; and that the wire facts a service states about itself agree with the model — its `TargetPrefix()`, its `PathPrefixes()` claim, and the Query API versions and service aliases in `internal/awsapi`. With a pinned checkout, set `AWS_MODELS_DIR` to add the same byte-for-byte regeneration check used by the scheduled model-refresh workflow.

  **`go run ./scripts/aws-models.go --ensure` fetches that checkout for you**, into a per-user cache keyed by revision (`os.UserCacheDir()/overcast/aws-models/<revision>`) rather than a gitignored directory inside the repo — this project's workflow puts every task in its own worktree, and a fetch per worktree would mean several copies of a large third-party tree. It reads the revision to fetch from `models/aws/VERSION`, never "latest", so the cache can never go stale: it is either present or fetched, and re-running the command with a complete cache touches no network. `--service <name>` (repeatable, comma-separated too) does a sparse fetch instead of the whole corpus, widening an existing sparse checkout the same way a plain `git sparse-checkout set` would; omit it for the full checkout `aws-models-check`'s byte-for-byte diff needs. `--prune` deletes cached revisions other than the one `models/aws/VERSION` currently pins. It composes on stdout — everything else it prints goes to stderr:

  ```sh
  AWS_MODELS_DIR="$(go run ./scripts/aws-models.go --ensure)" make aws-models-check
  AWS_MODELS_DIR="$(go run ./scripts/aws-models.go --ensure --service acm)" make generate-aws-operations
  ```

  `AWS_MODELS_DIR` has no Makefile/Taskfile default: `aws-models-check` with it unset is the deliberate no-network gate CI's `aws-operation-coverage` job runs, and defaulting the variable to a `go run` invocation would turn every such run into a network fetch (cached, but still a behavior change from "offline gate" to "fetch first"). Compose it explicitly as above instead. `AWS_MODELS_REVISION` already defaults from `models/aws/VERSION` in the Makefile, so only `AWS_MODELS_DIR` needs supplying.

  **An exemption is deleted when its reason stops being true, not reviewed.** `capgen --check-model` fails on a stale entry in `capabilityManifestExemptions`, `capabilityOperationAliases` or `compatRegistryServiceExemptions`, and on a ledger row naming a fault that has been fixed. Nine of the fourteen manifest exemptions were false when that check was written. Never widen an exemption to make a gate green — that is the mechanism #864 was filed about.

  **It now checks that an operation is served where AWS serves it — #864 closed that gap.** This bullet used to claim "router coverage" and not have it: the only model consultation was a name-only `awsapi.HasOperation`, and the router test it named registers only S3 and passes a `501` as its success condition, so an operation implemented at an invented path was indistinguishable from one deliberately unimplemented. That is how EventBridge Scheduler stayed unreachable for 33 releases and seven more services were found the same way (see `docs/plans/route-reachability-audit.md`). The manifest's `HTTPMethod` and `URI` are now compared against the registered routes, by `internal/router/modelbinding_dev_test.go`.

  **What it still does not check is request *shape*.** The manifest carries the `@http` binding and no member shapes, so a member AWS binds to the query string can be read from the body and nothing here will object. That is the second half of #793, and it needs the raw pinned models rather than the manifest (#883, blocked on #884). It also cannot prove a *handler's* branch on a query-discriminated path — only that the classifier names the right operation. See [docs/plans/manifest-enforcement.md](./docs/plans/manifest-enforcement.md) for that boundary and for the fallout ledgers.
- `.gitattributes` marks generated sources `linguist-generated`, so GitHub review collapses them.
- A bare `git clone` builds: `go build ./...` and `go vet ./...` need no generation step.

### The three things you may still have to build

Three artefacts are embedded in the binary but deliberately not committed, because all three are build output: the SPA, the in-container Lambda init, and the packed aws-sdk shape tables. Each keeps a committed `.gitkeep` placeholder so its `//go:embed` pattern always resolves, which is what makes "a bare `git clone` builds" true of all three.

#### `web/dist` — the SPA

`embed.go` has `//go:embed all:web/dist`, and the SPA is build output, so it is *not* committed — only a `web/dist/.gitkeep` placeholder is, which keeps the embed pattern resolving. Consequences:

- Go builds always compile. A binary built without an SPA serves the API normally and returns a 503 naming `make build-web` on the web UI port — that response is the symptom, not a bug to investigate.
- Anything that must actually serve the UI needs `make build-web` first. `make ci-local-go`, the Docker build, and release builds all assert a real `web/dist/index.html` and fail loudly without one.
- Backend-only work never needs it: `go vet -tags slim ./...` skips the UI entirely.
- `-tags slim` also removes real routes, not just the UI — `/_overcast/mcp` is registered only in `!slim` builds. A test that exercises one of those surfaces must carry the same build constraint, or it fails under `-tags slim` and looks like a routing bug when it isn't. See [tests/AGENTS.md § Build-tag-sensitive tests](./tests/AGENTS.md#build-tag-sensitive-tests--guard-the-test-like-its-subject).
- **A bare check cannot see tag-gated code at all, and that cuts the other way.** `golangci-lint run ./...`, `go vet ./...` and `go test ./...` all use the default build context, so nothing you run without a tag ever compiles a file behind `//go:build dev` or `nosqlite` — every `*_dev.go`, `internal/capabilities`, `internal/mcp`, and each service's `capabilities_dev.go`. A syntax error in one passes every local check and fails only in CI's `Test suite (-tags slim,dev)`. `make verify` now vets all three of CI's tag sets for you; if you are checking by hand, `go vet -tags slim,dev ./...` is the one most often needed, because capability declarations live behind `dev`. Setting `build-tags` in `.golangci.yml` is not a shortcut — those files come in `//go:build !dev` pairs, so naming the tag drops the other half and moves the blind spot instead of closing it.

#### `internal/services/lambda/initbin/dist` — the Lambda init

`internal/services/lambda/initbin` has `//go:embed all:dist`, and the in-container Lambda init ([cmd/lambda-init](./cmd/lambda-init)) is build output, so the two binaries are *not* committed — only `internal/services/lambda/initbin/dist/.gitkeep` is. Build them with **`make lambda-init`** (`task lambda-init` on Windows), which cross-compiles `linux/amd64` and `linux/arm64` with `CGO_ENABLED=0 -trimpath -ldflags "-s -w"`. Consequences:

- A bare checkout still builds, vets and tests. `go build ./...` and `go vet ./...` need nothing from you. `go test ./...` needs the artefacts, and the test bootstraps take care of it: `internal/services/lambda`'s `TestMain` and `tests/helpers/lambdafixture`'s `EnsureInit` (which `tests/integration/lambdadocker`'s `TestMain` calls) build anything missing once per test binary — and, because an embed is baked at *compile* time and a fresh checkout's test binary was compiled before the artefacts existed, they point `initbin.EnvDistDir` (`OVERCAST_LAMBDA_INIT_DIST`) at the freshly built files so the very first `go test` passes, not the second. That env override is also how to run a locally patched init without relinking Overcast; unset, production reads only the embed.
- **`-tags slim` does not drop it.** Slim images run Lambda, so the init is embedded in every flavour. Do not reach for a build tag to shrink it.
- Both architectures are always embedded (~13 MB). A function's `Architectures` picks which one is copied into its container, and an `arm64` function runs under emulation on an amd64 host, so neither can be assumed away at build time.
- `make build`, `make build-slim` and every cross-build target depend on `lambda-init`, and the Dockerfile builds it in the shared Go stage before either binary — so the released artefacts always carry a real init. `//go:embed` reads the tree at compile time: a build that skips the step compiles perfectly well and then fails at the first Lambda invoke that needs it.
- That failure is loud and actionable by design, never a silent fallback: `initbin.For` names the missing file and the command that produces it.

#### `internal/awsshapes/dist` — the aws-sdk shape tables

Step Functions' `aws-sdk:` integrations read one Smithy shape table per service. The tables are **committed as text** (`internal/awsshapes/tables/*.txt`, regenerated by `make generate-aws-operations`) so a model refresh reviews as a readable diff, but that text is not what the binary embeds: **`make aws-sdk-shapes`** (`task aws-sdk-shapes`; it runs `go run ./cmd/awsshapes-pack`) gzips it into `internal/awsshapes/dist/`, which is what `//go:embed all:dist` picks up — ~0.5 MB per binary rather than ~4 MB. Consequences:

- A bare checkout still builds, vets and tests. The three test packages that exercise the tables (`internal/awsshapes`, `internal/services/stepfunctions`, `tests/integration/stepfunctions`) point `OVERCAST_AWS_SDK_SHAPES_DIR` at the committed text in their `TestMain` (`internal/awsshapes/awsshapestest`), so the first `go test` passes. A new test package that runs `aws-sdk:` Tasks needs the same one-line `TestMain`.
- Every build target, the Dockerfile and each CI job that builds a binary run the pack step first. It is deterministic and rewrites only changed files, so it costs well under a second and never invalidates a build cache.
- A build that skips it compiles and then fails its first `aws-sdk:` Task with `States.Runtime`, naming the missing file and the command — the same loud failure as a missing Lambda init.
- After regenerating the tables, rerun `make aws-sdk-shapes`: `internal/awsshapes`' `TestDist_matchesTheCommittedTables` fails on a packed copy that no longer matches the committed text.

---

## Resolving a decision — when behaviour is unclear, or options are all defensible

This ladder settles two different situations. The obvious one is not knowing how AWS behaves.
The more common one is having **several reasonable options** — a name for a derived resource, a
default for an unset field, error-vs-no-op on a missing resource, the order side effects fire in
— none of which feels like a compatibility question at the time. It is one. Ask **what does AWS
do here, in this service, in this case** before you pick, then:

1. Prefer **real AWS behaviour** — spin up a resource and test the edge case
2. Then **existing Overcast behaviour** — consistency within the codebase matters
3. Then **compatibility test expectations** — what do tests in `tests/` and `compat/` expect?

This matters more for you than for a human contributor: you resolve forks like this constantly
and silently, and a choice you never noticed making is one nobody gets to review. Where the
answer is cheap to look up, look it up before deciding rather than defending the guess later.
Where it is expensive and the fork is genuinely load-bearing, **say which way you went and why**
in the PR body — see [CONTRIBUTING § AWS is the tie-breaker](./CONTRIBUTING.md#aws-is-the-tie-breaker).

If a task would require broad architectural changes, **stop and surface the tradeoffs** rather than refactoring across services silently. A `501` with an honest explanation is better than a divergent `200`.

---

## Release awareness

Changes merged into `main` do **not** imply a stable release. Docker images are only published when a release tag is pushed. (Athena's engine image is the one exception: it is published from `main` — see [RELEASE.md](./RELEASE.md).) Do not assume code on `main` is available to end users — treat it as nightly/integration until tagged.

---

## Working efficiently

`go build ./...` and `go vet ./...` over the whole repo is slow. Go's build cache means **only changed packages recompile**, so scope verification to what actually changed:

```sh
# After touching internal/services/foo/ — build and vet only that subtree
go build ./internal/services/foo/...
go vet  ./internal/services/foo/...

# Or run its tests (test compilation implies vet, -count=1 avoids cached results)
go test -count=1 ./internal/services/foo/... ./tests/integration/foo/...
```

Widen to `./...` only once before marking a task done. Avoid `go build ./cmd/overcast` during iteration — the `cmd/overcast` main package embeds the web UI, adding unnecessary overhead for backend-only changes. Use `./cmd/overcast -tags slim` or stay within `./internal/...` until the final check.

`-tags slim` also drops the embedded SPA, so backend-only verification never needs `make build-web` — see [Generated files](#generated-files).

For TypeScript changes, run `pnpm run typecheck` in `web/`. Do not use `tsc --noEmit` directly: it resolves `web/tsconfig.json`, a solution-style config with `"files": []` and only project references, so it compiles zero files and always passes.

### Iterating on a specific test

When fixing a single handler or function, run only the relevant test rather than the full package suite:

```sh
go test -count=1 -run TestMyFunction ./internal/services/foo/...
```

To compile and vet without executing tests (fast syntax/type check):

```sh
go test -run=^$ -count=0 ./internal/services/foo/...
```

### Run `gofmt` before `go vet`

`go vet` can emit misleading output on unformatted code. Always format first:

```sh
gofmt -w ./internal/services/foo/
```

Or to check without writing: `gofmt -l ./internal/services/foo/`

### Check the editor error panel (workspace problems) before running a build

The language server surfaces compile and vet errors without a terminal round-trip. Use the `get_errors` tool to read the current problem list — if it's empty for the files you changed, a scoped `go vet` is usually sufficient confirmation.

### Use the `Explore` sub-agent for read-only investigation

When you need to understand an unfamiliar part of the codebase (e.g. "how does the SQS handler parse queue URLs?"), delegate to the `Explore` sub-agent rather than chaining many sequential searches in the main conversation. It returns a focused summary and keeps your context clean for implementation work.

---

## Waiting on CI — `scripts/pr-wait.sh`, never a poll loop

After opening a PR, enable auto-merge and then wait with **one** command:

```sh
gh pr merge <n> --squash --auto
scripts/pr-wait.sh <n>            # or scripts\pr-wait.ps1 <n>
```

It wraps `gh pr checks --watch --fail-fast` and exits 0/1/2/8 — passed / failed
/ not worth acting on / still pending. **How** to run it is a fork on one
question, answered below. Four things it does for you:

- **Returns at the first failure**, rather than waiting out the rest of a
  doomed run.
- **Brings the evidence.** On failure it fetches each failing job's annotations
  and the tail of its failing step, so the first thing you read is the error
  itself, not a URL to go and fetch. Output is capped so a pathological failure
  cannot bury the conversation.
- **Is re-runnable.** After you push a fix, run it again and it waits on the new
  head's run. If the head moves mid-watch it says so and exits 8, because the
  result it just collected is stale.
- **Checks the conflict at both ends.** A PR that is conflicting before the wait
  never gets watched (exit 2). One that goes conflicting *during* it — `main`
  moved underneath, which changes no check and no head SHA — is caught at the
  end and also exits 2, rather than reporting a green run on a PR that will now
  never merge.

**How to run it: a fork on one question.** The two answers below are complete
rules. Follow the one whose condition you meet and ignore the other — neither
is a refinement of the other, and whichever you read second does not win. The
question is not *who you are*, it is:

> Can anything wake you after this turn ends?

- **Yes — a human is watching the conversation.** Run it in the **background**,
  so you stay interruptible while it runs and its one completion notification
  lands in front of the person actually waiting for it. This is the common case
  and the default.
- **No — your turn ending is final.** Do not background it and stop. A stopped
  **subagent** is the example: it is re-invoked only while a live tracked
  background child is still running, and `pr-wait` routinely exits in seconds —
  a bad argument, a `gh` auth hiccup, a PR whose checks had already settled, its
  own exit 2. The child is dead before the turn ends, the wake never comes, and
  the agent is stranded mid-task owing a report it will never send (eight of
  them, in one session, on 2026-08-22). Run it in the **foreground** instead —
  `--watch` has its own timeout, so it is bounded — or, once
  `gh pr merge <n> --squash --auto` is armed, skip the wait entirely: there is
  nothing to wait for, because auto-merge completes the PR unattended. The
  terminal state is then *auto-merge armed, one foreground `gh pr checks <n>`
  shows no FAILED required check → report and exit*.

Subagent is the example of the second case, not the test for it. The test is the
wake question, and neither answer licenses the poll loop below.

Do **not** write a `while` loop over `gh pr checks --json` on a `sleep`
interval. It costs a request per tick, fires whether or not anything changed,
and produces a play-by-play of "N passed" messages that change no decision.
`gh pr checks` already has `--watch`. If you are typing `sleep` next to
`gh pr checks`, use the script instead.

Never pipe an exit-code-bearing `gh` command into another — the pipeline reports
the last command's status, which is how [#410](https://github.com/overcast-sh/overcast/pull/410)
merged over a failing compat check. Full rationale and the surrounding
corollaries are in the [`pull-request` skill § After Opening](./.agents/skills/pull-request/SKILL.md#after-opening--waiting-on-ci).

## Merging — no `--admin`, ever

Merge with plain `gh pr merge --squash` (or `--rebase`). The main ruleset now
enforces what used to be convention: required status checks, PR-only changes,
linear history — and it has **no bypass actors**, so `--admin` cannot skip
anything and must not be attempted. A quarantine PR under the compat flake flow
still merges normally, but **not** for the reason this paragraph used to give:
"Aggregate Compatibility Results" *is* a required check now. It merges because
the flaky-list lint reads the `quarantine-approved` label live from the pull
request, so once a reviewer applies it the lint passes, the aggregate goes green
and the required check is satisfied. Before the label it is red and the pull
request is blocked — which is the point of the label, not a bug.

While `VERSION` on main names an **untagged** version (a release is pending or
a release workflow has failed), merge only changes needed to get that release
out — `scripts/release-candidate-check.sh` printing `true` is the definition of
this window. Publishing itself always waits for the maintainer's approval of
the `release` environment.

Run it on `main`, or on a branch that has not touched `VERSION`. The script
reads the *checked-out* `VERSION`, so on a release branch it prints `true`
because that branch carries the new version — which says the branch is a
release candidate, not that the window is open. Conflating the two is what put
the wrong changelog-gate comment on
[#563](https://github.com/overcast-sh/overcast/pull/563).

**Never enable auto-merge on a release-prep PR.** Do not run `gh pr merge --auto`
against a PR that changes `VERSION`, and do not enable auto-merge on it through
the web UI. Auto-merge is fine for ordinary PRs, where letting the required
checks gate the merge is exactly the point. A release-prep PR is different: its
merge to `main` is what opens the release window and starts the release
workflow, and it should happen only after a human has smoke tested the RC image
that the PR itself published (see [RELEASE.md](./RELEASE.md) § Creating An Alpha
Release, step 6). Green checks say the build is sound; they do not say the
release is ready. Merge it as a deliberate, separate step.

This is a merge-timing rule, not a publishing safeguard — the `release`
environment's required reviewer already means nothing ships without the
maintainer approving the exact SHA.

## Stacked pull requests

When your work needs code from a PR that has not merged yet, stack on it rather
than duplicating the code or waiting: target that PR's branch instead of `main`.
GitHub understands stacks natively — it runs CI on **every** layer, not just the
bottom, and rebases and retargets the branches above when the bottom merges.

Build one with the `gh stack` extension (`gh stack init` / `add` / `submit` /
`sync`), turn existing branches into one with `gh stack init branch1 branch2`,
join **already-open** PRs with `gh stack link` (remote-only — it rebases and
force-pushes nothing, so it is safe against another agent's branch), or open a
PR against another PR's branch in the web UI. REST, GraphQL and webhooks expose
the stack for automation. Not supported in GitHub Desktop.

Four rules, each of which has cost this repo a recovery:

- **Merge bottom-up**, and **never `--delete-branch` a base that has children** —
  it closes the child PR and deletes its head branch, and a closed PR cannot be
  retargeted.
- **Never rebase or force-push a branch you do not own while its PR is open.** If
  a lower PR conflicts with `main`, its owner fixes it; ask rather than fixing it
  for them.
- **After anything in the stack merges, `gh stack sync`** (or fetch and reset to
  the remote) — GitHub has already rebased the branches above, so a local
  `git rebase` replays commits the server dropped.
- **A PR in a stack cannot be merged with `gh pr merge`.** Both it and the plain
  REST merge endpoint answer `403 Merging stacked PRs via this endpoint is not
  supported`. Use the asynchronous endpoint and poll the UUID it returns:

  ```sh
  gh api -X PUT repos/overcast-sh/overcast/pulls/<n>/merge-async -f merge_method=squash
  gh api repos/overcast-sh/overcast/pulls/<n>/merge-async/<uuid>   # until "status":"merged"
  ```

- **A PR showing no checks at all is `CONFLICTING`, not queued** — GitHub
  dispatches no workflows on a conflicting PR. Check **`mergeable`** before
  waiting on CI, not `mergeStateStatus`: `CONFLICTING` is a value of `mergeable`
  (MERGEABLE / CONFLICTING / UNKNOWN) and is not a member of the
  `MergeStateStatus` enum at all — that field spells a conflict `DIRTY`.
  `pr-wait` guarded on the wrong one for months and never fired.

  ```sh
  gh pr view <n> --json mergeable,mergeStateStatus
  # a conflicting PR: {"mergeable":"CONFLICTING","mergeStateStatus":"DIRTY"}
  ```

  `mergeable` is `UNKNOWN` until GitHub finishes computing it — asking is what
  schedules the computation — so look again after a second rather than reading
  `UNKNOWN` as "fine".

Full workflow, including the generated-file conflict recipe, is in the
`stacked-prs` skill.

## Reserved ports — 4566 and 4567 belong to the user

Agents must **not** start their own test instances of Overcast on port **4566** (API) or **4567** (web UI) — those are reserved for the user's own running instance, unless the user explicitly directs otherwise. Starting a test instance on those ports silently breaks whatever the user is doing with their instance, or fails confusingly when theirs is already bound.

- Use `scripts/run-test-instance.sh` (or `.ps1`) — it picks a free port pair at or above 4570, refuses 4566 and 4567 in either role even when the scan base is moved, publishes both ports to `127.0.0.1` only, and prints the API endpoint and web UI URLs.
- **It takes named options, and has no `docker run` passthrough.** `--image`, `--base-port`, `--name`, `--env KEY=VALUE` (limited to `OVERCAST_*` and `AWS_*`), `--data-volume`, `--mount-docker-socket`, `--no-logs`. Anything else stops the script with a message naming the argument; nothing is dropped silently. That is deliberate: the script is meant to be permitted to agents in place of a blanket `docker run`, and a passthrough would make the two grants identical. If you genuinely need something outside that list, run `docker` yourself with the user's agreement and say why — do not add a passthrough back.
- `--mount-docker-socket` is what lets the instance run **Lambda and ECS**, and what lets the web console discover its own published port and connect without the *Connect to Overcast* screen. It is also host root for anything inside the container, so ask for it when you need it and leave it off when you do not.
- When you want the user to look at something in a test instance, give them the **full clickable URL including the port** (e.g. `http://localhost:4570` / `http://localhost:4571`) — never say "open the web UI" and assume a port.
- The same courtesy applies in reverse: something already listening on 4566/4567 is the user's instance — never kill it, restart it, or point tests at it without being asked.

## Docker images — one tag per branch, and remove it afterwards

`make docker-console` and `make docker-slim` (and their `task` equivalents) tag the image after the **sanitised current branch name**, not `overcast:dev`. The shared tag was the same class of problem as a shared port: several worktrees build into one name, a parallel agent's build lands between yours and your `docker run`, and you test or screenshot their code with nothing to indicate it happened.

- `scripts/image-tag.sh` (or `.ps1`) prints the tag. A slash becomes `-`, uppercase is lowered, and a detached HEAD becomes `detached-<short sha>` rather than the literal `HEAD` every detached worktree would otherwise share. Set `OVERCAST_IMAGE_TAG` to override; an override that is not a legal Docker tag is refused rather than quietly rewritten.
- Pass the built image to the launcher with `--image "overcast:$(sh scripts/image-tag.sh)"`.
- **Clean up.** One image per branch accumulates a gigabyte at a time on a machine several agents share. `make docker-clean` (or `task docker-clean`) removes this branch's console and slim images; run it when you are done with them, in the same task that built them.

## Calling the AWS CLI — use `scripts/awslocal.sh`

**Never call `aws` directly against Overcast.** Use `scripts/awslocal.sh` (or `.ps1`):

```sh
OVERCAST_PORT=4580 scripts/awslocal.sh sqs list-queues
```

A developer machine carries an ambient AWS environment — `AWS_PROFILE`, `AWS_REGION`, SSO state, a stale `AWS_ENDPOINT_URL` — and it leaks into every `aws` call without announcing itself. The wrapper unsets **every** `AWS_*` variable in its own process and sets back only the endpoint, placeholder credentials, region, and `AWS_PAGER=""` (an unset pager hangs a piped call on `less`). Your shell is left untouched.

This is not hypothetical tidiness. An ambient `AWS_REGION` once made an agent's `aws` calls sign for a different region than its `curl` calls, so the two saw different SQS queues; that was written up as a severe cross-protocol state bug before the region mismatch was spotted. Region partitioning was correct behaviour all along. Exporting `AWS_ACCESS_KEY_ID` and friends by hand does not protect you — it leaves everything you did not think of in place, which is exactly the part that bites.

Two things it deliberately does not cover:

- **Endpoint and URL-minting tests.** It passes `--endpoint-url`, which suppresses the SQS queue-URL origin override in the JS/.NET/Java SDKs — the bug class [docs/dev/manual-testing.md](./docs/dev/manual-testing.md) exists to catch. Those need a *bare* SDK client with no endpoint configured.
- **Region.** It defaults to `us-east-1`; pass `OVERCAST_REGION` when you mean another. Resources really are per-region, as on AWS, so a create in one region and a list in another correctly disagree.

## What agents must NOT do

- **Never push directly to `main`.** Agents must not run `git push origin main`, push the current branch when it is `main`, create or move tags on `main`, or otherwise update protected release branches directly. Always work on a feature/release branch and use a pull request or explicit human-managed merge path. If a task appears to require a direct `main` push to trigger automation, stop and ask for human confirmation instead. (The compat workflow's baseline-promotion commit is the sole exception, and it belongs to the workflow — never to you: see [compat/AGENTS.md § Baseline & uniformity policy](./compat/AGENTS.md#baseline--uniformity-policy).)
- **Never open a pull request into a `release/*` branch.** Base every PR on `main`, including while a release is being prepared. The release bot owns that branch — it merges `main` in and folds new fragments into the release section on every push — and a PR based on it skips the test matrix, the compat suite, the changelog gate and the breaking-change hold, all of which run on PRs into `main` only. If you branched off a release branch by accident, rebase onto `main` (`git rebase --onto origin/main origin/release/x.y.z`) and retarget before pushing. `release-branch-base.yml` refuses the shape; [RELEASE.md § Nothing merges into the release branch](./RELEASE.md#nothing-merges-into-the-release-branch) says why.
- **Never raise an issue, pull request or comment on an external repository without the user's firm, explicit confirmation for that specific report.** Anything posted through `gh` goes out under the user's own GitHub identity, on projects they may have a relationship with — it is theirs to send, not yours. This covers upstream bug reports and feature requests (oxc, facebook/react, renovate, the AWS SDKs, …), comments on other people's issues, and reactions; and it covers briefing a sub-agent to do any of those — a brief may say "write it up and stop", never "file it if none exists". The right terminal state is: evidence and a minimal reproduction collected, the upstream tracker searched read-only for an existing report, a plain factual draft written in the issue's own template (no narrative, no praise, no "found while…"), and the user asked. Only a clear "yes, file it" posts it. If the user later asks for an already-posted report to be tidied, edit in place and keep to the facts.
- **Never commit directly on `main`.** All changes must go through a non-`main` branch and pull request. Before committing, run `git branch --show-current`; if it returns `main`, stop and ask before doing anything else. This applies to every change, including release prep, docs-only edits, generated files, and emergency fixes.
- **Start editing workflows on a branch.** At the start of any skill or workflow that may edit files or create commits, check `git branch --show-current`. If it returns `main`, create or switch to a task branch before editing; if unrelated worktree changes make that unsafe, stop and ask. Use clear branch names such as `fix/sqs-visibility-timeout`, `compat/elasticache-serverless-cache`, or `release/0.0.1-alpha.6`.
- **Amend related mistakes instead of narrating them.** If you forgot a directly related file such as a changelog fragment, generated doc, or focused test, amend or squash your own commit so the branch stays coherent. This is fine even after pushing when you own the branch, it is not shared, and you use `git push --force-with-lease` to avoid overwriting other people's work. Use a separate follow-up commit instead when the change is already merged, the branch is known to be shared, or unrelated commits now sit on top. Do not create noisy correction commits or give the user a running play-by-play of fixups unless the branch history is shared or the user asks for that detail.
- **Never leave the workspace in a broken state.** After every change, check the workspace problem list (compiler errors, type errors, lint errors) - via the `get_errors` tool, and fix any problems you introduced before considering the task done. You are not finished while problems you caused remain open.
  - **`go build ./...` is necessary but not sufficient.** It only catches compile errors. Also run `go vet ./...` to catch lint/static-analysis warnings (unused params, unused funcs, unnecessary nil checks, etc.) that appear in the VS Code Problems panel but don't fail compilation. Fix every warning you introduced.
  - **Sub-agents must do this too.** A sub-agent invoked by a parent agent is held to the same standard. Before returning a result, run `go build ./...` (for Go changes) and/or `pnpm run typecheck` in `web/` (for TypeScript changes — not `tsc --noEmit`, which typechecks nothing) and fix every error you caused. If a linter or vet warning is introduced (e.g. `go vet ./...` reports a new issue), fix it. Do not offload verification to the parent — own it.
- **Never commit or push a diff you have not read.** Read `git diff --staged` before every commit and `git diff main...HEAD` before every push, and remove anything that does not belong. This is not optional on long sessions — it matters most there. See [Self-review the diff](#self-review-the-diff-before-committing-or-pushing).
- Never implement a handler or fix a bug without a failing/reproducing test first
- Never return bare `404` for unimplemented operations — always `501`
- Never call `os.Getenv` in service code — use `*config.Config`
- Never update only the summary table in a service doc — update both tables
- Never publish a performance claim without measurement conditions — every number (startup time, memory, image size, latency) must document what was measured, how, and under what conditions. See [docs/dev/performance.md § Documenting performance claims](docs/dev/performance.md#documenting-performance-claims)
- Never do blocking work (store reads, network I/O, DDL, file reads) inside `<svc>.New()` or an `Init*` method called from `router.New()`. Use a `sync.Once`-guarded lazy-init method called from the handlers that need it. See [docs/dev/performance.md § Startup budget — rules for service authors](docs/dev/performance.md#startup-budget--rules-for-service-authors)
- Never edit `web/src/routeTree.gen.ts` — it is auto-generated by TanStack Router when the dev server runs (`pnpm run dev` in `web/`). After adding or changing route files, check whether the dev server is already running (the user usually has it running); if so, the file will update automatically. Only regenerate manually if the server is not running.
- Never assume that you are the only AGENT working - be careful with git operations that may break what others are working on (e.g `git stash` or `git checkout`)

---

## Tool use discipline — preventing hallucination in tool chains

Tools are the agent's primary source of truth about the runtime environment. To ensure clean, reliable tool-chaining:

### Ground truth rule

- **Tool outputs are authoritative.** If a tool returns data (e.g., `runtime_list_instances` returns a list of endpoints), that IS the current state. Never override it with cached knowledge, prior context, or assumptions.
- When a current tool result conflicts with prior context, surface the discrepancy explicitly for user review rather than silently choosing one.

### Chaining discipline

- **Use tool N's output as input to tool N+1.** For example: `runtime_list_instances` → use exactly those endpoints for `runtime_probe_instance`. Never probe endpoints not returned by the prior tool unless explicitly asked.
- **Document the dependency.** Annotate tool calls with the reason: "Using endpoint from runtime_list_instances: http://localhost:4566"
- **Never diverge into cached assumptions** once a tool chain is active. If prior knowledge suggests a different path, ask the user: "I know from earlier context that X, but the tool just returned Y—which should I use?"

### Validation before deviation

- Before using an endpoint, config value, or other runtime fact NOT returned by a tool, explicitly ask or state the assumption
- Default action when uncertain: use only what the current tools returned
- If no tool has provided it, that's a signal to call an appropriate tool

### Immutable snapshots

- Treat a tool's result as immutable within the task context
- Reference it by the snapshot, not by reconstructed logic
- Example: "Using the instances from step 1 (localhost:4566, 127.0.0.1:4566)"—then don't invent a third instance mid-chain

### This prevents

- Hallucinating service endpoints or config from prior logs
- Ignoring explicit tool output because prior context "feels" more reliable
- Skipping obvious follow-up tools because of assumptions about what they'd return
- Silent divergence from the stated task into cached knowledge paths
