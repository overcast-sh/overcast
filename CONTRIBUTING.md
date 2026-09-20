# Contributing to Overcast

Welcome — we're glad you're here.

This guide covers everything you need to go from zero to a merged pull request — coding
standards, architecture decisions, performance expectations, and workflow.

For test conventions, see [tests/AGENTS.md](./tests/AGENTS.md).
For current implementation status and what to build next, see [STATUS.md](./STATUS.md).
AI agents using this repo should also read [AGENTS.md](./AGENTS.md) for agent-specific guardrails.

---

## Contents

- [Contributing to Overcast](#contributing-to-overcast)
  - [Contents](#contents)
  - [Project goals](#project-goals)
  - [Design philosophy: match real AWS](#design-philosophy-match-real-aws)
    - [AWS is the tie-breaker](#aws-is-the-tie-breaker)
  - [Service implementation tiers](#service-implementation-tiers)
  - [Core principles](#core-principles)
  - [Supported platforms](#supported-platforms)
    - [Cross-platform development contract](#cross-platform-development-contract)
  - [Prerequisites](#prerequisites)
  - [First-time setup](#first-time-setup)
  - [Development workflow](#development-workflow)
    - [Day-to-day commands](#day-to-day-commands)
    - [Step debugging](#step-debugging)
    - [TDD cycle — mandatory](#tdd-cycle--mandatory)
  - [Code standards](#code-standards)
    - [Clean, idiomatic, performant code](#clean-idiomatic-performant-code)
  - [Error handling](#error-handling)
  - [Logging standards](#logging-standards)
    - [Log levels](#log-levels)
    - [When to use each level](#when-to-use-each-level)
  - [Time / clock injection](#time--clock-injection)
  - [Shared utilities — use serviceutil, never duplicate](#shared-utilities--use-serviceutil-never-duplicate)
  - [Persisted state: JSON compatibility, table graduation, and migrations](#persisted-state-json-compatibility-table-graduation-and-migrations)
    - [JSON compatibility: evolving a persisted struct](#json-compatibility-evolving-a-persisted-struct)
    - [Data earns a table](#data-earns-a-table)
    - [Writing a migration](#writing-a-migration)
  - [Performance and safety](#performance-and-safety)
  - [Design patterns](#design-patterns)
  - [CloudFormation integration](#cloudformation-integration)
    - [How it works](#how-it-works)
    - [Forwarding properties — the allow-list is data, and its leftovers are reported](#forwarding-properties--the-allow-list-is-data-and-its-leftovers-are-reported)
    - [Rules](#rules)
  - [Testing](#testing)
  - [Versioning and changelog](#versioning-and-changelog)
  - [Refreshing the AWS API models](#refreshing-the-aws-api-models)
  - [How to add an endpoint](#how-to-add-an-endpoint)
  - [How to add a service](#how-to-add-a-service)
  - [Writing docs](#writing-docs)
  - [Service package structure](#service-package-structure)
  - [Web UI standards](#web-ui-standards)
    - [Linting — oxlint](#linting--oxlint)
    - [API access policy (SDK-first)](#api-access-policy-sdk-first)
    - [Frontend — Tailwind CSS v4](#frontend--tailwind-css-v4)
    - [Tables — reach for `ResourceTable` before composing `<Table>` yourself](#tables--reach-for-resourcetable-before-composing-table-yourself)
    - [Attribute grids — `DefinitionCard`, not a two-column `<Table>`](#attribute-grids--definitioncard-not-a-two-column-table)
    - [Topology map methodology](#topology-map-methodology)
    - [Service home screen](#service-home-screen)
    - [Global search](#global-search)
    - [service-registry.ts and unsupported-services.ts](#service-registryts-and-unsupported-servicests)
  - [Commit conventions](#commit-conventions)
  - [Pull request checklist](#pull-request-checklist)
  - [Reporting bugs](#reporting-bugs)
  - [Ideas for contributions](#ideas-for-contributions)

---

## Project goals

Overcast aims to be the best zero-config local cloud emulator:

1. Works with the AWS CLI without any changes
2. Works with all official AWS SDK clients
3. Drop-in replacement for LocalStack — switching is one line
4. Zero configuration — `docker run -p 4566:4566` is the whole setup
5. Fast startup and low memory — CI should not wait for the emulator
6. Honest about gaps — missing features say `501`, not `200` with wrong data
7. Fully open — MIT, no auth tokens, no telemetry, no usage limits
8. Production-quality internals — race-safe, well-tested, easy to contribute to

---

## Design philosophy: match real AWS

The usefulness of Overcast is directly tied to how closely it behaves like real AWS. Every
behavioral difference — whether it's a wrong error code, a missing validation, a field with
a slightly different default, or a state transition that happens in the wrong order — is a
potential surprise waiting to bite a developer who trusted their local tests. The closer we
get to real AWS behaviour, the more confidently people can develop and test locally, and the
fewer "works on my machine" failures they hit when deploying.

This means:

- **Requests and responses are the compatibility contract.** Everything an AWS SDK sends to
  us and everything we send back — status codes, headers, body shape, field names, casing,
  default values, error codes, pagination tokens — MUST match real AWS. Internal
  implementation may differ freely, but the wire-level inputs and outputs are the public API
  and must be indistinguishable from the real service. This is what "compatibility" means.
- **Error codes and messages** should match what AWS actually returns, not what seems reasonable.
- **Response shapes** (field names, casing, nesting, default values) should mirror the real API.
- **Validation rules** should reject the same inputs AWS rejects, with the same error responses.
- **State transitions and side effects** should follow the same sequencing AWS uses.
- **Don't skip intermediate states.** If a real AWS resource goes through `CREATING` →
  `AVAILABLE`, the emulated version should too — even if the transition happens immediately
  after the initial response. Artificial delays are not required, but every state in the
  lifecycle must exist and be observable. Code that polls for `CREATING` before proceeding
  is real-world code, and it should work here the same way it works against AWS.
- **When you're unsure how AWS behaves, test it.** Spin up a real AWS resource, try the edge
  case, and replicate what you observe. Guessing leads to drift.

### AWS is the tie-breaker

The rules above tell you what to do once you know a decision is a fidelity decision. Most
drift does not arrive that way. It arrives as an ordinary design choice where two or three
options all look reasonable on internal grounds — what to call a derived resource, which
default to pick for an unset field, whether an operation on a missing resource errors or
no-ops, what order side effects fire in, whether an empty result is an omitted field or an
empty list. Nothing about those choices announces itself as a compatibility question, so the
temptation is to settle them on taste, convenience, or whatever the surrounding code does.

**When you have several defensible options in front of you, the question that decides is:
what does AWS do here? How does this specific service behave in this specific case?** Not
"what would a reasonable service do" — services differ, and AWS is not always internally
consistent. S3 and DynamoDB disagree about plenty. The answer you want is the one for the
service you are editing.

This applies even when the choice looks purely internal. Anything a client can observe —
through a response, a subsequent read, a state transition, a timing dependency, or an error
it has to handle — is part of the contract whether or not you were thinking about the
contract when you chose. If checking is cheap (docs, an existing test, a sibling
implementation), check before you decide rather than after review asks. If the answer turns
out not to be observable, you have lost a minute; if it was, you have avoided a divergence
that would have shipped looking correct.

When the evidence runs out and you genuinely have to pick, the two directions are not equally
bad. **A divergence that makes something work here and fail on AWS is the expensive one** — the
user tested locally, we said yes, and they find out in their account. That is the exact promise
this project exists to keep, and permissiveness is how it gets broken: accepting an input AWS
rejects, skipping a validation AWS enforces, defaulting a field AWS requires, ignoring a
property AWS acts on. Being stricter than AWS is also wrong, but it fails loudly, locally, in
front of the person who can report it. Prefer the error you can see.

Perfect parity is not always achievable — some behaviours depend on AWS internals we can't
replicate, and that's fine. But fidelity is the default goal, not a stretch goal. When a
known divergence is unavoidable, document it explicitly (in the service doc and in code
comments) so users aren't caught off guard. Never silently return a `200` with wrong
behaviour — a `501` that says "not implemented" is always preferable to a response that
looks right but acts wrong.

**When AWS's answer is the surprising one, record it.** Sometimes matching AWS means
shipping something that reads as a defect: a header a standard says to send that AWS
omits, an input AWS accepts that it plainly should reject, an answer every other emulator
disagrees with. Left unexplained, that gets "fixed" — by a linter, by a review comment, or
by the next contributor working from the spec instead of from AWS. Add it to
[docs/dev/compatibility/looks-like-a-bug.md](./docs/dev/compatibility/looks-like-a-bug.md)
with the command you ran and the date, and cite the page from a comment at the code site.
Checking real AWS needs the user's explicit approval first; that page's Methodology section
covers the cheapest way to get an answer worth recording.

---

## Service implementation tiers

Every service sits at one of four tiers. The tier names appear throughout this document,
in STATUS.md, and in plan docs — this section is their canonical definition. When a task
says "bring service X to inert level", this is the contract it refers to.

| Tier | Contract | Example |
| --- | --- | --- |
| **stub** | Operations are routed and answer with protocol-correct responses, but resources are synthetic or incomplete. Exists to satisfy discovery/IaC calls (CDK lookups, CloudFormation scaffolding). Unrouted operations return `501`. | Shield |
| **inert** | Resources exist as real metadata: they can be created, read, listed, updated, and deleted **exactly as real AWS would** — same validation rules, error codes, defaults, derived/auto-created child resources, pagination, and tagging. They just don't *do* anything: no side effects (no email actually sent, no container provisioned, no DNS actually served). | Route 53 |
| **partial** | Inert plus real side effects for the most-used subset of the service (e.g. Docker-backed execution, actual message delivery), with the rest of the surface still inert or `501`. | RDS, ElastiCache |
| **full** | The emulated 20%-most-used surface is behaviourally complete, side effects included. Remaining exotic operations may still be `501`, honestly reported. | S3, SQS |

What each tier obligates:

- **stub → inert:** implement the resource lifecycle against `state.Store` with
  AWS-faithful wire behaviour (see [Design philosophy](#design-philosophy-match-real-aws)).
  Auto-created companion resources (e.g. a hosted zone's default NS/SOA records, a queue's
  default attributes) must exist, because real-world code reads them back. CloudFormation
  handlers must create real resources through the emulated service — not synthetic stub
  IDs (see [CloudFormation integration](#cloudformation-integration)). The service must
  register a web-UI search contributor (see [Global search](#global-search)).
- **inert → partial/full:** side effects appear; CF handlers pass through all relevant
  configuration; documented divergences shrink.

The code-level source of truth is `ServiceTiers` in
[internal/router/tiers.go](./internal/router/tiers.go) (surfaced via `/_overcast/health` and the
web UI, with `ServiceGoalTiers` marking work-in-progress services) — update it in the
same commit that graduates a service. The per-operation inventory lives in each
service's `capabilities_dev.go`. Report operations honestly: `StatusSupported` means
"behaves like AWS at inert level or above", never "returns a plausible-looking 200".

### Operation-level tiers (Tier 0 / Tier 1 / Tier 2)

The tiers above describe a whole *service*. [docs/plans/inert-tier-rollout.md](./docs/plans/inert-tier-rollout.md)
defines a parallel, finer-grained vocabulary for a single *operation*, used when
generating or reasoning about Tier 1 coverage at scale:

| Tier | Name | Meaning |
| --- | --- | --- |
| **Tier 0** | protocol-correct 501 | Routed, and answers with the right `NotImplemented` envelope for its protocol family. No state, no shape. |
| **Tier 1** | inert | Accepted, stores and echoes back everything the caller told it — CRUD, tagging, pagination, ARNs, timestamps, not-found/conflict errors — with no side effects. This is the per-operation form of the **inert** service tier above; see the plan's §3 for the normative contract and `internal/inert/conformance` for it as executable tests. |
| **Tier 2** | full emulation | The operation actually does the thing (an invoke runs, a message is delivered). |

A whole service's tier (stub/inert/partial/full) is a rollup of its operations' tiers — a
service is "inert" once every in-scope operation it owns is at least Tier 1.

---

## Core principles

These guide every decision — from architecture to variable naming. Read them before writing code.

1. **Test-first, always.** Failing test before every feature. Reproducing test before every fix.
2. **Correctness over completeness.** A missing `501` is better than a broken `200`.
3. **No global state.** All dependencies injected. Every component independently testable.
4. **One responsibility per file.** `service.go` routes. `handler.go` handles HTTP. `store.go` owns state.
5. **Explicit over implicit.** Errors are values. Config is typed. Nothing magic.
6. **DRY — never duplicate logic.** If the same pattern exists in two places, extract it. Shared helpers go in `serviceutil`. Shared types go in `protocol` or a common types file. Copy-paste is a bug. However, duplication is acceptable when the DRY abstraction would be harder to understand or maintain than the repeated code — avoid over-engineering.
7. **Idiomatic Go, always.** Follow [Effective Go](https://go.dev/doc/effective_go) and standard library conventions. Prefer simple, readable code over clever abstractions. Keep functions short and focused — one screen, one job. Use the type system to prevent misuse. If a reviewer has to ask "why?" the code is too clever.
8. **Performance is everyone's job.** Think about allocations, algorithmic complexity, and memory layout in every code path — not just hot paths. Pre-size collections, reuse buffers, stream large data, avoid unnecessary copies. Profile before optimising, but write efficient code from the start. Target: every handler ≤1 ms overhead above store access.
9. **Maintainability is a feature.** Code is read 10× more than it is written. Optimise for the next reader: consistent structure, clear naming, small interfaces, minimal coupling. If a change in one package forces changes in three others, the design is wrong.
10. **Honest TODOs.** Every `// TODO:` includes a description and priority:
    `// TODO(priority:P1): implement SigV4 validation` — picked up by the TODO-to-issue Action.
    The marker only ever *opens* a comment, in exactly that form, and its description is
    one line of at most 120 characters — the Action takes the rest of that line as the
    issue title, and everything below it as the issue body, so detail goes on the next
    line rather than running the title on. Naming a marker
    mid-sentence files an issue out of the middle of your prose ([#1138](https://github.com/overcast-sh/overcast/issues/1138)),
    so refer to deferred work in prose without writing the word: "the P3 note on
    `apigateway.Method`". `make lint-todos` enforces this.
11. **AWS compatibility over test convenience.** Never diverge from real AWS behaviour to make tests easier.
    Async behaviour (SNS delivery, SQS visibility timeouts, Lambda cold starts) stays async. Tests adapt.
12. **AWS fidelity on core APIs — extensions are strictly additive.** Implemented AWS API
    endpoints must behave exactly as a real AWS SDK client expects. Never add non-standard
    fields to AWS responses, alter error codes, change state machine transitions, or introduce
    side effects that would surprise code tested against real AWS. Emulator-only features
    (progress SSE, source browsing, saved test events, topology graph) live behind `/_` prefixed
    internal endpoints or custom headers — never on the AWS API surface. If a feature cannot be
    implemented faithfully, return `501` rather than a divergent `200`.
13. **When several options are defensible, ask what AWS does.** Fidelity is not only a bar for
    changes you already know are AWS-facing — it is the tie-breaker for ordinary design choices
    whose alternatives all look reasonable. Naming, defaults, ordering, error-vs-no-op, empty-vs-
    omitted: settle them on how the service you are editing behaves in that exact case, not on
    taste or convenience. See [AWS is the tie-breaker](#aws-is-the-tie-breaker).

---

## Supported platforms

| Platform | Arch         | Support  | Notes                                        |
| -------- | ------------ | -------- | -------------------------------------------- |
| Linux    | amd64, arm64 | Required | Docker image target; CI runs here            |
| macOS    | amd64, arm64 | Required | Developer workstations; Apple Silicon native |
| Windows  | amd64        | Required | Native console binary and development host   |

**All contributions must compile without error on every supported platform.**
Use build tags (`//go:build linux`, `//go:build !windows`, etc.) to isolate
platform-specific syscalls. Verify with:

```bash
GOOS=linux   GOARCH=amd64 go build ./...
GOOS=darwin  GOARCH=arm64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...
# or: make build-cross
```

`make build`, `make build-slim` and every cross-build target run
**`make lambda-init`** first (`task lambda-init` on Windows): the in-container
Lambda init is embedded in the binary and, like the SPA, is build output rather
than a committed file. A plain `go build ./...` skips it and still compiles —
the committed placeholder keeps the embed resolving — so run it whenever the
binary has to actually invoke a Lambda function.
[docs/dev/generated-files.md](./docs/dev/generated-files.md) is the full
inventory: every generated artefact, whether it is committed, build output or
derived at runtime, and the rule that decides which.

`go build ./...` on the current platform only catches compile errors for that
platform. If you use `syscall`, `os/exec` with Unix-specific flags, or anything
from `golang.org/x/sys/unix`, add a build tag and a corresponding stub for other
platforms. See `internal/inithooks/hooks_unix.go` and `hooks_windows.go` for the
pattern, and `internal/router/procstart_*.go` for the process-start-time files.

### Cross-platform development contract

Platform support covers both the shipped Overcast runtime and the contributor experience. Source
code, build and test tooling, hooks, setup instructions, and routine development workflows must
work on Windows, macOS, and Linux. A workflow that only works on its author's machine or preferred
shell is not complete.

- Never commit machine-specific absolute paths. Derive locations from the repository, operating
  system, environment, or an explicitly documented local override.
- Prefer platform-neutral implementations and standard path/process APIs. Do not concatenate path
  separators, assume `/tmp`, or rely on Unix process behavior in shared code.
- When tooling genuinely needs shell-specific behavior, provide equivalent POSIX and PowerShell
  entry points or a tested platform dispatcher. Keep their observable behavior and exit codes in
  sync.
- Label shell-specific command examples and provide the equivalent command when syntax differs.
  Requiring Git Bash on Windows is acceptable only when it is an explicit prerequisite and the
  invocation does not assume one installation path.
- Test platform-selection and path-resolution logic. Cross-build the runtime with `make
  build-cross`; exercise native platform tooling when changing it or explain the verification gap
  in the PR.

If a dependency cannot support one of these platforms, surface the tradeoff before adding it. Do
not silently narrow platform support or document a single-machine workaround as project policy.

---

## Prerequisites

| Tool          | Version | Install                                                   |
| ------------- | ------- | --------------------------------------------------------- |
| Git           | Current | https://git-scm.com/downloads — use Git for Windows on Windows (includes Git Bash used by hooks) |
| jq            | 1.6+    | JSON processing for agent hooks and PR helper scripts — https://jqlang.org/download/ |
| Go            | 1.25+ (the `go` line in `go.mod`) | https://go.dev/dl/                                        |
| Docker        | 24+     | https://docs.docker.com/get-docker/                       |
| golangci-lint | `GOLANGCI_LINT_VERSION` in the Makefile | `make lint-go` uses pinned `go run` automatically; Renovate keeps the pin current |
| actionlint    | `ACTIONLINT_VERSION` in the Makefile | `make lint-actions` uses pinned `go run` automatically; Renovate keeps the pin current |
| Node.js       | see `.node-version` | For web UI builds and Lambda work — https://nodejs.org. The file at the repo root is the one source of truth: CI's `setup-node` steps, fnm, nodenv and n read it, and Renovate moves it (with the Dockerfile's node base, in one PR) when the next LTS arrives |

---

## First-time setup

```bash
# 1. Fork and clone
git clone https://github.com/overcast-sh/overcast
cd overcast

# 2. Install Go dependencies
go mod tidy

# 3. Confirm everything passes
make test

# 4. Start the server (confirms it boots cleanly)
make run
# → overcast listening on :4566
```

If `make test` passes, you're ready.

---

## Development workflow

### Day-to-day commands

```bash
make test              # all tests + race detector — run before every commit
make test-unit         # fast unit tests (internal/) — run while writing code
make test-integration  # full integration suite
make test-coverage     # HTML coverage report → coverage.html
make lint              # all linters: Go/emulation, web UI, GitHub Actions
make lint-go           # Go/emulation lint (golangci-lint)
make lint-web          # web UI lint (oxlint)
make lint-actions      # GitHub Actions workflow lint (pinned actionlint)
make fmt               # gofmt all files
make vet               # go vet
make check             # aggregate pre-PR checks
make run               # build and run on :4566
docker compose up      # run in Docker (rebuilds image)
make docker-console    # build the image, tagged after the current branch
make docker-clean      # remove this branch's images when you are done
make docker-clean-test-networks  # sweep overcast_*_test_* networks a killed test run left
```

### Docker image tags are per-branch

`make docker-console` and `make docker-slim` tag their output `overcast:<sanitised
branch name>` and `overcast-slim:<sanitised branch name>` rather than the
`overcast:dev` they used to hardcode. With one shared tag, two checkouts — two
worktrees, or a contributor and an agent — build into the same name, and
whichever built last is what runs. The failure is silent: the container starts
and serves the other branch's code.

`scripts/image-tag.sh` (or `image-tag.ps1`) derives the tag: a slash becomes
`-`, uppercase is lowered, and a detached HEAD becomes `detached-<short sha>`
rather than the literal `HEAD` that every detached checkout would share. Override
it three ways, in the Makefile's usual style:

```sh
make docker-console IMAGE_TAG=scratch
make docker-console CONSOLE_IMAGE=overcast:whatever
OVERCAST_IMAGE_TAG=scratch task docker-console
```

`docker compose up` is the exception: Compose cannot run a script to derive a
tag, so it stays on `overcast:dev` unless you export `OVERCAST_IMAGE_TAG`.

**Clean up after yourself.** A tag per branch means an image per branch, and
they are not small. `make docker-clean` (or `task docker-clean`) removes the
current branch's pair.

Docker-backed tests mint a network pair per test server and remove it on
cleanup, but a killed or timed-out test process runs no cleanups, and enough
leaked pairs exhaust the daemon's address pool. `make docker-clean-test-networks`
sweeps the empty `overcast_*_test_*` pairs and nothing else — see
[docs/dev/development-setup.md § Docker networks left behind by tests](docs/dev/development-setup.md#docker-networks-left-behind-by-tests).

### Reproducing CI locally

`make ci-local` (or `task ci-local`, or `bash scripts/ci-local.sh`) runs the
same pipeline as `.github/workflows/test.yml`, in the same dependency order:

```
docs index → web lint → typecheck → vitest → SPA build
           → go vet → go build → go test
```

One dependency fixes that order: `embed.go` has `//go:embed all:web/dist`, so
the Go stages need a built SPA — which is why the CI Go jobs declare
`needs: web` and download the `web-dist` artifact. (A committed
`web/dist/.gitkeep` keeps the embed pattern resolving on a bare checkout, so
`go build ./...` always compiles; the resulting binary just has no UI, and the
Go stages here assert a real `index.html` rather than accepting that.)

Nothing else has to be generated first. The console's docs navigation and its
search index are derived from the docs the binary embeds, at runtime
([`internal/docsindex`](./internal/docsindex/docsindex.go)), so there is no
artifact to build before a build and none to catch stale. `make docs-lint`
(part of `make docs-check`) checks the docs themselves.

The script stops at the first failure and names the stage that failed. Every Go
command goes through [`scripts/docker-go.sh`](./scripts/docker-go.sh), so **no
host Go toolchain is required** — only Docker and Node. It runs from Git Bash
on Windows as well as Linux/macOS.

```bash
make ci-local                        # everything
make ci-local-web                    # web stages only — iterating on the UI
make ci-local-go                     # Go stages only (needs an existing web/dist)
bash scripts/ci-local.sh --full      # Go tests: -race across ./... (slow)
bash scripts/ci-local.sh --host-go   # use a host `go` instead of Docker
bash scripts/ci-local.sh --help      # all flags and env equivalents
```

Node dependencies are never installed for you: run `pnpm install` in `web/` once
before the first web run.

> [!NOTE]
> On Windows/macOS the Go stages run against a bind-mounted repo, so they are
> much slower than native — a full `make ci-local` takes ~35 min cold
> (`internal/state` alone can take 10 min). Use `make ci-local-web` while
> iterating on the UI, and save the full run for pre-push.

### No local Go toolchain? (e.g. Windows outside the devcontainer)

`scripts/docker-go.sh` (Git Bash/macOS/Linux) and `scripts/docker-go.ps1`
(PowerShell) run any `go` command in a Docker container with the repo mounted
and shared module/build caches, so no host Go install is needed:

```bash
scripts/docker-go.sh test -count=1 ./internal/state/...
scripts/docker-go.sh vet ./...
scripts/docker-go.sh shell   # interactive shell in the container
```

They also work from git worktrees, which the devcontainer cannot see (it
mounts only the main checkout). See the header comment in
[scripts/docker-go.sh](./scripts/docker-go.sh) for cache/performance details.

**They are CPU-capped.** Uncapped, the container takes the whole machine: on a
24-core host `go test ./internal/services/...` through the wrapper peaked at
**2269% CPU** — 22.7 of 24 cores — and held it there for the compile phase. The
wrappers now bound three separate things, because one is not enough:
`docker run --cpus` (what the container may consume), `GOMAXPROCS` (parallelism
*inside* one process — container-aware `GOMAXPROCS` only arrived in Go 1.25, and
the image was `golang:1.24-bookworm` when this was measured, so its runtime would
otherwise still see all 24 cores; the wrappers now take the devcontainer's image
from `.devcontainer/Dockerfile` and keep setting it so the cap holds whatever
`OVERCAST_GO_IMAGE` names), and `go test -p` (concurrent test *binaries*, which
defaults to `GOMAXPROCS` and so squares the parallelism if left alone). Same run
with the cap: **1049% peak**, under the 1200% ceiling, for roughly 15% more wall
clock.

> Measured 2026-08-08 (UTC) on Windows 11, 24 logical cores, Docker Desktop reporting
> 24 CPUs, `golang:1.24-bookworm`. Both runs were
> `docker-go.sh test -run '^$' -count=1 ./internal/services/...` against a
> **cold** `OVERCAST_GO_BUILD_CACHE` volume, so the compile phase — the part
> that saturates — is what is being compared. CPU is the peak of ~1 Hz
> `docker stats --no-stream` samples of that run's container only; wall clock is
> the sample count (22 uncapped vs 25 capped), not a stopwatch.

Defaults are derived from the detected core count, never hardcoded — `--cpus=N`
is rejected outright when N exceeds the CPUs the daemon reports, so a fixed
number would break smaller machines. Half the cores for `--cpus`/`GOMAXPROCS`,
a quarter for `-p`, both clamped to at least 1.

| Variable | Default | Effect |
| --- | --- | --- |
| `OVERCAST_GO_CPUS` | half the detected cores | `docker run --cpus` and `GOMAXPROCS`. `0` removes the cap entirely — the pre-cap behaviour. |
| `OVERCAST_GO_TEST_P` | a quarter of the detected cores | `-p`, injected after the `test` subcommand. `0` never injects. An explicit `-p` from the caller always wins. |

`scripts/go.sh`'s Docker fallback is capped the same way. Its native path is
not: `--cpus` has nothing to bound on the host, and a host toolchain is yours to
schedule.

`docker-compose.dev.yml`'s `test` service is capped too, but it needs a helper to
do it: a Compose file can only carry a literal or a `${VAR}`, and `docker compose
run` has no `--cpus` flag, so the number cannot be derived where it is used.
`make container-test`, `make container-test-unit`,
`make container-test-integration` and their `task` equivalents go through
[scripts/container-test.sh](./scripts/container-test.sh) (`.ps1` on Windows),
which computes the same numbers and exports `OVERCAST_GO_CPUS` and
`OVERCAST_GO_TEST_P` for Compose to substitute into `cpus:`, `GOMAXPROCS` and
`GOFLAGS`. `-p` travels via `GOFLAGS` rather than the service's `command:` so it
survives a command override; it is spelled as an empty value rather than `0`
because `go test -p 0` is an error. Invoking `docker compose` by hand still
works and stays unbounded.

### Step debugging

Full step debugging is supported. Set a breakpoint (click left of line number),
press F5, select a launch configuration. See **[docs/dev/debugging.md](./docs/dev/debugging.md)**
for the full guide including conditional breakpoints, logpoints, and debugging
test failures.

### TDD cycle — mandatory

> [!IMPORTANT]
> We are strict about test-first development. Every feature starts with a failing test;
> every bug fix starts with a reproducing test. PRs without tests will not be merged.

The order is:

1. Write a **failing test** that describes the desired behaviour
2. Run `make test` — confirm the test fails for the right reason
3. Write the **minimum implementation** to make it pass
4. Run `make test` — all tests must pass with race detector
5. Refactor if needed — tests must still pass
6. Update service-doc prose (behavior notes, caveats) as needed, then regenerate generated docs tables with `make docs`; add a changelog fragment under `.changelog/` (see [Versioning and changelog](#versioning-and-changelog))

See [tests/AGENTS.md](./tests/AGENTS.md) for test conventions.

---

## Code standards

- **Format:** `gofmt`. Run `make fmt` before committing. Non-formatted code fails CI.
- **Lint:** `golangci-lint` v2.x (pinned in the Makefile, fetched via `go run` — no install needed). Run `make lint`. Config in `.golangci.yml`, which uses the v2 schema.
- **Naming:** Exported types get doc comments. Error sentinels: `ErrBucketNotFound`. Constructors: `NewHandler(...)`.
- **Comments:** Exported symbols require doc comments (linter enforced). Mark deferred work with `// TODO(priority:Pn):` opening the comment — never mid-sentence (`make lint-todos`, see [Honest TODOs](#core-principles)).
- **HTTP errors:** Use `protocol.WriteXMLError` (S3) or `protocol.WriteJSONError` (JSON services) — never raw `http.Error`.
- **HTTP success responses:** Use protocol writers (`protocol.WriteXML`, `protocol.WriteQueryXML`, `protocol.WriteJSON`, `protocol.WriteAWSJSON`) rather than ad-hoc `json.Marshal` + header writing in handlers.
- **Unimplemented:** Return `501` via the protocol-matching helper (`protocol.NotImplementedXML`, `protocol.NotImplementedQueryXML`, `protocol.NotImplementedJSON`) — never a bare `404`.
- **Query-protocol parse failures:** Return an AWS `InvalidArgument` Query XML error (`protocol.WriteQueryXMLError`) — do not map malformed form/query input to `NotImplemented`.
- **Request IDs:** Success and error responses must include the expected AWS request-id header via shared protocol helpers; do not manually omit or rename request-id headers.
- **State:** All mutable state through `state.Store` — never direct maps or globals.
- **No globals:** All dependencies are injected via function parameters.
- **Time:** Never call `time.Now()` directly in service or handler code. Use the injected `clock.Clock` (from `internal/clock`). See [Time / clock injection](#time--clock-injection) below.

### State Store Key Conventions

`state.Store` keys are internal, but they power reset/debug tooling and should remain predictable.

- Use a service namespace such as `sqs:queues`, `lambda:functions`, or `appsync`; register new namespaces in `internal/state/tier.go` when they use `state.Store`.
- For region-scoped resources, build keys with `serviceutil.RegionKey(region, resourceKey)`, which produces `{region}/{resourceKey}` and allows empty-region scans across all regions.
- Prefer slash-delimited resource keys when the AWS identifier is path-like or naturally hierarchical, for example `{bucket}/{objectKey}`, `{table}/{hashKey}/{sortKey}`, or `{apiId}/{resourceId}`.
- Do not force slash hierarchy when AWS uses another stable identifier shape; preserve identifiers that are naturally ARN-, colon-, or name-based.
- Keep flat keys canonical. Debug tree views may split on `/` for readability, but all reads/deletes/copy links must use the exact stored key.
- Large or highly indexed data may use a service-specific backend instead of `state.Store` when there is a measured access-pattern reason, as DynamoDB items do; expose those through virtual debug namespaces rather than duplicating data into `state.Store`.

### Clean, idiomatic, performant code

These apply **everywhere** — handlers, stores, tests, utilities, middleware:

- **No dead code.** Delete unused functions, variables, and imports. Do not comment out code "for later."
- **No magic numbers.** Use named constants. `maxPageSize = 1000` not `1000` scattered through handlers.
- **Small interfaces.** Accept the narrowest interface that works (`io.Reader` not `*os.File`). Produce concrete types.
- **Value receivers for read-only methods.** Pointer receivers only when mutating or when the struct is large.
- **Return early.** Guard clauses at the top, happy path unindented. Avoid deep nesting.
- **Table-driven tests.** One `t.Run` per case. Share setup, vary inputs. Never copy-paste a test and tweak one line.
- **Zero-alloc where practical.** Use `sync.Pool` for hot-path buffers, `strings.Builder` for concatenation, `strconv` over `fmt.Sprintf` for simple conversions. Avoid `interface{}` in tight loops.
- **Consistent structure across services.** If S3 does it one way and SQS another, unify — don't let inconsistency accumulate.

---

## Error handling

Wrap errors with cause — never discard:

```go
// Standard wrapping — add context, preserve the original
return fmt.Errorf("s3: put object %q: %w", key, err)
```

**Inspecting the chain:**

```go
errors.Is(err, io.EOF)              // true if io.EOF is anywhere in the chain
errors.As(err, &specificType)       // extract a specific type from the chain
errors.Unwrap(err)                  // get the immediate cause
```

**For AWS errors, use `protocol.Wrap()`** — attaches an underlying cause while
presenting a clean AWS error code to the HTTP client:

```go
// The client sees: {"__type":"InternalError","message":"An internal error occurred."}
// The server logs: InternalError (cause: sqlite: disk I/O error)
return nil, protocol.Wrap(protocol.ErrInternalError, storageErr)
```

The cause is **never sent to clients** — this is tested. It is only available
for server-side logging and debugging. This is the recommended pattern any time
a state operation fails — never discard the underlying error.

Use `errors.Is` / `errors.As` to inspect. `protocol.AsAWSError(err)` extracts an AWS error from the chain.

---

## Logging standards

We use structured logging (`go.uber.org/zap`). Never use `fmt.Sprintf` in log messages.

```go
// ✅ structured — fields are queryable and filterable
logger.Info("bucket created",
    zap.String("bucket", name),
    zap.String("region", cfg.Region),
)

// ❌ unstructured — just a string, can't filter or query
logger.Info(fmt.Sprintf("bucket %s created in %s", name, cfg.Region))
```

### Log levels

Overcast's level ladder has five rungs: `TRACE` < `DEBUG` < `INFO` < `WARN` < `ERROR`.
`TRACE` is Overcast-specific — zap has no built-in level below `DEBUG`, so it's
defined in [`internal/logging`](./internal/logging/level.go) as
`zapcore.Level(-2)`, one step under zap's `DebugLevel`. It exists because
Docker's `HEALTHCHECK` (and any orchestrator's liveness/readiness probe) hits
`/_overcast/health` every few seconds forever, and the web UI polls `/_overcast/debug/*`
continuously — at `INFO` that traffic drowns real activity; even at `DEBUG` it
drowns the request-explaining detail a human actually opened the logs to
read. `TRACE` gives that machine chatter somewhere to go without either
problem.

**The decision rule, for when you're adding a log line and unsure whether
it's `DEBUG` or `TRACE`:**

`DEBUG` is _event-driven and request-scoped_ — a line fires because a
specific client operation happened, and it explains Overcast's reasoning
about that operation (protocol identified, dispatch path chosen, store
decision made, why a message wasn't delivered). `TRACE` is _time-driven or
machinery-scoped_ — a line fires because time passed or infrastructure
cycled (health probes, UI polling, flush/checkpoint/sweep ticks, pool/buffer
internals), regardless of what any client did.

**The litmus test:** _if the server were completely idle — no client ever
connected — would this line still be emitted?_ If yes, it's `TRACE`. If it
only fires because of a specific request and helps explain that request's
outcome, it's `DEBUG`.

**Secondary heuristic:** lines with unbounded frequency independent of
request volume (per-tick, per-poll, per-cycle) are `TRACE` even when they
mention request-adjacent state (e.g. a table name); lines bounded per-request
(e.g. one retry-decision line within a request) are `DEBUG`.

**Why it matters:** `DEBUG` must stay readable enough that "re-run with
`OVERCAST_LOG_LEVEL=debug` and attach the output" produces a useful bug-report
artifact — `TRACE` is where volume is allowed.

### When to use each level

| Level   | Use for                                                                                                                                                                     |
| ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TRACE` | Periodic machinery chatter: health/readiness probe request logs, `/_overcast/debug/*` polling request logs, per-tick flush/checkpoint/maintenance/sweep cycle logs, pool/buffer internals. |
| `DEBUG` | What a human debugging Overcast actually wants: per-request internals for real AWS calls, dispatch/protocol decisions, store operation diagnostics, replay/seed detail, migration step detail beyond the INFO-level summary lines. |
| `INFO`  | Process lifecycle (listening, shutdown, migrations applied, backup written), genuine state milestones (resource created/deleted), and one request line per **real AWS API call**. |
| `WARN`  | Unexpected-but-handled conditions: a malformed persisted record skipped, protocol drift, a slow-filesystem probe, a torn WAL line tolerated, debug mode enabled.               |
| `ERROR` | Actionable failures: the store degraded, a migration failed, unrecoverable request handling, a panic recovery.                                                                |

The Logger middleware logs one line per request: `INFO` for real AWS API
calls, `TRACE` for `/_overcast/health` and `/_overcast/debug/*` polling (see
`isOperationalPollPath` in `internal/middleware/logger.go`), `ERROR` for 5xx
responses. Don't duplicate the per-request line in handlers — add `DEBUG`/
`TRACE` lines only for detail the request-line summary doesn't carry.

`OVERCAST_LOG_LEVEL` accepts `trace`, `debug`, `info`, `warn`, or `error`
(case-insensitive; see `internal/config/config.go`). `internal/logging.ParseLevel`
handles `trace` before delegating to zap's own parser, and
`internal/logging.WrapLevelEncoder` teaches the JSON/console encoders to
render `TraceLevel` as `"trace"`/`"TRACE"` instead of the meaningless
`"Level(-2)"` zap would otherwise print. Service code logs at `TRACE` via
`serviceutil.ServiceLogger.Trace(...)` (or `logger.Log(logging.TraceLevel, ...)`
for the handful of call sites — e.g. `internal/state`'s background loops —
that hold a raw `*zap.Logger` and can't import `internal/serviceutil` without
an import cycle through `internal/protocol`).

Never log credentials, request bodies of sensitive operations, or values that may contain PII.

---

## Time / clock injection

**Never call `time.Now()` directly** in service handlers, stores, or any code
under `internal/services/`. Instead, use the injected `clock.Clock` from
`internal/clock` (a thin wrapper around `github.com/benbjohnson/clock`).

```go
// ✅ Correct — injectable, testable
type Handler struct {
    clk clock.Clock
    // ...
}

msg.SentTimestamp = h.clk.Now().UnixMilli()
msg.VisibleAfter  = h.clk.Now().Add(time.Duration(delay) * time.Second)

// ❌ Wrong — not testable without real sleeps
msg.SentTimestamp = time.Now().UnixMilli()
```

The clock is wired in through `router.New → s3.New / sqs.New → newHandler`.
In production, `clock.New()` (real wall-clock) is used. In tests, pass
`helpers.WithMockClock()` to `NewTestServer` and advance via `srv.Clock.Add(d)`:

```go
// Advance through a 30-second visibility timeout without any real sleep:
srv := helpers.NewTestServer(t, helpers.WithMockClock())
// ... send a message, receive it (marks it invisible for 30s) ...
srv.Clock.Add(31 * time.Second)   // instant — no time.Sleep required
// ... receive again — message is now visible again ...
```

This also applies to Lambda timeout enforcement and any future SNS retry backoffs.

---

## Shared utilities — use serviceutil, never duplicate

```go
serviceutil.DecodeJSON(w, r, &req)           // JSON body -> struct, writes error on failure
serviceutil.RequireString(w, r, v, "Name")   // validates required field
serviceutil.QueryInt(r, "max-keys", 1000)    // query param with default
serviceutil.Paginate(items, limit, token, opts) // opaque continuation tokens; opts sets the op's AWS default/cap; errors.Is(err, serviceutil.ErrInvalidPageToken) on a bad token
serviceutil.BucketName(name)                // validates AWS naming rules
serviceutil.ServiceLogger(logger, "s3")     // scoped structured logger
serviceutil.LazyInit.Do(fn)                // sync.Once with retry; Reset() for tests
```

Add to `serviceutil` when a pattern appears in two or more services. Never add service-specific logic there.

---

## Persisted state: JSON compatibility, table graduation, and migrations

Most persisted state in `internal/state`'s generic `kv` table is a JSON blob owned by each
service's own `store.go` — `internal/state` itself never parses those values, it only stores
and retrieves opaque strings. This section covers how to evolve those structs safely, when a
namespace has outgrown the generic store, and how to register a migration when either need
arises.

### JSON compatibility: evolving a persisted struct

- **Additive changes are free.** `encoding/json` silently ignores unknown fields on decode
  and leaves missing fields at their Go zero value. A new struct field with a sensible
  zero-value default needs no migration and no data conversion — old rows decode fine as-is,
  and newly written rows carry the new field going forward.
- **Reshaping is breaking.** Renaming a field, changing its type, or restructuring the struct
  (splitting one field into several, changing a slice to a map, moving data to a different
  key shape) changes how existing rows decode. You have two options, and only two:
  1. **Convert the data**, via a numbered migration registered with
     `internal/state/migrate.go`'s `Migration`/`RegisterMigration` — the right choice when
     the data needs an actual transformation, or is moving to a new storage location
     entirely (a dedicated table). See the CloudWatch Logs blob→row conversion in
     [internal/services/cloudwatch/logs/migrations.go](./internal/services/cloudwatch/logs/migrations.go)
     for a worked example: it decodes the legacy `logs:events` JSON blob (one array per
     stream) and inserts one row per event into the new `logs_events` table.
  2. **Accept data loss on old→new upgrade**, documented explicitly — in the PR description
     and in a code comment near the struct — with a stated reason it's acceptable. Overcast
     is a local dev tool with no durability guarantees (see
     [AGENTS.md § Non-goals](./AGENTS.md#non-goals--decision-guide-for-agents)), so losing
     state that predates a reshape is often a reasonable tradeoff — but it must be a
     deliberate, written decision, never a silent side effect of a struct edit.
- **When the data stays in the same generic `kv` namespace and the old and new shapes can
  coexist,** prefer an **inline runtime fallback** over a migration: decode the old shape
  lazily on read, convert it in place, and write it back in the new shape. No migration
  needed at all. See
  [internal/services/cloudformation/store.go](./internal/services/cloudformation/store.go)'s
  `getStackEvents`/`getLegacyStackEvents`: `cfn:events` moved from one JSON-array blob per
  stack to one row per event, but both shapes live in the same `cfn:events` namespace — a
  read that finds no per-event rows for a stack falls back to the legacy blob key, decodes
  it, and opportunistically rewrites it as rows so later reads take the fast path.

**Deciding which to use:** ask whether the data is moving to a **new storage location or
shape** the generic `kv` store can't represent — a dedicated SQL table (see
[Data earns a table](#data-earns-a-table) below). If so, use a migration: it runs once, in
the background, off the request path. If the data is staying in the generic `kv` store and
the old and new shapes can be told apart on read (a missing field, a different key pattern,
a type-sniffable JSON shape), an inline runtime fallback is lighter weight than adding a
migration for what is, from SQLite's point of view, not a schema change at all.

### Data earns a table

The generic `kv` table is the default for a reason: it comes for free with `HybridStore`'s
memory-speed reads, the async flush path, `/_overcast/debug/state` visibility, and `/_overcast/reset`
support. A dedicated SQL table forfeits all of that — it needs its own backend
implementation(s), its own migration, and its own debug wiring. **A namespace earns a
dedicated table; a service does not automatically get one just because it's high-traffic.**
Most services should never need one.

A namespace earns a dedicated table only when at least one of these is true:

1. **Unbounded, high-frequency append that would force blob rewrites.** The generic store
   holds one JSON value per key — if a namespace's natural shape is "one growing list per
   resource" (events, log lines, stream records), every append is a full
   get-decode-append-encode-set of the *whole* history, which is quadratic over the
   resource's lifetime. `logs:events` was the textbook case (see
   [internal/services/cloudwatch/logs/migrations.go](./internal/services/cloudwatch/logs/migrations.go)).
2. **A query need the key order can't serve.** The generic store only supports exact-key
   lookups and prefix scans in key order. If a namespace genuinely needs a secondary index
   (querying by an attribute that isn't a prefix of the key), a real SQL table with real
   indexes is the only way to serve that without an in-process full scan.
3. **Measured evidence the K/V path is the bottleneck, after generic fixes are exhausted.**
   Benchmark first (see [docs/dev/performance.md](./docs/dev/performance.md)), and try the generic
   fixes first — `Scan` instead of N `Get`s, the dedicated read pool, dirty-overlay flush
   thresholds — before concluding the K/V path itself is the problem. `sqs:messages`,
   `cloudwatch:metricdata`, `kinesis:records`, and most other high-volume namespaces are
   row-shaped and fine in the generic store today; don't graduate a namespace speculatively.

Graduating a namespace to a dedicated table obligates the author to build all of the
following — treat this as the real cost of graduating, not an afterthought:

- **Dual backends** — a memory-mode implementation (in-process maps/slices, zero JSON
  overhead) and a SQLite-mode implementation, selected at startup based on the underlying
  `state.Store`. See
  [internal/services/dynamodb/item_store.go](./internal/services/dynamodb/item_store.go)
  (`memItemBackend` / `sqlItemBackend`) or
  [internal/services/cloudwatch/logs/event_backend.go](./internal/services/cloudwatch/logs/event_backend.go)
  (`memEventBackend` / `sqlEventBackend`) as the two existing worked examples. Memory mode
  must keep full functional parity — it is not a second-class fallback.
- **A migration registered per the reserved version-range convention** in
  [internal/state/migrate.go](./internal/state/migrate.go) — see
  [Writing a migration](#writing-a-migration) below.
- **A `router.DebugStateProvider` implementation**, so the new table stays visible to
  `/_overcast/debug/state` and resettable via `/_overcast/reset`. The raw state debugger only enumerates
  the generic `kv` store by default, so a dedicated table is otherwise invisible to it and
  immune to reset. Implement
  `DebugNamespace`/`DebugStateKeys`/`DebugStateValues`/`DebugResetState` (see
  [internal/router/debug.go](./internal/router/debug.go)'s `DebugStateProvider` interface)
  and register the provider with the router. DynamoDB's and CloudWatch Logs' `Service`
  methods of the same names are the two existing worked examples.
- **Its own write buffering, if writes are hot** — a dedicated table does not automatically
  inherit `HybridStore`'s async flush behavior; if the table sees frequent writes, the
  service owns batching them (see CloudWatch Logs' per-stream unflushed-event write buffer
  in `event_backend.go`).

### Writing a migration

Migrations are registered once, from a package `init()`, via
[`state.RegisterMigration`](./internal/state/migrate.go). See
`internal/services/cloudwatch/logs/migrations.go` and
`internal/services/dynamodb/migrations.go` for two complete worked examples — a
schema-plus-data-conversion migration and a schema-only migration, respectively.

**Claim a version range.** `internal/state/migrate.go`'s `Migration` doc comment is the
authoritative list of reserved ranges — read it before picking a version, and update it in
the same PR that claims a new decade. As of this writing: 1-9 is `internal/state` core (the
`kv` table, `auto_vacuum`), 10-19 is CloudWatch Logs (`logs_events`, versions 10-11 used),
20-29 is DynamoDB (`dynamodb_items` / `dynamodb_stream_records`, versions 20-21 used), and
30+ is free. Claim the next unused decade for a new dedicated table, and leave a comment
there explaining what you claimed — a duplicate `Version` value panics at binary startup (a
programmer error to catch immediately, not a runtime condition calling code should handle).

**`Up` runs inside the wrapping transaction — respect that.** `Up` receives a `*sql.Tx`, not
a `*sql.DB`. Most DDL and DML is fine inside a transaction, but SQLite refuses a handful of
statements there — `VACUUM` is the one you're most likely to hit ("cannot VACUUM from within
a transaction"). If your migration needs one of those, split it: do the transactional part
(e.g. a `PRAGMA` that merely requests a mode change) in `Up`, and do the actual
transaction-refusing statement in `AfterCommit`, which runs against the raw `*sql.DB`
immediately after `Up`'s transaction commits. Copy the pattern from `migrate.go`'s own
`auto_vacuum` migration (`migrationAutoVacuumVersion`): `Up` sets
`PRAGMA auto_vacuum = INCREMENTAL` transactionally; `AfterCommit` runs `VACUUM` afterward,
since auto_vacuum mode has no effect on an existing non-empty database until a `VACUUM`
actually runs.

**New table vs. inline fallback is the same decision as JSON compatibility, just phrased the
other way round.** If you're here because you're adding a *new* dedicated table (see
[Data earns a table](#data-earns-a-table) above), you need a migration — there is no
generic-store alternative once the data has its own schema. If you're here because you're
*reshaping* an existing blob in the generic `kv` store, re-read
[JSON compatibility](#json-compatibility-evolving-a-persisted-struct) first: a migration is
only the right tool when the data is moving to a new storage location, or the old and new
shapes genuinely can't coexist in the same key. Don't reach for a migration to reshape a blob
that could just be normalized lazily on read.

**Idempotency: `Up` must be safe to run against a database that already has the schema it
creates.** Databases created before the migration runner existed — or before your specific
migration was added — need to adopt your schema cleanly the first time they run under the
runner, not fail because the table is already there from an old ad-hoc `CREATE TABLE`. Use
`CREATE TABLE IF NOT EXISTS` (and `CREATE INDEX IF NOT EXISTS`). Migration #1
(`migrationKVTableVersion`) is the canonical example — it recreates the bare `kv` table so a
database created before the runner existed adopts version 1 transparently on first open — and
the CloudWatch Logs table migration (`migrationLogsEventsTableVersion`) does the same for
`logs_events`.

**There are no down-migrations.** The runner takes a file-copy backup
(`<dbPath>.bak-v<fromVersion>`) before the first pending migration runs against a database
that already has schema (`backupBeforeMigration` in `migrate.go`) — restoring that backup
file by hand is the only rollback story. Do not build migration-reversal tooling.

**Testing.** Follow the shape in
[internal/state/migrate_test.go](./internal/state/migrate_test.go) for the runner's own
mechanics (fresh database, a pre-existing bare-`kv` database adopting cleanly, an idempotent
no-op on a second run, a failed migration leaving `user_version` unchanged and later
migrations never running) and
[internal/services/cloudwatch/logs/migrations_test.go](./internal/services/cloudwatch/logs/migrations_test.go)
for a real data-conversion migration (valid blobs converted, malformed blobs skipped and
logged per the malformed-persisted-state rule, a no-op when there's no legacy data to
convert).

For a comparison of what each `state.Store` backend does with this data once it's written —
durability, memory residency, read/write performance, and known limitations — see
[docs/dev/storage-backends.md](./docs/dev/storage-backends.md).

---

## Performance and safety

> Performance is not a phase — it is a property of every line of code.

**Targets:** <15 MiB idle memory. Watch for unexpected jumps in Docker image size — the full image is ~96 MB and the slim image ~36 MB; large increases should be justified (e.g. a new runtime dependency) and called out in the PR.

- Avoid allocations in hot paths — `json.Marshal` not `bytes.Buffer`+encoder.
- Pre-size slices: `make([]string, 0, n)`.
- **Stream data-heavy operations** — any operation that reads or writes large/unbounded data (object bodies, batch responses, scan results, log tails) **must** use `io.Reader`/`io.Writer` pipelines. Loading everything into memory first (`io.ReadAll`, `bytes.Buffer`) is only acceptable when the data is provably small and bounded. Prefer `io.Copy`, `json.NewDecoder(r.Body)`, and chunked writes over accumulate-then-send patterns.
- Measure before optimising: `make bench`.
- **Document measurement conditions for every performance claim.** A number without context is misleading. See [docs/dev/performance.md § Documenting performance claims](docs/dev/performance.md#documenting-performance-claims) for the required fields (what, how, environment, inclusions/exclusions).
- **Respect the startup budget.** No store reads, network I/O, DDL, file reads, or eager goroutine work in service `New()` or any `Init*` method called from `router.New()`. Use the `sync.Once` lazy-init pattern. See [docs/dev/performance.md § Startup budget — rules for service authors](docs/dev/performance.md#startup-budget--rules-for-service-authors).

**Goroutine leaks** — every goroutine must respect context cancellation:

```go
select { case msg := <-ch: ...; case <-ctx.Done(): return }
```

Also: always `defer ticker.Stop()`, always pass `r.Context()` to blocking calls.

**Cross-platform:** use `filepath.Join` (not string concat), `os.TempDir()` (not `/tmp`), no CGO (`modernc.org/sqlite`), no shell scripts in the build pipeline.

---

## Design patterns

| Pattern              | Where                                               | Purpose                                              |
| -------------------- | --------------------------------------------------- | ---------------------------------------------------- |
| Strategy             | `lambda.Runtime` interface                          | Swap runtimes without changing Lambda handler        |
| Registry             | `router.allServices`                                | Append to add a service; nothing else changes        |
| Repository           | `services/*/store.go`                               | Typed domain access; JSON serialisation in one place |
| Middleware chain     | `internal/middleware/`                              | RequestID -> Recovery -> Logger -> SigV4 -> service  |
| Dependency injection | `router.New(cfg, store, logger, clk, [hookRunner])` | No globals; everything testable                      |
| Functional options   | `tests/helpers.Option`                              | Flexible test server configuration                   |
| Observer (planned)   | `internal/events/`                                  | SNS->SQS, SQS->Lambda event pipelines                |
| Host-route table     | `internal/middleware/hostroute.go`                  | Host-subdomain (execute-api/lambda-url/appsync-api) → path-style route |

---

### Host-based (subdomain) routing

Some AWS services address a resource via the request's **Host header**
rather than its path — `{id}.execute-api.{region}.amazonaws.com`,
`{urlId}.lambda-url.{region}.on.aws`, `{id}.appsync-api.{region}.amazonaws.com`,
and (on a parallel track) S3 virtual-hosted buckets. Overcast recognises this
grammar with one shared parser and dispatch table,
[`internal/middleware/hostroute.go`](./internal/middleware/hostroute.go):

```
{id}.{label}[.{region}].{base}[:port]
```

`ParseHostRoute` matches a fixed `label` vocabulary (`hostRouteLabels`) and
returns the `{id}` (everything before the label — dot-joined, so dotted IDs
work) and `{region}` (only when the segment after the label looks like an AWS
region). `HostRouteService` exposes the same label→service map to
`internal/middleware/logger.go`'s `detectService`, so a request's log label
can never drift from what it was actually routed to — there is exactly one
place that knows the label vocabulary.

The dispatch table itself (`[]middleware.HostRouteRow`, each a `Label` +
`Rewrite func(r *http.Request, m HostRouteMatch)`) is **not** a package
global — it's built once in `router.New()` (`internal/router/router.go`,
"Host-based routing" section) after the owning services exist, because a
`Rewrite` closure typically calls back into its service (e.g.
`apigwSvc.HostRouteRewrite`). It's registered early via the same
declare-a-pointer-populate-later pattern already used for `queryDispatchers`
and the event `bus` (chi requires all `r.Use` calls before any route is
registered, but the services those rows call into don't exist yet at that
point in `New()`).

**Adding a new host-routed service costs exactly one row:**

1. Add its `"label"` → service-name entry to `hostRouteLabels` in
   `internal/middleware/hostroute.go`.
2. In `router.go`'s "Host-based routing" section, append one
   `middleware.HostRouteRow{Label: "...", Rewrite: yourSvc.HostRouteRewrite}`
   to `hostRoutes`.
3. Implement `HostRouteRewrite(r *http.Request, m middleware.HostRouteMatch)`
   on your service. Keep it thin — a path rewrite onto a route your service
   already registers (see `appsync.Service.HostRouteRewrite` for the
   simplest case: pure string rewrite, no store lookup) or, when Host alone
   is genuinely ambiguous about which existing route applies (see
   `apigateway.Service.HostRouteRewrite` / `ExecuteByHost`: a Host only
   carries `{apiId}` + `{region}`, not whether that ID is a REST or HTTP API,
   or where the stage boundary falls in the path), rewrite onto a small
   marker route in your own package that resolves the ambiguity using your
   package's own store and then re-enters your existing path-style handler
   via a synthetic `chi.RouteContext` (see `handler_host_execute.go`) — never
   duplicate protocol logic in the row itself.

S3's virtual-hosted addressing (`internal/middleware/s3virtualhost.go`)
already fits this grammar (label `s3`, region optional) but is deliberately
**not** folded into the table yet — see the doc comment at the top of
`hostroute.go` for why (a parallel branch is actively changing it) and treat
migrating it in as a follow-up once both land.

User-facing setup (wildcard DNS, `OVERCAST_HOSTNAME`, what's supported today)
is documented in [docs/networking.md](./docs/networking.md).

---

## CloudFormation integration

Every service that creates resources must have corresponding **CloudFormation resource
handlers** so that those resources can be provisioned via `cdk deploy` (or raw
`CreateStack`). This is not optional — CloudFormation is the primary way CDK users
interact with AWS, and if a resource type lacks a handler, CDK stacks that use it will
fail.

### How it works

The CloudFormation provisioner lives in `internal/services/cloudformation/provisioner.go`.
It maintains a `resourceHandlers` map from CloudFormation resource type strings
(e.g. `"AWS::SQS::Queue"`) to `resourceHandler` implementations. Each handler has two
methods:

```go
type resourceHandler interface {
    Create(ctx context.Context, cfnRouter chi.Router, cfg *config.Config,
           props map[string]interface{}, rCtx resourceContext) (physicalID string, attrs map[string]string, err error)
    Delete(ctx context.Context, cfnRouter chi.Router, cfg *config.Config,
           physicalID string, rCtx resourceContext) error
}
```

Handlers dispatch internal HTTP requests through the emulator's own router (via
`httptest.ResponseRecorder`), so they exercise the real service implementation. Three
dispatch helpers exist:

| Helper            | Protocol       | Used by                               |
| ----------------- | -------------- | ------------------------------------- |
| `internalQuery`   | Query/XML      | EC2, IAM                              |
| `internalJSON`    | JSON target    | ECS, EventBridge, KMS, Step Functions |
| `internalRequest` | REST path/JSON | API Gateway, Lambda, S3               |

### Thin orchestration layer

CloudFormation handlers should be as thin as reasonably possible. Their job is to translate
resolved CloudFormation properties into the underlying AWS service API, capture the
CloudFormation-visible outputs (`Ref`, `Fn::GetAtt`, physical IDs), and apply documented
CloudFormation lifecycle semantics such as replacement-vs-update and deletion ordering.

Do not duplicate behavior already owned by the target service. Validation, defaulting,
state writes, generated IDs, events, lifecycle transitions, execution behavior, and modeled
service errors should come from the service handler whenever possible. A CloudFormation
resource handler should call through the emulator router using `internalQuery`,
`internalJSON`, or `internalRequest` rather than writing directly to the service store or
reimplementing service logic.

CloudFormation handlers may add CloudFormation-specific validation or translate underlying
service errors only when that makes observable behavior closer to real AWS CloudFormation.
Examples include rejecting an invalid resource property before dispatch because AWS
CloudFormation does, mapping a service error into the failure shape/status CloudFormation
would expose during stack events, or enforcing CloudFormation replacement rules that differ
from the service's update API. Do not add stricter validation or friendlier errors merely for
local convenience; every extra check should be justified by AWS CloudFormation docs,
CDK-emitted templates, existing compatibility tests, or documented real-AWS evidence.

Be DRY about transport and response plumbing, not about AWS semantics. Shared helpers are
appropriate for request dispatch, property-copy boilerplate, physical-ID splitting, and
small response attribute builders. Keep resource-specific property mappings, `Ref`/`GetAtt`
semantics, replacement rules, and intentional emulation gaps explicit near the handler so
they remain easy to review against AWS CloudFormation docs.

### Forwarding properties — the allow-list is data, and its leftovers are reported

A resource type has more properties than any handler forwards. For years every
handler named the ones it wanted one `if` at a time, so a property nobody
thought of was dropped in silence and the stack still went green.
[#540](https://github.com/overcast-sh/overcast/issues/540) found that in
twenty-odd services — three of them where the dropped property chose a Docker
image, so the emulator started the wrong engine and looked like it had
succeeded.

The answer is **not** a blind pass-through of the whole property map, even
though CloudFront's `DistributionConfig` survives intact by being one. That
works there because the template's shape and the API's input shape coincide,
and usually they do not: `Tags` is a `[{Key,Value}]` list in most resource types
and an object in a few (`AWS::EKS::*`, `AWS::MSK::Cluster`); property names are
PascalCase against lowerCamel members; some properties have no API member at
all. An allow-list is the honest description of what a handler can do — it just
must not be invisible. So `provisioner_properties.go` gives handlers two
things:

- **`forwardProperties(props, body, names...)`** — the allow-list as a list of
  names. It converts each name, and any nested object's keys, from the
  template's PascalCase to the member spelling. Use **`forwardPropertiesAs`**
  for values whose keys must *not* be converted, which is any map of the user's
  own keys — a nodegroup's `Labels`, for instance, where converting would
  rewrite the data rather than the member name.
- **`noteUnconsumedProperties(ctx, resType, props, consumed...)`** — everything
  the handler did not claim becomes an emulation limitation on the resource,
  which CloudFormation already surfaces as `ResourceStatusReason` (see
  `limitation.go` and `internal/protocol/limitation.go`). A dropped property now
  appears in `cdk deploy` output beside the resource it was dropped from. Call
  it from `Create` only: on `Update`, a property that is legitimately not
  re-applied is not a gap.
- **`cfnTagMap(props["Tags"])`** reads either tag shape into the `{key: value}`
  map most APIs model; merge it with `mergeStackTags(rCtx.StackTags, …)` so
  stack tags propagate. A type that forwards `Tags` must also join
  `stackTagPropagationResourceTypes` or `stackTagPropagationExclusions` —
  `TestStackTagPropagationCoverage` fails otherwise.

A handler that uses both cannot drop a property silently: it is either
forwarded or reported. **Adopt them when you touch a handler.** The
per-property judgement — what the service accepts, what it would reject, what
needs a shape translation because the two models genuinely differ — stays next
to the handler where a reviewer can check it against the AWS docs.

### Rules

1. **Every resource-creating endpoint must have a CloudFormation handler.** When you add a
   new resource type to a service (e.g. a new `CreateFoo` operation), register a handler in
   `resourceHandlers` at `internal/services/cloudformation/provisioner.go:1233`. If a
   service already has CF handlers for some resource types but not the one you're adding,
   add the missing entry. If the service is entirely absent from `resourceHandlers` (e.g.
   EKS, MSK, Route53), create the handlers and register them — even stub handlers are
   better than nothing.
2. **Physical IDs must match AWS format.** Use the same identifier AWS returns
   (ARN, ID with correct prefix, URL, etc.).
3. **Return `GetAtt` attributes.** If the resource type supports `Fn::GetAtt`, return the
   relevant attributes from `Create` so that cross-resource references resolve correctly.
4. **Implement `Delete`.** Stack deletion must clean up all provisioned resources.
5. **Call the underlying service.** Real handlers must dispatch through the emulated service
   API instead of directly mutating stores or duplicating service validation/defaulting.
   Direct store access is only acceptable when there is no service API to represent the AWS
   behavior, and the limitation must be documented and tested.
6. **Keep CloudFormation semantics explicit.** `Ref`, `Fn::GetAtt`, physical IDs, update
   replacement rules, CloudFormation-only validation/error translation, and no-op deletes
   are CloudFormation behavior. Implement them in the resource handler only when needed to
   match AWS-observable behavior, and verify them against AWS docs, CDK-emitted templates,
   compatibility tests, or real-AWS evidence.
7. **Stub what you can't implement yet.** If a resource type is recognised by CDK but not
   yet fully supported, use `&stubResourceHandler{}` — this returns a synthetic physical ID
   so the stack can still complete. Never silently ignore an unknown resource type.
8. **Handler files live in the cloudformation package.** Group handlers by service:
   `provisioner_ec2.go`, `provisioner_apigw.go`, `provisioner_ecs.go`,
   `provisioner_resources.go` (for smaller services).

### Verifying CF compliance when adding a service or endpoint

When you add a new service or a resource-creating endpoint, verify that:

1. The `resourceHandlers` map in `provisioner.go` has an entry for the corresponding
   CloudFormation resource type (e.g. `"AWS::SQS::Queue"`)
2. The handler's `Create` method dispatches an internal HTTP call through the emulator's
   router — it should exercise the real service implementation, not short-circuit
3. The handler does not duplicate validation, defaulting, ID generation, persistence, or
   execution behavior that belongs to the service package, except for CloudFormation-specific
   validation/error translation required to match AWS-observable behavior
4. `Delete` is implemented and removes the resource through the service API, or the no-op
   behavior is documented because AWS exposes no delete API and parent-resource cascade owns
   cleanup
5. The physical ID format matches what AWS returns (check the real AWS documentation)
6. `Ref` and `Fn::GetAtt` behavior is tested with CDK-like templates when CDK relies on
   those values for child-resource wiring
7. If the service uses a new dispatch protocol not covered by the three existing helpers
   (`internalQuery`, `internalJSON`, `internalRequest`), add the new helper to
   `provisioner.go`

**Every emulated service** that creates resources now has at least a stub handler in the
`resourceHandlers` map. Real (non-stub) handlers dispatch to the emulated service
implementation via the appropriate protocol helper and return the correct physical ID
and `Fn::GetAtt` attributes. See
[provisioner_json_coverage.go](../internal/services/cloudformation/provisioner_json_coverage.go)
and
[provisioner_query_rest_coverage.go](../internal/services/cloudformation/provisioner_query_rest_coverage.go)
for the most recently added handlers.

**Service implementation tiers and CF handler requirements:** Every service that reports
itself as `StatusSupported` or `StatusPartial` in `capabilities_dev.go` must be at least
**inert tier** (defined in [Service implementation tiers](#service-implementation-tiers) —
resources exist as metadata, can be created/listed/updated/deleted as real AWS would, but
don't "do" anything). The CF provisioner must keep pace: when a service reaches
inert tier, its CF handlers must create real resources through the emulated service,
not just return synthetic stub IDs. When a service advances to **partial** or **full**
tier (resources have real side effects — e.g. Docker containers, actual message
delivery), the CF handlers should reflect that by passing through all relevant
configuration properties.

---

## Testing

Tests use the **Given/When/Then** pattern. Full test conventions are in
[tests/AGENTS.md](./tests/AGENTS.md).

```go
func TestGetObject_notFound(t *testing.T) {
    // Given: a bucket with no objects
    srv := helpers.NewTestServer(t)
    createBucket(t, srv, "empty-bucket")

    // When: we GET a non-existent key
    resp, err := http.DefaultClient.Do(get(srv, "/empty-bucket/missing.txt"))
    require.NoError(t, err)
    defer resp.Body.Close()

    // Then: we get a well-formed NoSuchKey error
    helpers.AssertStatus(t, resp, http.StatusNotFound)
    helpers.AssertXMLError(t, resp, "NoSuchKey")
    helpers.AssertRequestID(t, resp)
}
```

---

## Versioning and changelog

We use [Semantic Versioning](https://semver.org/). Version bump rules:

| Change                                                       | Bump  |
| ------------------------------------------------------------ | ----- |
| Breaking API change (env var rename, response format change) | MAJOR |
| New endpoint, new service, new feature                       | MINOR |
| Bug fix, performance improvement, documentation              | PATCH |

**Every PR that changes shipped runtime behaviour must add a changelog
fragment under `.changelog/`.** Do not edit the `[Unreleased]` section of
`CHANGELOG.md` — it stays empty between releases, and CI
(`python3 scripts/changelog.py check`) fails any PR that writes into it.
Fragments are one file per PR at a unique path, so concurrent PRs can never
merge-conflict over the changelog; at release time they are curated into the
new versioned section of `CHANGELOG.md` and deleted. Each line is one entry —
`<+|-|~|*|section>[!|.] [area] <prose>` — and carries its own category, scope
and compatibility marker. Write them with `python3 scripts/changelog.py new`
rather than by hand; `.changelog/README.md` documents the grammar, and in
particular when a change has to be marked breaking.

The changelog is used as the basis for GitHub release notes. Keep it focused on
changes users need to know about when they install or run Overcast: new services,
new endpoints, AWS compatibility fixes, user-visible bug fixes, config/env var
changes, Docker/binary packaging changes, performance changes with measured
conditions, and documentation that materially changes user guidance.

Do not add changelog fragments for purely internal development changes unless
they affect shipped artifacts or runtime behaviour. Examples that usually do not
belong in release notes: CI-only refactors, test-only changes, local tooling,
code cleanup, non-user-visible refactors, and workflow maintenance.

Add your fragment as `.changelog/YYYYMMDD-<slug>.md` (full format and naming
rules in [.changelog/README.md](./.changelog/README.md)). Fragments have no
frontmatter — every line stands alone and carries its own category and scope:

```markdown
* [sqs] `ReceiveMessage` now applies `VisibilityTimeout` as documented
```

**A PR with no fragment has to say that is deliberate.** The `Changelog entry`
check fails any PR that adds nothing under `.changelog/`, because a forgotten
fragment and a fragment nobody needed are the same empty diff. Clear it by
commenting `/no-changelog <reason>` on the PR — the reason is required and is
kept as the record of the decision. `/needs-changelog` puts the question back.

A PR is passed without being asked when every file it touches is in an area
whose contents cannot reach a user — `compat/`, `tests/`, test files anywhere,
`docs/plans/`, `docs/dev/`, `.agents/`, the editor and dev-container config,
contributor docs, and local tooling. One file outside them and the question is
asked. Never edit `CHANGELOG.md` to clear the check: that file belongs to the
release PR, which the bot keeps current by merging `main` into its branch on
every push, and a second hand in the same section aborts that merge. While a
release PR is open, add the fragment as usual and the bot folds it in. Both
rules, with the reasoning, are in
[.changelog/README.md § When a change needs no
fragment](./.changelog/README.md#when-a-change-needs-no-fragment).

---

## Refreshing the AWS API models

`models/aws/VERSION` pins the public
[`aws/api-models-aws`](https://github.com/aws/api-models-aws) Smithy corpus used
to generate `internal/awsapi/manifest.gen.go`. The generated manifest and
runtime indexes are committed; the raw model checkout is not.

The `AWS API model refresh` workflow checks upstream weekly and can be started
manually from GitHub Actions. It also runs when a push to `main` changes the
model inputs while a refresh PR is open, so that PR is regenerated on the new
`main` instead of sitting conflicted until the next weekly run; a push never
opens a PR. When a new revision exists, it regenerates the
manifest, runs the model and routing gates, and creates or updates one PR from
`automation/aws-api-models`. It resets and force-with-lease updates only that
dedicated branch and never merges the PR.

A refresh is not inert at runtime — `restFallback` switches on whether the
pinned corpus claims a request, so an added operation takes its path off the S3
fallback, a protocol-trait change moves an operation's error envelope, and a
binding becoming shared by several services drops the credential-scope check
that an unshared one gets. `awsmodelgen -changelog-output` therefore writes a
changelog fragment into the same commit as the revision bump whenever one of
those categories moved, derived from the same inventory diff the PR body is
built from. When none of them moved, no fragment is written and the workflow
comments `/no-changelog` naming what it checked. Neither answer is a default:
both are the generator's reading of the diff.

The workflow authenticates as the repository's release GitHub App
(`RELEASE_APP_CLIENT_ID` / `RELEASE_APP_PRIVATE_KEY`), the same App that opens
release PRs, and fails immediately if those secrets are missing. It cannot use
[`GITHUB_TOKEN`](https://docs.github.com/en/actions/concepts/security/github_token):
GitHub refuses `createPullRequest` from Actions unless the repository's
[Actions settings](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository)
allow it, and even then a PR opened as `github-actions[bot]` starts no workflow
runs — the PR would sit on required checks that never run. The App needs
Contents: read & write and Pull requests: read & write, and nothing else.

To regenerate locally you need a checkout of the revision recorded in
`models/aws/VERSION`. `go run ./scripts/aws-models.go --ensure` fetches
exactly that revision into a per-user cache (`os.UserCacheDir()`, not a
directory inside the repo — this project routinely has ten or more worktrees
checked out at once, and a fetch per worktree would mean that many copies of
a large third-party tree) and prints the models directory on stdout, so it
composes directly into `AWS_MODELS_DIR`:

```sh
make generate-aws-operations \
  AWS_MODELS_DIR="$(go run ./scripts/aws-models.go --ensure)"
make aws-models-check \
  AWS_MODELS_DIR="$(go run ./scripts/aws-models.go --ensure)"
```

A cache hit touches no network — completeness is tracked by a sentinel file
written only once a checkout fully succeeds, so an interrupted fetch is redone
rather than trusted. Add `--service <name>` (repeatable) to fetch only that
service's models via a sparse checkout, which widens in place if you ask for
another service later; omit it for the full checkout the commands above need.
`--prune` deletes cached revisions other than the one currently pinned. Pass
`--source` (or set `OVERCAST_AWS_MODELS_SOURCE`) to point at a mirror instead
of the public repository named in `models/aws/VERSION`. See
`go run ./scripts/aws-models.go --help` for the rest of the flags.

The generator rejects a checkout whose `HEAD` differs from the pin. Supplying
`AWS_MODELS_DIR` to `aws-models-check` adds a byte-for-byte regeneration check;
without it, the same target remains a no-network validation of the committed
corpus and ownership indexes. Never hand-edit or hand-merge the generated
manifest.

---

## How to add an endpoint

> ### Where the route may live — one rule, enforced in CI
>
> **Every path Overcast serves on the AWS API listener is either a binding the
> pinned AWS manifest models, or it lives under `/_overcast/`. There is no
> third category.**
>
> - **Emulating an AWS operation?** Serve it at the method and URI the model
>   binds. Look the operation up in `internal/awsapi/manifest.gen.go` and copy
>   its `URI` — do not infer it from the AWS docs, and do not invent one.
> - **Anything else** — health, metrics, debug, per-service admin APIs, the
>   data plane of an emulated workload — goes under
>   `/_overcast/<service>/<resource>/…`.
>
> The prefix is reserved by an S3 naming rule rather than by convention: a
> bucket name cannot begin with an underscore, so no request AWS models can
> ever collide with it. It is the only prefix with that property, which is why
> there is one rather than the sixteen roots it replaced.
>
> **Do not nest an invented sub-resource inside a modeled prefix.** A path like
> `/2015-03-31/functions/{name}/source` reads as an AWS binding and is not one.
> That is worse than an obviously-ours path: it misleads anyone reading the
> routing table, and it collides the day AWS models that sub-resource. It is
> also invisible to a `grep` for `/_`, which is how several of these went
> unnoticed until the gate below reported them.
>
> **The gate:** `TestNoRouteIsRegisteredOutsideTheNamespace`
> (`internal/router`, `-tags dev`; run by `make aws-models-check`, which CI
> runs on every PR) walks every registered route and fails on anything that is
> neither modeled nor namespaced. It reports the offending pattern and what to
> do about it. If you have a genuine exception, add it to `nonManifestRoutes`
> with the reason recorded as the map value — the review question is always
> *"why is this not under `/_overcast/`?"*
>
> Two consequences worth knowing before you pick a path, because they are not
> only about naming: what a request is classified as for **tracing** and for
> **IAM enforcement** is decided from the path, by allowlists in
> `internal/trace` and `internal/middleware`. Moving a route can therefore
> change whether it appears in the trace UI and whether it is authorized. See
> [docs/plans/non-canonical-url-namespace.md](./docs/plans/non-canonical-url-namespace.md)
> §4.5 and §5 — both were found the hard way.

1. Write a **failing test** in `tests/integration/<service>/<service>_test.go` (GWT form)
2. **For new services using the typed pattern** (see [How to add a service](#how-to-add-a-service)):
   - Add request/response types and the codec-agnostic handler function to `typed_logic.go`
   - Register the operation in `typed_ops.go` via `op.NewTyped[In, Out]("OperationName", s.handlerTyped)`
   - The `Dispatch`/`DispatchQuery` method in `service.go` already routes through the typed dispatcher — no additional wiring needed
3. **For existing services with legacy dispatch** (see [Smithy wire protocols](./docs/dev/smithy.md)):
   - Add request/response types to `handler.go` — match AWS SDK wire format exactly (casing matters)
   - Add handler method; wire the route or dispatch case
4. Add state helpers to `store.go` if needed
5. If the endpoint creates a new resource type, add or update the CloudFormation resource handler in `internal/services/cloudformation/` — see [CloudFormation integration](#cloudformation-integration). Even if the service is not yet in `resourceHandlers`, you must at minimum register a `&stubResourceHandler{}` entry so that CDK stacks using the service don't fail. Register the handler in `resourceHandlers`, return the correct physical ID and `GetAtt` attributes, and implement `Delete`.
6. Update `capabilities_dev.go` for the service — change or add the `Capability` entry for this operation to `StatusSupported` (or `StatusPartial`/`StatusWIP` if incomplete).
   Keep this file current whenever operations are added, removed, renamed, or implementation status changes; this metadata is the source of truth consumed by capgen/docgen for generated service docs and `STATUS.md` coverage output.
   Then regenerate the static snapshot and refresh the docs:
   ```sh
   make generate-caps   # regenerates internal/capabilities/all.gen.go
   make docs            # rewrites docs/services/<service>/operations.md and the landing page's Operations stub
   make check-caps      # verifies all dispatcher entries have a matching capability
   make aws-models-check # verifies capabilities against the pinned AWS operation corpus
   ```
   `aws-models-check` reports `UNKNOWN_MODEL_OPERATION` when the service or
   operation name does not resolve through the generated AWS manifest. Correct
   a typo or add the appropriate modeled-name alias first. Use `DocOnly: true`
   only for documentation metadata that is not a dispatched AWS operation. For
   a deliberate emulator-internal helper or a legacy operation that AWS no
   longer models, add a narrowly scoped entry with a reason to
   `capabilityManifestExemptions` in `cmd/capgen/main.go`; do not weaken the
   global gate or mark an implemented AWS endpoint `DocOnly` merely to silence
   the check.

   > **Do not manually edit `docs/services/<service>/operations.md`, or the block between the `<!-- BEGIN overcast:capabilities -->` and `<!-- END overcast:capabilities -->` markers on the landing page.** Both are overwritten by `make docs`. Edit `capabilities_dev.go` and re-run `make docs` instead.
   >
   > **AWS Docs links** are auto-generated from the `serviceDocsBaseMap` in `cmd/capgen/main.go` — no per-operation `DocsURL` is needed for most operations. If a service is missing from that map, add it. Use the `DocsURL` field on a `Capability` entry only to override the link for a specific operation (e.g. when the URL pattern differs from the service base).
7. Add a changelog fragment under `.changelog/` (see [Versioning and changelog](#versioning-and-changelog))
8. Add the operation to `compat/suites/registry.json`, then implement it in **every** SDK/CLI compat suite (node-js-sdk, python-sdk, go-sdk, cli, java-sdk, dotnet-sdk, rust-sdk) — marking it `na` where an SDK has no API for it. `go run ./cmd/compat --check-parity` enforces this; see [compat/AGENTS.md § Baseline & uniformity policy](./compat/AGENTS.md#baseline--uniformity-policy)
9. **Web UI** — if the new endpoint exposes data the user would want to see or manage:
   - Update the service's list/detail pages in `web/src/features/<service>/` (or create them if they don't exist)
   - Add topology nodes/edges in `internal/router/topology.go` if the endpoint creates a new resource type that has relationships to other services
   - Wire SSE cache invalidation in `web/src/hooks/use-event-stream.ts` so the UI updates in real time when the resource is created/deleted
10. `make test` — all tests must pass with `-race`

> [!NOTE]
> **Windows / dev-container:** `go test -race ./...` (full workspace) can hang or be very
> slow when the source is on a Windows host volume (e.g. `E:\`) because the race detector
> rebuilds everything and every file I/O crosses the Hyper-V boundary. The Vite polling
> watcher makes this worse.
>
> Recommended workflow on Windows hosts:
>
> - During active dev, run targeted tests without `-race`:
>   `go test -count=1 ./tests/integration/s3/` etc.
> - Run the full race-enabled suite (`make test`) only before pushing/merging — ideally
>   inside the container where the filesystem is local:
>   `make container-test` (or `task container-test`).

Prefer `make container-test` over `docker compose -f docker-compose.dev.yml run --rm test`.
The `test` service is CPU-capped the same way the Go-in-Docker wrappers are, but a Compose
file cannot derive the cap itself: `--cpus=N` is rejected outright when N exceeds the CPUs
the daemon reports, so the number cannot be hardcoded, and `docker compose run` has no
`--cpus` flag to pass one through. So [scripts/container-test.sh](./scripts/container-test.sh)
(and its `.ps1` twin) computes it and exports `OVERCAST_GO_CPUS` / `OVERCAST_GO_TEST_P`,
which the Compose file substitutes into the service's `cpus:`, `GOMAXPROCS` and `GOFLAGS`.
The same `OVERCAST_GO_CPUS=0` opt-out applies. Calling `docker compose` directly still
works and is exactly as unbounded as it always was — it just does not get the cap.

---

## How to add a service

1. Create `internal/services/<n>/` with the standard file layout:
   - **`service.go`** — `Service` struct, `New`, route registration, `Dispatch`/`DispatchQuery` methods with codec dispatch
   - **`typed_ops.go`** — `typedOps()` returning `map[string]op.Operation` via `op.NewTyped[In, Out]` registrations; also `Operations()` and `SupportedProtocols()` for the `ProtocolService` interface
   - **`typed_logic.go`** — codec-agnostic handler functions (`func(ctx, *Input) (*Output, *protocol.AWSError)`) and the request/response types
   - **`store.go`** — state access, JSON serialisation
   - `handler.go` / `handler_stubs.go` — only if there is legacy dispatch code (existing services); new services should NOT create these files
   - `capabilities_dev.go` — `//go:build dev` operation inventory
2. **Wire trace log capture.** Every handler function that uses the service logger must opt into per-request log capture with one line at the top:
   ```go
   log := s.log.WithRecorder(ctx)  // or h.log.WithRecorder(r.Context())
   ```
   Then use `log.X(...)` instead of `s.log.X(...)` / `h.log.X(...)` in the handler body. This is zero-cost when `OVERCAST_DEBUG` is off (the Recorder is nil and `WithRecorder` returns the original logger unchanged). See `internal/trace/core.go` for the implementation and `internal/services/dynamodb/handler.go` for worked examples. The rule is simple: **every handler function with `h.log.X` / `s.log.X` calls must start with `WithRecorder`**.
3. **All new services must use the typed dispatch pattern from the start** (see [Smithy wire protocols](./docs/dev/smithy.md)). The `Dispatch` (or `DispatchQuery` for Query-protocol services) method must check `codec.FromContext(ctx)` at the top and route to the typed handler when a codec is present. The legacy `handler.go` / `handler_stubs.go` split only exists for older services that predate the codec infrastructure — do not create these files in new services. See `internal/services/scheduler/` (REST-path) or `internal/services/ecr/` (JSON-target) as canonical examples.
4. Implement `router.Service` interface; append to `allServices` in `internal/router/router.go`. For JSON-protocol services implement `router.TargetDispatcher` (`TargetPrefix()` + `Dispatch()`). For Query-protocol services implement `router.QueryDispatcher` (`DispatchQuery()`). For REST-path services implement `PathPrefixService`.
5. **Respect the startup budget.** `<svc>.New()` and any `Init*` method called from `router.New()` must be pure field assignment — no store reads, no network I/O, no DDL, no synchronous file reads, no goroutines that do work before their first tick. See [docs/dev/performance.md § Startup budget — rules for service authors](./docs/dev/performance.md#startup-budget--rules-for-service-authors) for the full rule set and the lazy-init pattern.
6. Create `internal/services/<n>/capabilities_dev.go` — declare every operation the service exposes, with the correct `Status` for each. Use `//go:build dev` at the top. See `internal/services/sqs/capabilities_dev.go` as the canonical example. Then generate and check:
   ```sh
   make generate-caps   # adds the new service to internal/capabilities/all.gen.go
   make docs            # writes docs/services/<n>/operations.md and the landing page's Operations stub
   make check-caps      # optional: only works for dispatcher-based services
   ```
7. Write P1 tests in `tests/integration/<n>/<n>_test.go`
8. Add CloudFormation resource handlers for every resource type the service creates — register them in `resourceHandlers` in `internal/services/cloudformation/provisioner.go`. If the service creates resources that AWS has CloudFormation types for (which is nearly always the case), you must add the entries. At minimum, use `&stubResourceHandler{}` for resource types you can't fully implement yet — this lets CDK stacks succeed while the implementation is incomplete. See [CloudFormation integration](#cloudformation-integration) for the full rules, dispatch helpers, and verification checklist.
9. Create `docs/services/<n>.md` following [docs/dev/service-doc-template.md](./docs/dev/service-doc-template.md) — an H1, a one-sentence positioning line, a `**Status:**` chip, `## Quick start`, then whatever of `## What works` / `## Differences from AWS` / `## Gotchas` you have something to say about, and `## Related` last. Add the sentinel markers (`<!-- BEGIN overcast:capabilities -->` / `<!-- END overcast:capabilities -->`) above `## Related` and run `make docs`: it fills in the `## Operations` stub and writes the per-operation tables to `docs/services/<n>/operations.md`. Everything between the markers, and that whole sub-page, is overwritten on every run — never edit either by hand. `make docs-check` fails on a page that breaks the structure. Follow [Writing docs](#writing-docs) for the prose — in particular, never cite `docs/dev/**` or `docs/plans/**`, which `make docs-check` rejects.

10. Add service to README.md table and add a changelog fragment under `.changelog/`
11. Add the service's groups and tests to `compat/suites/registry.json` covering all P1 operations, then implement them in **every** SDK/CLI compat suite — the per-suite file and registration table is in [compat/AGENTS.md § When a new Overcast service is implemented](./compat/AGENTS.md#when-a-new-overcast-service-is-implemented). Uniformity is enforced by `go run ./cmd/compat --check-parity`; any suite you cannot complete in the same PR must be declared in `compat/parity-debt.json` with a reason
12. **Web UI** — consider whether developers using Overcast would find it useful to see or administer this service's resources from the management console (most CRUD-style services qualify; internal plumbing like STS usually does not). If yes:

- Add an entry to `SERVICES` in `web/src/lib/service-registry.ts`. This is the **single registration point** — `nav-services.ts` (sidebar + search) and `dashboard.tsx` (dashboard cards) both derive from it automatically. Set the relevant fields:
  - `to`, `category`, `description` — required for sidebar navigation
  - `dashboardDescription` — longer card description (falls back to `description`)
  - `dashboardLabel` — alternate dashboard label (falls back to `label`, e.g. `"EC2 / VPC"`)
  - `docKey` — enables the docs button on dashboard cards
  - `nav: false` — omit from sidebar but still show a dashboard card (e.g. KMS, STS)
  - `dashboardCard: false` — omit from dashboard but still show in sidebar (e.g. WAF, CloudWatch)
- Create list and detail pages in `web/src/features/<n>/` and `web/src/routes/<n>/` (follow an existing service like SSM or KMS as a template)
- Add topology nodes and edges in `internal/router/topology.go` so the service appears on the system map with its resource relationships
- Add SSE event types and wire cache invalidation in `web/src/hooks/use-event-stream.ts` so the UI updates in real time
- Add an AWS SDK client factory in `web/src/services/aws-clients.ts`; if the service needs a custom BFF route (beyond simple JSON proxy), add a handler in `internal/bff/bff.go` and register it in `bff.NewHandler`

---

## Writing docs

Every file under `docs/` that isn't `docs/plans/` or `docs/dev/` ships on the
public site. The full rule set — one job per doc, a two-sentence intro
budget, no citing a file the site doesn't publish, tables over paragraphs —
is in [docs/dev/content-charter.md](./docs/dev/content-charter.md). Read it
before writing or substantially editing a published doc; `make docs-check`
mechanically enforces the citation rule and a frontmatter description length
cap, but the rest is a judgment call the charter exists to guide.

Service pages have a fixed shape on top of that —
[docs/dev/service-doc-template.md](./docs/dev/service-doc-template.md). A
landing page (`docs/services/<key>.md`) answers "does this work and what's the
one command" above the fold; the per-operation tables live on
`docs/services/<key>/operations.md`, which `cmd/capgen` generates. `make
docs-check` fails on a page that breaks the structure, so read the template
before adding or restructuring a service page.

---

## Service package structure

Within a service package, split files by **lifecycle stage and concern** — never by individual operation, never by using subfolders (subfolders = separate packages, which breaks access to private types).

### Typed pattern (required for all new services)

| File                  | Contains                                                                       |
| --------------------- | ------------------------------------------------------------------------------ |
| `service.go`          | `Service` struct, `New`, `Dispatch`/`DispatchQuery` with codec check at top    |
| `typed_ops.go`        | `typedOps()` → `map[string]op.Operation`, `Operations()`, `SupportedProtocols()` |
| `typed_logic.go`      | Codec-agnostic handlers (`func(ctx, *In) (*Out, *protocol.AWSError)`) + types  |
| `typed_ops_test.go`   | Handler unit tests (typed path)                                                |
| `store.go`            | State access, JSON serialisation                                               |
| `capabilities_dev.go` | `//go:build dev` — operation inventory                                         |

### Legacy pattern (existing services only — do not use for new services)

| File                  | Contains                                                                       |
| --------------------- | ------------------------------------------------------------------------------ |
| `service.go`          | `Service` struct, `New`, route registration                                    |
| `handler.go`          | Dispatcher methods + **fully implemented** handlers only                       |
| `handler_stubs.go`    | All `NotImplementedXML`/`NotImplementedQueryXML`/`NotImplementedJSON` stubs    |
| `handler_<group>.go`  | Implemented handlers for one feature group, when that group exceeds ~200 lines |
| `store.go`            | State access, JSON serialisation                                               |
| `types.go`            | Domain types and error constructors, when `store.go` grows large               |
| `capabilities_dev.go` | `//go:build dev` — operation inventory for MCP, docs, and coverage checks      |

**Rule: `handler.go` must never contain a stub.**
Stubs live in `handler_stubs.go`. When implementing an operation, _move_ its method body from `handler_stubs.go` into `handler.go` (or into the appropriate `handler_<group>.go`). This makes `handler.go` a complete, accurate inventory of what works — a reader should be able to tell at a glance what is implemented without scrolling past placeholder methods.

**Rule: new services must never create `handler.go` or `handler_stubs.go`.**
These files are artifacts of the pre-codec architecture. New services use `typed_ops.go` + `typed_logic.go` instead. The `typedOps()` map in `typed_ops.go` is the single source of truth for which operations are implemented — unimplemented operations are simply not registered. The `Dispatch`/`DispatchQuery` method returns 501 generically for any operation not found in `typedOp` (no per-operation stub needed). Both legacy and typed services declare capabilities in `capabilities_dev.go`, where unsupported ops are marked `StatusUnsupported` for documentation/coverage reporting.

**Rule: support metadata must be code-first, not prose-first.**
Human-written docs are important, but they are not a stable machine-readable source of truth for support status.

- The authoritative support inventory should live in code, close to the service implementation.
- For dispatcher-based services, the implemented operation registry in `handler.go` and the remaining stubs in `handler_stubs.go` are the current practical source of truth.
- As coverage reporting matures, each service should expose machine-readable support metadata (service name, operation list, implementation state, notes, tier, and optional CloudFormation/UI links) from code rather than relying on Markdown parsing.
- `capabilities_dev.go` is the authoritative per-service operation inventory today; keep it accurate at all times.
- capgen/docgen, MCP coverage tools, status surfaces, and generated docs consume that code-derived metadata or a generated manifest derived from it.
- Human-facing docs in `docs/services/` and summary files such as `STATUS.md` should be generated from or validated against the code-derived metadata in tests or checks.

Preferred direction:

- Keep prose for explanation, caveats, and usage notes.
- Keep support status in code.
- Fail CI when machine-readable support metadata and docs drift.

**Rule: do not add manual operation tables to service docs.**
`docs/services/<service>/operations.md` already contains a generated summary table and a per-endpoint breakdown (produced by `make docs` from `capabilities_dev.go`), and the landing page carries a generated coverage stub linking to it. Do not add a hand-written duplicate on the landing page — they will drift immediately and confuse contributors. If the generated table is missing a column or status you need, add it to `capabilities_dev.go` and extend `capgen`/`docgen` instead of writing a parallel table by hand.

**Rule: never hand-edit generated status/coverage tables.**
The following sections are generated and must only be updated via tooling:

- `STATUS.md` block between `<!-- BEGIN overcast:status -->` and `<!-- END overcast:status -->`
- `docs/services/<service>/operations.md` in full, and the block in `docs/services/<service>.md` between `<!-- BEGIN overcast:capabilities -->` and `<!-- END overcast:capabilities -->`

After changing capabilities or operation support, run:

```bash
make docs
```

This command runs the capgen/docgen pipeline that regenerates service capability blocks and the `STATUS.md` coverage table from `capabilities_dev.go`.

If you changed docs manually and are unsure whether generated sections drifted, re-run `make docs` before committing.

**Rule: Split `handler.go` only when a coherent group of implemented handlers exceeds ~200 lines.**
Split by feature group, not by HTTP method or operation name. Good split points:

| File                    | When to create it                                                                                               |
| ----------------------- | --------------------------------------------------------------------------------------------------------------- |
| `handler_multipart.go`  | CreateMultipartUpload, UploadPart, CompleteMultipartUpload, AbortMultipartUpload, ListParts are all implemented |
| `handler_versioning.go` | Versioning + lifecycle group is implemented                                                                     |
| `handler_tagging.go`    | Object/bucket tagging handlers are implemented                                                                  |

Never split `handler_stubs.go` — one stub file per service is always sufficient.

**Rule: Never use subfolders inside a service package.**
`internal/services/s3/buckets/` would require exporting `s3Store`, `errNoSuchBucket`, and every other private symbol. The cost always outweighs the benefit. Multiple files in the same package is the correct Go pattern (the standard library and the AWS SDK both do this).

---

## Web UI standards

### Linting — oxlint

`pnpm run lint` in `web/` is **`oxlint .`**, and that is the whole gate
(`make lint-web` and `scripts/verify-changed.sh` both call it, so there is one
entry point). **There is no ESLint in this repository.**

- **[`web/.oxlintrc.json`](./web/.oxlintrc.json) owns the rule set** — every rule,
  its severity, and the reasoning. It also loads
  `web/eslint-plugin-local` and `@tanstack/eslint-plugin-query` as oxlint JS
  plugins. `@tanstack/query/…` keeps its upstream spelling; this repo's own
  rules are `local/…`, named for where they come from rather than what they
  lint — the namespace was `classnames/…` until the plugin outgrew it.
  Type-aware rules run through `oxlint-tsgolint`, which embeds its own
  typescript-go, so they do not depend on the `typescript` devDependency.
- **New rules go in `.oxlintrc.json`.** There is nowhere else to put one.

ESLint was retired in [#1330](https://github.com/overcast-sh/overcast/issues/1330)
step 3. The four rules it was still running turned out to enforce nothing: three
are inert by construction, and `react-hooks/component-hook-factories` is
unreachable in `eslint-plugin-react-hooks` 7.0.1 (the React Compiler pass behind
it is gated off, and forcing it on makes the plugin swallow every diagnostic for
the file). `.oxlintrc.json`'s header has the full derivation. Removing ESLint is
also what unblocked TypeScript 7 — `@typescript-eslint/typescript-estree` crashed
at module load under it, whatever rules were enabled.

Suppression comments keep the `// eslint-disable-next-line <rule>` form — oxlint
honours them, and the rule names are unchanged. One caveat worth knowing: for the
React Compiler rules oxlint treats a suppression comment as an opt-out for the
whole enclosing function, where ESLint scopes it to the named rule on the named
line. Keep suppressions on the reported line, and do not assume a
`react-hooks/exhaustive-deps` disable is silencing only `exhaustive-deps`.

Editor: install `oxc.oxc-vscode` (in the workspace recommendations).
`.vscode/settings.json` pins `oxc.configPath` to `web/.oxlintrc.json` — required,
because the language server otherwise treats it as a nested config and ignores
its `options` block ([oxc#19937](https://github.com/oxc-project/oxc/issues/19937)).

### API access policy (SDK-first)

For Web UI data access, use AWS SDK clients wherever possible.

- Use service clients from `web/src/services/aws-clients.ts` for AWS API
  operations and AWS-compatible resources.
- Use direct `fetch` calls only for Overcast-specific extension
  endpoints (for example, `/_*` internal APIs such as topology,
  observability, or emulator-only tooling endpoints).
- Do not replace available AWS SDK calls with ad-hoc `fetch` wrappers;
  keep AWS-surface behavior and typing anchored to SDK clients.

### Frontend — Tailwind CSS v4

The web UI uses **Tailwind CSS v4**. When writing or editing component styles:

1. **Always prefer canonical Tailwind classes** over arbitrary-value syntax (`[…]`).
   - Good: `translate-y-0.5`, `gap-2`, `p-4`, `text-sm`
   - Bad: `translate-y-[2px]`, `gap-[8px]`, `p-[16px]`, `text-[14px]`
2. **Arbitrary values are a last resort** — only use square-bracket syntax when there is genuinely no canonical class available (e.g. a one-off brand colour or a value outside the default scale).
3. **Use Tailwind v4 syntax** — Tailwind 4 changed some conventions. Prefer `*:` (universal child selector variant) over `[&>…]` when targeting children. Consult the [Tailwind v4 docs](https://tailwindcss.com/docs) when unsure.
4. **Run the canonical upgrade** if you notice non-canonical classes: `pnpm dlx @tailwindcss/upgrade`.

### Tables — reach for `ResourceTable` before composing `<Table>` yourself

Any list of resources renders through **`ResourceTable`**
(`web/src/components/ui/resource-table.tsx`) — inside `ResourceListPage` or
`ResourceListSection` on an index page, or as `variant="embedded"` inside a detail page.
It owns the things every table in this UI must get right and that hand-rolled tables
keep getting subtly different: the loading / error / empty / "no matches" states (with
`isFiltered` + `onClearFilter` so a filter that finds nothing never reads as "this
doesn't exist"), row click and row actions, the delete flow, the mono-by-default cell
with `prose` opt-out, and the card/embedded framing. Its API is documented at the top
of the file; the 14 index pages converted in #1200 are the worked examples.

The engine also carries what used to send a page back to a hand-rolled table:
`rowClassName` for a row that has a tone of its own (a failure event, a log level),
`rowKey`'s second argument for a feed whose entries carry no id, `emptyExtra` for
content that belongs under the empty state, `expandedContent` for a detail panel that
opens under its own row (several at once), `interactive` on a column whose cell holds
a control, and a delete whose `getVars` builds whatever the mutation needs — a lock
token, an ETag, a composite `{apiId, routeId}` — with `canDelete` where only some rows
can be deleted at all. Two rules to know before writing the columns: a column sorts
only if it declares `sortValue`, and a timestamp sorts on a `Date` rather than on the
ISO string it arrived as; and a list that refetches on an interval declares
`defaultSort`, because the emulator's storage order is not stable across polls and a
row that moves under the cursor is worse than an order the API never promised. The
columns menu appears unasked only on a card table with five columns or more.

Composing `<Table>`/`<TableBody>` from `components/ui/table.tsx` directly is the
exception, not the default. Two kinds of table earn it: one that is not a resource list
at all (the IAM policy simulator's result grid, the debug page), and one whose shape
genuinely does not fit (`log-search-results`' virtualized stream). When you do, say why
in a comment at the call site — "ResourceTable didn't fit because …" — so the next
reader can tell a decision from an oversight, and disable
`local/prefer-resource-table` on the import with the same reason. That rule flags
any import of the table primitives under `features/**`, so an exception is recorded
twice on purpose: once for the reader, at the call site, and once for the linter, at the
import it needs. Fifty-five bespoke tables accumulated before that rule existed; their
migration, and `ResourceTable`'s move onto TanStack Table v9 (sorting, column
visibility, pagination on one engine), is
[#1327](https://github.com/overcast-sh/overcast/issues/1327). Do not add to the count.

### Attribute grids — `DefinitionCard`, not a two-column `<Table>`

The other half of that decision: when the thing on screen is **one resource's
attributes** rather than a list of resources, the answer is not a bespoke
`<Table>` either — it is **`DefinitionCard` / `DefinitionList` / `Definition`**
(`web/src/components/ui/definition-card.tsx`). A table of two columns whose left
column is a fixed set of field names is a definition list wearing a table's
markup: it has nothing to sort, hide, page or act on, and a screen reader gets
"row, Distribution ID, E1" instead of a term and its definition.

`Definition` owns the typography so no detail page has to restate it: the label
is the field-label spec from `lib/typography.ts` (the same spec `TableHead`
uses, so a detail-page label and a column header read alike), and the value is
**mono by default** because ARNs, ids, timestamps, counts and sizes are machine
output — `variant="prose"` is the marked exception for a sentence a human wrote.
Absence renders as an em dash, so a field that is unset stays visible instead of
vanishing from the grid. `copyable` puts the shared inline `CopyButton` beside a
value and names it after its label. The list is a container-query grid, so the
same markup is a vertical run inside a narrow card and a three-up grid on a
full-width page with no call site choosing.

Reach for it whenever a page shows "here are this thing's fields": the metadata
card a detail page opens with, a configuration tab, a dialog's file metadata, a
panel showing a single setting. Keep `StatusBadge`, `Timestamp`, `ArnText` and
the rest as the value node — the component owns typography and layout, not
content. Its adoption across the detail pages is tracked in
[#1101](https://github.com/overcast-sh/overcast/issues/1101).

### Topology map methodology

The system map is both a **diagnostic surface** and a **graph-based workspace** for
building, testing, and iterating on stacks. It is not a frame-perfect replay of
backend state. Its job is to make distributed behaviour legible at a glance: what
connected to what, what just happened, and how a developer can explore, tweak, and
reason about a stack by interacting with the graph directly.

That means the topology map should prefer **observability over literal timing** for
fast transient actions.

It also means map interactions should support fast iteration. The graph is not just a
read-only status board; it is a visual way to inspect resources, trigger actions,
follow relationships, and refine a stack while seeing the consequences in context.

Rules:

1. **Dilate only genuinely fast transitions.** If an action happens too quickly to be
   perceived in the UI, keep its visual state alive long enough to be seen.
2. **Do not slow states that are already human-visible.** Long-running or naturally
   observable states should render honestly and should not be artificially prolonged.
3. **Preserve sequencing even when dilated.** A node should still show the correct
   order of transitions (`visible` → `in-flight` → `done`, `idle` → `active`, etc.).
   Time dilation is for readability, not for inventing new lifecycles.
4. **Use one visual-state model per node type.** Counts, badges, row states, pulses,
   and ghost rows for the same resource should be derived from the same visual logic.
   Never let the header say one thing while the embedded detail list says another.
5. **Keep the AWS API truthful.** Time dilation belongs only in the map UI and other
   emulator-specific observability surfaces. Never change AWS-compatible API behaviour
   or backend state timing to satisfy the map.

Preferred techniques:

- **Ghost rows / tombstones** for recently removed items so deletes remain visible.
- **Short TTL pulses / edge glows / write flashes** for events that would otherwise be
  imperceptible.
- **Visual dwell windows** for extremely short intermediate states, such as a message
  that is received and deleted too quickly to ever be noticed as in-flight.
- **Client-side countdowns or decay** when the goal is to communicate that a transient
  state is draining away rather than disappearing instantly.

Node-level QOL guidance:

1. **Design each node around the most useful developer actions for that resource.**
   The node should expose the highest-value interaction a developer is likely to want
   in the middle of an iterative workflow.
2. **Prefer direct, contextual actions over navigation when the task is small and
   frequent.** If a developer commonly wants to send a message, publish a payload,
   invoke a function, inspect recent logs, or peek a queue, those actions should be
   considered for the node itself rather than forcing a page transition first.
3. **Keep actions intuitive and resource-native.** A node should feel like a compact,
   graph-local version of the service: queues send and receive messages, topics
   publish, functions invoke and show logs, log groups expose streams, and so on.
4. **Bias toward actions that help exploration, testing, and iteration.** Prioritise
   features that let a developer try something quickly, observe the result in context,
   and continue iterating without losing their place on the graph.
5. **Do not overload nodes with low-value controls.** Add actions deliberately.
   If an interaction is rare, destructive, configuration-heavy, or easier to
   understand on the dedicated resource page, keep it there.
6. **Surface state and action together when possible.** The best node interactions
   let a developer act and immediately see the effect in the same place.
7. **Treat node space as scarce.** Every always-visible bit of node UI should earn
   its place by being useful, sensible, and immediately understandable in context.

When deciding whether a node needs a QOL action, ask:

- What is the first thing a developer wants to do with this resource while looking at the map?
- Can that action be completed safely and clearly without leaving the graph?
- Will doing it on-node make the graph feel more useful as a stack-building and debugging workspace?
- Is the action common enough to justify persistent space in a compact node UI?
- Given the tight space inside nodes, does each surfaced detail justify occupying that space?

Examples:

- SQS messages may remain visually `in-flight` for a short dwell window on the map,
  then transition to a crossed-out `done` ghost row, even if the real delete already
  happened.
- SQS nodes may expose send-message and queue-peek interactions directly on the node
  because they are common, fast feedback loops during development.
- SNS nodes may expose publish-on-node because testing fan-out is a frequent graph-local action.
- Lambda nodes may expose test invoke and recent-log access because developers often
  want to trigger a function and inspect its effect without losing graph context.
- Lambda instances, event pulses, and write bursts may linger briefly after the raw
  event so developers can understand what just occurred.
- CloudWatch log stream activity dots may stay active for a short recent-activity
  window rather than dropping to idle immediately after a write.

When adding or changing topology-map behaviour, ask:

- Would a developer be able to notice this transition without slowing it down?
- If not, what is the smallest visual dwell that makes it understandable?
- Are the node badge, counters, list rows, and animations all telling the same story?

### Service home screen

Every service list/home page **must** include a `ServiceDocsButton` in its `PageHeader`
actions. This button opens the Overcast docs modal for the service, linking users to the
endpoint support matrix and AWS documentation.

```tsx
// In the component:
const [docsOpen, openDocs, closeDocs] = useDocsFromHash()

// In PageHeader actions (always first in the action group):
<ServiceDocsButton
  service="elasticache"   // matches the docs/services/<service>.md filename
  label="ElastiCache"
  open={docsOpen}
  onOpen={openDocs}
  onClose={closeDocs}
/>
```

See [function-list.tsx](web/src/features/lambda/components/function-list.tsx) as the
canonical example. Apply this to every new service's home component and retrofit it to
any existing service page that is missing it.

### Global search

Every service at **inert tier or above** (see
[Service implementation tiers](#service-implementation-tiers)) must register a search
contributor so its resources appear in the global search (⌘K / Ctrl+K).

1. Create `web/src/lib/search-contributors/<service>.ts` using `createSearchContributor`:

```ts
import { myService } from "@/services/api";
import { createSearchContributor } from "./create-contributor";
import type { MyResource } from "@aws-sdk/client-my-service";

createSearchContributor<MyResource>({
  id: "myservice",
  cacheKey: (ep) => [ep.baseUrl, ep.region, "myservice", "resources"] as const,
  fetchAll: () => myService.listResources(),
  matchFields: (r) => [r.name, r.arn],
  toResult: (r) => ({
    id: `myservice:${r.name}`,
    label: r.name ?? "",
    sublabel: r.arn,
    service: "My Service",
    serviceKey: "/myservice",
    type: "Resource",
    href: `/myservice/${encodeURIComponent(r.name ?? "")}`,
  }),
});
```

2. Import it in `web/src/lib/search-contributors/index.ts`.

The `cacheKey` **must** include `ep.baseUrl` and `ep.region` as the first two elements —
this matches the key shape produced by feature-level `data.ts` query options, so the
contributor can read from the cache without a network round-trip.

### service-registry.ts and unsupported-services.ts

When a service moves from "unsupported" to any implemented tier:

- **Remove** its entry from the `CATALOG` array in `web/src/lib/unsupported-services.ts`.
- **Add** a full entry to `SERVICES` in `web/src/lib/service-registry.ts` (including `to`, `category`, `description`). The sidebar (`nav-services.ts`) and dashboard card list (`dashboard.tsx`) both derive from this automatically.

---

## Commit conventions

[Conventional Commits](https://www.conventionalcommits.org/) format:

```
<type>(<scope>): <short description>

[optional body]

[optional footer — reference issues]
```

**Types:** `feat`, `fix`, `test`, `docs`, `refactor`, `chore`, `perf`

**Scopes:** `s3`, `sqs`, `dynamodb`, `sns`, `lambda`, `state`, `config`, `router`, `middleware`, `tls`, `debug`, `ci`

**Examples:**

```
feat(dynamodb): implement PutItem and GetItem (P1)
fix(sqs): apply VisibilityTimeout correctly on first receive
test(s3): add GWT tests for CopyObject cross-bucket
docs(dynamodb): mark PutItem and GetItem as supported
perf(state): use sync.Map for concurrent MemoryStore reads
chore(ci): add TODO-to-issue GitHub Action
```

---

## Pull request checklist

- [ ] `make test` passes (all tests, with race detector)
- [ ] `make lint` passes (no golangci-lint errors)
- [ ] New endpoints have integration tests in GWT form
- [ ] `capabilities_dev.go` updated and `make generate-caps` re-run if any operations changed
- [ ] `make check-caps` passes (for dispatcher-based services)
- [ ] `make docs-check` passes (no uncommitted doc drift)
- [ ] `docs/services/<service>.md` and `docs/services/<service>/operations.md` — regenerated via `make docs` (never edited by hand); the landing page's hand-written sections still follow [the service page template](./docs/dev/service-doc-template.md)
- [ ] CloudFormation resource handlers registered in `resourceHandlers` for every new resource type — see [CloudFormation integration](#cloudformation-integration)
- [ ] Changelog fragment added under `.changelog/` (never edit `[Unreleased]` directly), or `/no-changelog <reason>` commented on the PR to record that it needs none
- [ ] Commit messages follow conventional commits
- [ ] No debug logging left in production paths
- [ ] No new global variables
- [ ] Runtime, tooling, hooks, and instructions remain usable on Windows, macOS, and Linux; any
      shell-specific workflow has an equivalent or tested dispatcher

---

## Reporting bugs

Open a [bug report](.github/ISSUE_TEMPLATE/bug_report.md) with:

1. The service and operation that's broken (e.g. "SQS / ReceiveMessage")
2. The operation's status in `docs/services/<service>/operations.md` — if it says ❌, it's expected to not work
3. What you expected vs what happened (include the error code)
4. A minimal reproduction (curl or code snippet)
5. Your Overcast version and run mode

---

## Ideas for contributions

Check [GitHub Issues](https://github.com/overcast-sh/overcast/issues) for open work.
Look for the `good first issue` label if you're new, or filter by priority
(`P1`, `P2`, `P3`) and effort (`small`, `medium`, `large`) to find something
that fits.
