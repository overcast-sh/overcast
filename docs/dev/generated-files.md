# Generated files: what is committed, and why

Every generated artefact in this repository is one of three things. This page
says which, for all of them, so the next person does not have to re-derive it.

- **Committed** — regenerate with the owning command and commit the result.
- **Build output** — written by the build, ignored by git.
- **Derived at runtime** — no artefact exists at all.

## The rule

> Commit a generated file only when something outside the build reads it.

GitHub rendering a Markdown table counts. The website's content sync counts. A
Go `//go:embed` counts, because a bare `git clone && go build ./...` has to
work. A Vite import does **not** count on its own — that is the build reading
its own output.

The rule has a second half, learned the hard way: **a sorted, one-entry-per-page
manifest must never be committed.** Two branches conflict on it whenever one
adds a page and another edits the page next to it in the sort order, even when
their Markdown does not overlap. The resolution is always the same non-decision
— take main's side, regenerate — which is the tell that the file did not belong
in git.

Nothing else fixes that. A `.gitattributes` merge driver does not help: GitHub
computes mergeability server-side and does not run custom drivers, so the PR
still shows as conflicting. `merge=union` is worse than the conflict — it keeps
both sides, so the same page appears twice in the index and search silently
returns duplicates.

## The inventory

| File | Verdict | Why |
| --- | --- | --- |
| `internal/capabilities/all.gen.go` | committed — `make generate-caps` | `//go:build dev`, so a `-tags dev` build on a bare clone needs it. One line per operation, and it changes only alongside the `capabilities_dev.go` that produced it — which conflicts too, with a real decision in it. |
| `web/src/types/api.gen.ts` | committed — `make generate-ts` | Three commits in the repository's life. Untracking it would put a Go toolchain back into the Node-only SPA build (the CI web job and the Dockerfile's web stage), which is a real cost for a file that does not churn. |
| `web/src/routeTree.gen.ts` | committed — written by `pnpm dev` / `pnpm build` | The TanStack Router Vite plugin regenerates it in place on every dev server and build, so it is already build output in practice. It stays committed because `pnpm typecheck` runs `tsc` without Vite and would fail without it, and no standalone generator CLI is installed. Five commits ever, none in months — not a conflict source. |
| `internal/awsapi/manifest.gen.go` | committed — `make generate-aws-operations` | Regenerating it needs an external `api-models-aws` checkout pinned by `models/aws/VERSION`. Not reproducible from this repository alone, so it must be committed. `go run ./scripts/aws-models.go --ensure` fetches that checkout into a per-user cache and prints its path, so it composes as `AWS_MODELS_DIR="$(go run ./scripts/aws-models.go --ensure)"`; see CONTRIBUTING.md § Refreshing the AWS API models. |
| `internal/awsshapes/*.gen.go` | committed — `make generate-aws-operations` | The Smithy shape tables Step Functions' `aws-sdk:` integrations serialise requests and read responses with, one file per service in `models/aws/sdk-shapes-services.txt`. Cut by the same pruner as `models/aws/shapes/` from the same pinned checkout, so they are committed for the same reason as the manifest; `sdk-shapes-sha256` in `models/aws/VERSION` lets an ordinary pull request prove they are the generator's own bytes. Compiled in, and decoded lazily per service. |
| `compat/suites/registry.generated.json` | committed — `make generate-compat-model` | The generated registry sibling every compat loader concatenates with `registry.json`; suite images copy it in beside `registry.json`, so it has to exist in the tree. Rewritten wholly by `cmd/compatgen`; empty while no suite has a scenario backend. |
| `compat/model/scenarios/<service>.json`, `compat/model/gaps.json` | committed — `make generate-compat-model` | The scenario IR the interpreter suites read at test time, and the refusal report reviewers read. Generated offline from the committed shape snapshot, the recipes and the capability table; `make compat-model-check` (CI) regenerates and diffs. |
| `compat/suites/go-sdk/internal/groups/scenarios_gen.go`, `scenarios_<service>_gen.go` | committed — `make generate-compat-model` | The Go source the `go-sdk` suite compiles for the generated groups: one file per service plus the index `groups.All` calls. The typed SDKs have no dynamic-dispatch API, so their backend is emitted source rather than an interpreter; the semantics live in the suite's hand-written `internal/scenario`. Emitted only while `scenarioBackends` in `cmd/compatgen/registry.go` names `go-sdk` — the index is written either way, so the package always compiles. |
| `compat/suites/java-sdk/src/main/java/io/overcast/compat/groups/ScenariosGen.java`, `Scenarios<Service>Gen.java` | committed — `make generate-compat-model` | The Java source the `java-sdk` suite compiles for the generated groups: one class per service plus the index `Main` merges. It resolves every member's spelling from the pinned shape snapshot rather than from the SDK — the AWS SDK for Java v2 boxes every scalar, so nothing about a member's type follows from the SDK — and the semantics live in the suite's hand-written `io.overcast.compat.scenario`. Emitted only while `scenarioBackends` in `cmd/compatgen/registry.go` names `java-sdk`; the index is written either way, so the package always compiles. |
| `compat/suites/dotnet-sdk/Groups/ScenariosGen.cs`, `Scenarios<Service>Gen.cs` | committed — `make generate-compat-model` | The C# the `dotnet-sdk` suite compiles for the generated groups: one file per service plus the index `Program.cs` registers as the suite's `ScenarioBackend`. Same split as go-sdk's — data plus typed calls, semantics hand-written in the suite's `Scenario/` — but the member's type comes from the shape model rather than the SDK, so an operation the pinned `AWSSDK.*` does not declare is a compile error in the suite rather than a refusal in `gaps.json`. Emitted only while `scenarioBackends` in `cmd/compatgen/registry.go` names `dotnet-sdk` — the index is written either way, so the project always compiles. |
| `compat/suites/rust-sdk/src/groups/scenarios_gen.rs`, `scenarios_<service>_gen.rs` | committed — `make generate-compat-model` | The Rust source the `rust-sdk` suite compiles for the generated groups: one module per service plus the index `scenario_groups` returns. It resolves every member's spelling from the pinned shape snapshot too — a fluent builder setter takes the value itself rather than an `Option`, so a member's optionality never reaches the call site — and the semantics live in the suite's hand-written `crate::scenario`. Emitted only while `scenarioBackends` in `cmd/compatgen/registry.go` names `rust-sdk`; the index is written either way, so the crate always compiles. |
| `internal/services/dynamodb/reserved_words.txt` | committed — `make generate-ddb-reserved-words` | `//go:embed`ed by `internal/services/dynamodb`, so a bare clone must find it. Regenerating needs network access to the AWS Developer Guide, so no CI job re-runs it and nothing gates its freshness — refresh by hand when AWS changes the published list, and review the diff. The file's header records the source URL, the fetch date and the word count. |
| `docs/README.md` service-index block | committed — `make docs` | Rendered on GitHub, embedded in the binary, **and** parsed by the website's content sync (`scripts/sync-overcast-content.ts` reads the `overcast:service-index` sentinels). Three consumers outside the build. |
| `docs/services/<key>/operations.md` (50 files) | committed — `make docs` | Same three consumers. Changes only when capability data changes, which is a real change. |
| `docs/generated/service-support.json` | committed — `make docs` | Reviewed data, read outside the build. |
| `web/dist/**` | build output — `.gitignore`d, with a committed `dist/.gitkeep` | `//go:embed all:web/dist` must resolve on a bare clone. A binary built without an SPA serves an explanatory 503 on the UI port. |
| `internal/services/lambda/initbin/dist/lambda-init-linux-*` | build output — `make lambda-init` | Same shape: committed `.gitkeep`, binaries ignored. |
| `web/src/docs-nav.gen.ts` | **deleted** — derived at runtime | Was a 7,332-line manifest, one 60-line object per page, rewritten 300–1,600 lines by every docs PR. Only the console read it. |
| `internal/docssearch/index.gen.jsonl` | **deleted** — derived at runtime | Was a 123-line manifest, one JSON line per page. All three concurrent docs PRs open at the time conflicted on it *pairwise* — including a pair whose Markdown did not overlap, where one branch inserted `services/appsync/limitations.md` and the other edited the `services/athena.md` line beside it. |

