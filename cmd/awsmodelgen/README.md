# AWS model generator

`awsmodelgen` converts a pinned checkout of AWS's public
[`aws/api-models-aws`](https://github.com/aws/api-models-aws) Smithy JSON AST
models into Overcast's committed operation manifest and immutable routing
indexes, and into the pruned Smithy shape snapshot under `models/aws/shapes/`.

It is a build and maintenance tool. The Overcast daemon never reads Smithy
models, contacts GitHub, or constructs model-sized maps at startup. Runtime
routing uses only the compact generated tables in
`internal/awsapi/manifest.gen.go`; the shape snapshot is generator input only
and is never read at runtime.

## Source and provenance

`models/aws/VERSION` records the official source URL, exact Git revision, model
date, license, and model format. The generator verifies that the parent
checkout of `-models` is at exactly `-source-revision`; a branch name, stale
checkout, or different commit is rejected.

Raw model files are not vendored. Keep a local checkout for manual regeneration:

```sh
git clone https://github.com/aws/api-models-aws
git -C api-models-aws checkout <revision-from-models/aws/VERSION>
make generate-aws-operations \
  AWS_MODELS_DIR=/path/to/api-models-aws/models
```

Never hand-edit or hand-merge `internal/awsapi/manifest.gen.go` or anything
under `models/aws/shapes/`.

## The pruned shape snapshot

The routing manifest deliberately carries no shapes. Inert-tier codegen
([docs/plans/inert-tier-rollout.md §4.6](../../docs/plans/inert-tier-rollout.md))
and the compat scenario generator
([docs/plans/compat-coverage-modelgen.md §3.7](../../docs/plans/compat-coverage-modelgen.md))
both need them, and neither may vendor the raw Smithy corpus
([docs/plans/aws-api-operation-coverage.md §3](../../docs/plans/aws-api-operation-coverage.md)).
The reconciliation is a pruner in this same binary: **one pruner, one
`shapes-sha256`, two consumers.** Do not build a second distillation.

### Flags

| Flag | Meaning |
| --- | --- |
| `-shapes-out <dir>` | Write the snapshot there (`models/aws/shapes`). Empty means do not produce one. |
| `-shapes-services <file>` | The reviewed in-scope service list (`models/aws/shapes-services.txt`): one canonical service key per line, `#` comments allowed. Required with `-shapes-out`. |

`-check` covers the snapshot too when `-shapes-out` is set: it regenerates in
memory and compares byte-for-byte, writing nothing. `-version-file` *requires*
`-shapes-out`, so `shapes-sha256` cannot be refreshed without the snapshot it
describes.

A listed service the corpus does not define is a hard failure — a silently
missing file would read to the generators as "this service has no operations".
A file no listed service produces fails `-check` and is deleted on write, so
removing a service cannot leave a stale snapshot behind.

### Layout

One file per service, `models/aws/shapes/<service>.json`, holding a fixed-order
header and then one line per shape in sorted shape-name order:

```json
{
"smithy": "2.0",
"service": "elastic-load-balancing",
"namespace": "com.amazonaws.elasticloadbalancing",
"serviceShape": "ElasticLoadBalancing_v7",
"sdkId": "Elastic Load Balancing",
"apiVersion": "2012-06-01",
"protocols": ["AWSQuery"],
"shapes": {
"AccessLogInterval": {"type":"integer"},
"AddAvailabilityZonesInput": {"type":"structure","members":{ … }}
}
}
```

Shapes keep the Smithy AST vocabulary — `type`, `members`, `member`/`key`/`value`,
`input`/`output`/`errors`, and the full resource lifecycle (`identifiers`,
`properties`, `create`, `put`, `read`, `update`, `delete`, `list`, `operations`,
`collectionOperations`, `resources`) — so a consumer decodes it with the same
structs it would use for the raw AST. Those structs are
[`internal/awsmodel/snapshot.go`](../../internal/awsmodel/snapshot.go):
`Snapshot`, `SnapshotShape` and `SnapshotMember` are declared once and imported
by both the pruner and `cmd/compatgen`, so the writer's field set and the
reader's cannot drift. Two differences from upstream:

- **Namespace-relative IDs.** A reference into the service's own namespace loses
  its `com.amazonaws.<svc>#` prefix, which the header records once. References
  into another namespace stay fully qualified, so a relative name is exactly the
  one with no `#` in it. Worth 37% of the file size.
- **References are strings, not `{"target": …}` objects**, and reference lists
  are sorted, so an upstream reordering cannot churn the committed file.

Only shapes transitively reachable from the service — its operations, its
resources and their lifecycle bindings, and everything those reference — are
emitted. Prelude shapes (`smithy.api#String`, `smithy.api#Unit`, …) are implicit
targets and are never emitted. Documentation, examples, waiters, smoke tests,
endpoint rule sets, metadata and every out-of-scope service are dropped.

Output is byte-deterministic: sorted map keys throughout, sorted reference
lists, trait values decoded and re-encoded (with `json.Number`, so no numeric
literal is reformatted) rather than copied, `\n` line endings, no HTML escaping.
Regenerating twice produces identical bytes.

The line-per-shape format is a deliberate middle: fully compact would make a
model refresh an unreviewable one-line diff, and indenting the whole document
costs about 40% more bytes for a file no human edits.

### Trait allowlist

Only the traits the generators consume survive; the list is
`shapeTraitAllowlist` in [shapes.go](./shapes.go) and every entry carries the
reason it is there.

| Group | Traits |
| --- | --- |
| Structure and member semantics | `required`, `default`, `clientOptional`, `enum`, `enumValue`, `length`, `range`, `pattern`, `sparse`, `idempotencyToken`, `timestampFormat`, `mediaType`, `references`, `input`, `output` |
| Errors | `error`, `httpError`, `aws.protocols#awsQueryError` |
| HTTP bindings | `http`, `httpLabel`, `httpQuery`, `httpQueryParams`, `httpHeader`, `httpPrefixHeaders`, `httpPayload`, `httpResponseCode`, `httpChecksumRequired` |
| Serialisation names | `jsonName`, `xmlName`, `xmlFlattened`, `xmlAttribute`, `xmlNamespace`, `aws.protocols#ec2QueryName` |
| Pagination | `paginated` |
| Service identity and protocol | `aws.api#service`, `aws.auth#sigv4`, `aws.protocols#awsQueryCompatible`, and the eight protocol traits (`awsJson1_0`, `awsJson1_1`, `awsQuery`, `ec2Query`, `restJson1`, `restXml`, `rpcv2Cbor`, `rpcv2Json`) |
| Resource metadata | `aws.api#arn`, `aws.api#taggable` |

Unqualified names above are `smithy.api#`. Adding an entry widens the committed
snapshot for every in-scope service at once, so it is a reviewed act and belongs
in the pull request description.

### `shapes-sha256`

`models/aws/VERSION` records a digest over the **whole snapshot directory**, so a
hand-edit, a deletion or a partial merge all change it. The definition, so it is
reproducible by hand:

1. for each file, form the line
   `<sha256 of the file contents, lowercase hex><two spaces><path relative to models/aws/shapes, '/' separators><newline>`;
2. sort those lines bytewise;
3. concatenate them and take the SHA-256 of the result, lowercase hex.

That is `sha256sum`'s text-mode output format, so:

```sh
cd models/aws/shapes && sha256sum -t *.json | LC_ALL=C sort | sha256sum -t
```

reproduces it. (`-t` forces text mode, which is already the default outside
Windows.)

### Size budget

`internal/awsapi/shapes_provenance_test.go` holds the committed snapshot to
`maxShapeSnapshotBytes`. That constant is
[inert-tier-rollout.md §4.6](../../docs/plans/inert-tier-rollout.md)'s
acceptance gate made mechanical: raising it is a reviewer's decision about how
much of the fleet budget to spend, never an automatic consequence of adding a
service. §4.6 carries the measurement and the fleet projection it rests on.

## The runtime SDK shape tables

`-sdk-shapes-out internal/awsshapes -sdk-shapes-services models/aws/sdk-shapes-services.txt`
cuts a third artifact from the same pruner: one line-per-shape text table per
listed service under `internal/awsshapes/tables/`, plus an `index.gen.go` naming
them, which `internal/awsshapes` decodes lazily, the first time a Step Functions
`aws-sdk:` integration calls that service. It keeps only what a wire translator reads —
shape kinds, member names and targets, and the HTTP-binding and serialisation
traits (`sdkShapeTraitAllowlist` in [sdkshapes.go](./sdkshapes.go)) — and drops
enum values, documentation and constraints. Unlike the snapshot above it *is*
carried by the binary; that is the point of it, and why it is so narrow. The
committed text is not what the binary embeds, though: `make aws-sdk-shapes`
([cmd/awsshapes-pack](../awsshapes-pack)) gzips it into the untracked
`internal/awsshapes/dist/`, so a refresh still reviews as a readable diff while
each binary pays ~0.5 MB rather than ~4 MB.

The list is every modeled service that resolves to a service Overcast
implements; `internal/awsshapes`' `-tags dev` coverage test fails when a
service gains capabilities without an entry. `sdk-shapes-sha256` in
`models/aws/VERSION` digests `index.gen.go` and the text tables with the
`shapes-sha256` definition, `-check` compares them byte-for-byte, and
`internal/awsshapes/awsshapes_test.go` holds them to two size budgets: the
committed text, and the packed bytes every binary carries.

## Validation

Normal pull requests run the no-network checks against the committed corpus,
including checks that the committed manifest still hashes to the
`manifest-sha256` recorded in `models/aws/VERSION`, that the committed shape
snapshot still hashes to `shapes-sha256`, and that the snapshot is within its
size budget. That catches a hand-edit or a partial merge without needing the
models:

```sh
make aws-models-check
```

When the pinned checkout is available, add `AWS_MODELS_DIR` to regenerate the
manifest and the snapshot in memory and compare them byte-for-byte without
overwriting the committed files:

```sh
make aws-models-check \
  AWS_MODELS_DIR=/path/to/api-models-aws/models
```

The underlying check mode is:

```sh
go run ./cmd/awsmodelgen \
  -models /path/to/api-models-aws/models \
  -source-revision <revision> \
  -output internal/awsapi/manifest.gen.go \
  -shapes-out models/aws/shapes \
  -shapes-services models/aws/shapes-services.txt \
  -check
```

## Refresh inventories

The scheduled model-refresh workflow generates temporary JSON inventories for
the old and new revisions, then writes a Markdown review summary:

```sh
# Generate the baseline while the model checkout is at the old revision.
go run ./cmd/awsmodelgen \
  -models /path/to/api-models-aws/models \
  -source-revision <old-revision> \
  -output <temporary-directory>/baseline-manifest.go \
  -inventory-output <temporary-directory>/baseline-inventory.json

# Generate the committed manifest and compare after checking out the new revision.
go run ./cmd/awsmodelgen \
  -models /path/to/api-models-aws/models \
  -source-revision <new-revision> \
  -output internal/awsapi/manifest.gen.go \
  -inventory-output <temporary-directory>/current-inventory.json \
  -baseline-inventory <temporary-directory>/baseline-inventory.json \
  -summary-output <temporary-directory>/aws-model-summary.md \
  -shapes-out models/aws/shapes \
  -shapes-services models/aws/shapes-services.txt \
  -version-file models/aws/VERSION \
  -model-date YYYY-MM-DD
```

The summary reports:

- added and removed services and operations;
- protocol-trait changes;
- HTTP, target, signing-name, and RPC service-shape binding changes;
- added and removed routing collisions;
- claimable and explicitly ambiguous non-S3 fallback coverage; and
- newly uncovered or resolved operations.

Inventories and summaries are temporary review artifacts, not runtime inputs.
Output is deterministic for a given source revision.

## Automated refresh

`.github/workflows/aws-model-refresh.yml` checks upstream weekly and on manual
dispatch. It also re-runs when a push to `main` touches what the refresh is
generated from (`models/aws/`, `internal/awsapi/`, `internal/awsshapes/`,
`cmd/awsmodelgen/`, `scripts/aws-models.go`), but only to bring an already open
refresh PR up to date: a push never opens one. When AWS publishes a new
revision, it:

1. restores and verifies a cached upstream Git mirror;
2. asserts the committed manifest **and shape snapshot** still match the
   *pinned* revision, so already-stale generated output cannot be carried
   forward silently;
3. generates old and new inventories, the manifest, and the shape snapshot —
   all in the same commit as the revision bump, so the two cannot drift;
4. runs `make aws-models-check` with full regeneration enabled, which at that
   point asserts determinism rather than staleness;
5. force-with-lease updates only `automation/aws-api-models`; and
6. creates or updates one pull request without merging it.

See `CONTRIBUTING.md` and
`docs/plans/aws-api-operation-coverage.md` for maintainer configuration and the
larger operation-coverage design.