## Why the last two are gone rather than gitignored

The obvious fix is to stop tracking them and generate them at build time. That
trades a git problem for a build problem, in about twenty places:

- `//go:embed` of a missing file does not compile, so the Go side needs an
  embedded directory with a committed `.gitkeep` — and then a bare `go build`
  produces a binary whose docs search is silently empty unless every build path
  runs the generator first (`Makefile` × 13 targets, `Taskfile.yml`, the
  Dockerfile's Go stage, four CI jobs, `build-binaries.yml`, `release.yml`, the
  devcontainer, `air`).
- The navigation generator is Go, and both places the SPA is built are
  deliberately Node-only — the CI `web` job and the Dockerfile's `web-builder`
  stage. Generating there means either adding Go to both, or writing a second
  frontmatter-and-heading parser in Node.

Neither was necessary. Both files are pure functions of `docs/**.md`, and the
console binary already embeds exactly that set:

```go
//go:embed docs/*.md docs/cdk docs/services
var DocsServicesFS embed.FS
```

So [`internal/docsindex`](../../internal/docsindex/docsindex.go) parses that
tree and [`internal/bff`](../../internal/bff/bff.go) serves it — `/api/docs/nav`
for the sidebar and page outline, and an in-memory
[`docssearch.Index`](../../internal/docssearch/index.go) for `/api/docs/search`.
Both are built once, lazily, on the first request that needs them — around
100 ms for the whole corpus, paid only by a process that opens the console
docs. `BenchmarkBuildIndex` in `internal/docsindex` is the measurement.

The console loses nothing. It already fetched every doc body from
`/api/docs/page`, so the docs page has never worked without the Go BFF, and
`web/api/src/app.ts` proxies all of `/api/*` to it by design. Fetching the
navigation from the same place removes ~200 KB of JSON from the SPA bundle.

## What this means day to day

| You changed | You run |
| --- | --- |
| a published doc under `docs/` | `make docs-lint` — checks frontmatter, in-page anchors, service page structure, the 220-character description budget, the page length budget and the house-style tells. Nothing to regenerate, nothing extra to commit. |
| `docs/plans/**` or `docs/dev/**` | nothing — neither is published, embedded or indexed |
| a service's `capabilities_dev.go` | `make docs` (regenerates `all.gen.go`, the service tables and `service-support.json`), then `make generate-compat-model` — an operation's status decides whether it is probed or expected to work |
| `compat/model/recipes/`, `compat/model/values.json` or `models/aws/shapes/` | `make generate-compat-model` |
| nothing here, but AWS published a new DynamoDB reserved word | `make generate-ddb-reserved-words`, then update the count in `internal/services/dynamodb/expr_reserved_test.go` |
| a Go response struct the console reads | `make generate-ts` |
| a console route file | nothing — `pnpm dev` / `pnpm build` rewrites `routeTree.gen.ts` |

`make docs-check` is the CI gate over all of it, and still fails on a stale
committed artefact: it regenerates and diffs.
