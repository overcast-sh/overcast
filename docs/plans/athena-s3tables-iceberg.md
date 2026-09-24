# Athena, S3 Tables and Iceberg — plan for real support

> Status: proposal, 2026-09-23, based on `main` at `cb08d3d22`; "Where things
> stand" describes that commit. Some phases have since been implemented —
> see the note at the top of each. Tracking issue: #2073, with one issue per
> phase below.

## Where things stand

| Area | Today | Gap |
| --- | --- | --- |
| **Athena** (`internal/services/athena`, `TierInert`) | 12 of 70 modeled operations. `StartQueryExecution` stores the SQL and marks it `SUCCEEDED` straight away. `GetQueryResults` always returns an empty `ResultSet`. | No SQL runs. `QueryExecutionContext` is ignored and nothing is written to `OutputLocation`. `Statistics` is absent and the `QUEUED`/`RUNNING`/`FAILED` states never happen. There are no named queries, prepared statements, data catalogs or metadata APIs, no `primary` workgroup, and `CreateWorkGroup` silently overwrites an existing workgroup. |
| **Glue Data Catalog** (`internal/services/glue`, `TierInert`) | Create/Get/List/Delete for databases and tables, plus tags. | `Table` keeps 5 fields. `StorageDescriptor`, `Parameters` (including `table_type=ICEBERG` and `metadata_location`), `PartitionKeys` and `OpenTableFormatInput` are all dropped on decode. There is no `UpdateTable`, no partitions, no table versions, no existence or duplicate checks, and deleting a database leaves its tables behind. |
| **S3 Tables** (`s3tables`) | Not implemented. The pinned model has 49 operations; signed calls to them get a modeled 501 through `restFallback`. | Everything. |
| **Iceberg** | No code outside the generated shape tables. Firehose accepts an `IcebergDestinationConfiguration` and drops it, the same as every other destination. | Metadata model, REST catalog, and the engine reading and writing it. |
| **Integrations** | CloudFormation handles `AWS::Athena::WorkGroup`, `AWS::Glue::Database` and `AWS::Glue::Table`; the last one loses most of its properties. | There is no Step Functions `athena:` optimized integration. There is no web UI. `web/src/lib/unsupported-services.ts` still lists athena, glue, firehose and opensearch as not emulated. |

Constraints that shape the design:

- **CGO is off in every build** (Dockerfile, Makefile, cross-builds). That rules out an embedded DuckDB. The only SQL dependency is `modernc.org/sqlite`, which is pure Go, and it is itself dropped under `-tags nosqlite`.
- **No in-process S3 write or list API.** The only one is `s3.Service.GetObjectBytes`, wired to Lambda through a closure in `router.go`. Step Functions talks to S3 with synthetic HTTP calls instead (`stepfunctions/s3io.go`).
- **S3 Tables' root paths (`/buckets`, `/namespaces`, `/tables`, `/get-table`) are also legal S3 bucket names.** The dispatch therefore has to use the SigV4 signing name, as the `/applications` and `/v2/apis` dispatchers in `router.go` do. It cannot match on the path.

## Architecture

```
 SDK/CLI ──► Athena (Go) ──DDL──────────────► Glue catalog (Go) ◄─── Glue API / CFN
               │  Trino client protocol (HTTP)       ▲
               ▼                                     │ Glue metastore API
          Trino container ───────────────────────────┘
               │  S3 API (path-style)     │ Iceberg REST (SigV4 s3tables)
               ▼                          ▼
          Overcast S3  ◄──warehouse──  S3 Tables (Go) ◄─── S3 Tables API / CFN / Spark, PyIceberg
```

**Decision 1 — the query engine is Trino in a container.** Athena engine v3 *is*
Trino, so dialect, functions and type names match without a translation layer
for DML. Trino speaks every protocol we need:

- a Glue metastore (`hive.metastore=glue` with an endpoint override), pointed at Overcast's Glue;
- S3 (native filesystem with endpoint override and path-style), pointed at Overcast's S3;
- an Iceberg REST catalog, pointed at Overcast's S3 Tables.

The engine then reads real data that users put in S3, and whatever it writes
lands in S3 and Glue where the rest of Overcast can see it.

Rejected alternatives:

- **An embedded pure-Go SQL engine.** No Parquet/ORC/Iceberg readers, a different dialect, and years of work.
- **DuckDB as the default engine,** whether embedded, run as a CLI subprocess or wrapped in a sidecar: its dialect is not Athena's. See the DuckDB and Floci bullets below.
- **Presto.** Athena v2 is retired.
- **Turning on CGO to embed DuckDB.** Every release binary (10 of them: linux, darwin and windows, amd64 and arm64, full and slim) is cross-compiled on a single Ubuntu runner, and the images are cross-built from `$BUILDPLATFORM` onto musl Alpine. CGO would need a C toolchain for each target, macOS runners or a Darwin SDK, and a glibc base image or a musl DuckDB build. It would also stop a bare checkout building without a C compiler, which is exactly the Windows-contributor case the cross-platform contract protects. All of that buys an engine whose dialect is not Athena's.
- **If a Docker-free engine is wanted later,** run DuckDB's official CLI as a host subprocess: pin it by hash, download it on first use, and talk to it over stdin with JSON output. That gets the same engine with no CGO. Treat it as an optional second engine behind `ATHENA_ENGINE=duckdb`, and never as the default. It starts in under 100 ms and idles at tens of MiB, but it pays for that in fidelity:
  - Queries need translating from Athena's SQL. There is no Go transpiler, and about 10 Trino functions cannot be matched by rewriting (see `duckdb-trino-parity-extension`).
  - Its Iceberg writes (v1.5.3) are merge-on-read only and cannot `UPDATE` or `DELETE` sorted tables.
  - It has no ORC support and no metastore, so Overcast would have to map each Glue table to `read_parquet`/`read_csv` views itself.
- **Floci, a sibling emulator, takes this route**, with a sidecar container (`floci/floci-duck`: Rust, MIT) that wraps DuckDB in HTTP:
  - Each Glue table becomes a DuckDB view chosen by format: `read_parquet`, `read_json_auto`, `read_csv_auto`, or `iceberg_scan(metadata_location)` for `table_type=ICEBERG`.
  - The user's SQL goes to DuckDB **untranslated**, wrapped in `COPY (…) TO 's3://…' (FORMAT CSV, HEADER)`.
  - The only DDL it intercepts is `CREATE DATABASE`.
  - Iceberg is read-only through Athena.
  - Its S3 Tables stores metadata only: no warehouse objects and no Iceberg REST catalog.
  - It is fast and small, and a working proof that the opt-in DuckDB engine is viable. The price is that any query using Trino-only syntax fails, or silently behaves differently.
- **DataFusion-based engines** (Sail, Spice, datafusion-cli): their SQL is close to Postgres or Spark, not Trino, and they cannot yet do Iceberg `UPDATE` or `MERGE`.
- **Pure-Go SQL engines:** a MySQL dialect with no Iceberg SQL.
- **DuckDB-wasm under wazero:** not feasible, because it is an Emscripten build that needs pthreads and JavaScript glue.

The costs were measured on 2026-09-23 on Docker Desktop, Windows 11, 24 cores,
with Trino 483:

- The stock image is 2.4 GB on disk, idles at 736 MiB and is ready in 7.8 s.
- A tuned engine (below) idles at **~420 MiB**, peaks around **~800 MiB** under CTAS, aggregates, joins and window functions, and is ready in **~6 s**. Expect 10–15 s on a laptop.
- All of these costs are paid only when a query actually runs, because the engine starts on first use.

The tuning recipe to use in Phase 3:

- **Custom image:** build on `trinodb/trino-core` with only the `hive` and `iceberg` plugins (the `trino-packages` `custom-docker` script). That comes to roughly 1.2 GB, pinned by digest.
- **`jvm.config`:** `-Xmx512m -Xms256m -XX:+UseG1GC -XX:TieredStopAtLevel=1 -XX:ReservedCodeCacheSize=64M -XX:+ExitOnOutOfMemoryError`. A heap of 512m is the floor: 384m and 256m ran out of memory on ordinary queries.
- **`config.properties`:** `query.max-memory=256MB`, `query.max-memory-per-node=128MB`, `memory.heap-headroom-per-node=64MB`, `task.concurrency=1`, `task.max-worker-threads=4`, `exchange.max-buffer-size=8MB`, `sink.max-buffer-size=8MB`, `task.max-partial-aggregation-memory=4MB` and `query.max-history=10`.
- **Container limit:** 1 GB by default, raised through `ATHENA_ENGINE_MEMORY`.
- **Idle shutdown:** stop the container after N idle minutes and restart it lazily on the next query.

What was tried and does not work:

- **SerialGC** runs out of memory even on `count(*)`.
- **AppCDS** gives no startup gain and conflicts with the image's `libjvmkill` agent.
- **CRaC** needs an Azul JDK, Linux only, extra capabilities and a checkpoint per host.
- **GraalVM native-image** cannot work, because Trino generates bytecode at runtime.

**Decision 2 — Overcast owns DDL; Trino owns queries.** Athena accepts Hive DDL
(`CREATE EXTERNAL TABLE … ROW FORMAT SERDE … LOCATION`, `MSCK REPAIR TABLE`,
`ALTER TABLE ADD PARTITION`, `SHOW …`) that Trino cannot parse. The plan:

- A small Go parser for Athena's DDL subset turns those statements into Glue calls, which is what Athena itself does. They complete without the engine and set `StatementType=DDL`.
- Iceberg `CREATE TABLE … TBLPROPERTIES ('table_type'='ICEBERG')` and CTAS are rewritten to Trino's `WITH (…)` form and run on the Iceberg connector.
- Everything else (`SELECT`, `INSERT`, `UPDATE`, `MERGE`, `DELETE`, `OPTIMIZE`, `VACUUM`) goes to Trino unchanged.

**Decision 3 — an inert fallback stays.** `ATHENA_ENGINE=trino|inert`. With no
Docker, or with `inert`, today's behaviour stays: `SUCCEEDED`, empty results. It
is surfaced in the docs and in the console, and DDL still updates Glue. This
keeps `-tags slim` users and Docker-less CI working.

**Decision 4 — Iceberg metadata handling in Go.** S3 Tables `CreateTable` with
a schema, the Iceberg REST catalog's `commit`, and Glue `OpenTableFormatInput`
all have to write `metadata.json` and apply table updates and requirements. The
choice is between two options:

- **(a) `github.com/apache/iceberg-go`.** It is pure Go and already implements the metadata builder and the update and requirement semantics, but it pulls in arrow-go and adds binary weight.
- **(b) A hand-rolled model** covering the metadata v2 fields and the ~15 REST update actions.

Recommend spiking (a) and measuring the binary delta. If the delta is more than a
few MB, fall back to (b). Only metadata is touched here; data files are
written by the engine or the client, never by Overcast.

## Phases

Each phase ships on its own and has value without the phases after it. They
are listed in dependency order.

### Phase 0 — groundwork (S) — #2062, #2063

- Add an in-process S3 accessor next to `GetObjectBytes`: `PutObjectBytes`, `ListObjects` and `EnsureBucket`. `PutObjectBytes` must fire notifications, the same as the HTTP path. Inject it through func types in `router.go`.
- Remove the stale athena, glue, firehose and opensearch entries from `unsupported-services.ts`.

### Phase 1 — Glue Data Catalog fidelity (M) — #2064

Trino's Glue metastore and Iceberg's Glue catalog both need this, and it helps
CDK users on its own.

- Persist the full `TableInput` and `DatabaseInput`: `StorageDescriptor`, `Parameters`, `PartitionKeys`, `TableType`, `ViewOriginalText`, and so on. Stamp `CreateTime`, `UpdateTime` and `VersionId`. Tolerate records in the old shape (malformed-state rule).
- Add `UpdateTable`, with `VersionId` optimistic concurrency that fails `ConcurrentModificationException` on a stale version. Iceberg's Glue catalog commits through this.
- Add `GetTableVersion(s)`, `DeleteTableVersion`, `BatchDeleteTable` and `UpdateDatabase`.
- Partitions: `Create`, `BatchCreate`, `Get`, `GetPartitions` (with an `Expression` subset: `=`, `<>`, ranges, `AND`/`OR`, `IN`), `BatchGet`, `Update`, `Delete` and `BatchDelete`. Store them in a new `glue:partitions` namespace and register it in `state/tier.go`.
- Add `AlreadyExistsException`, `EntityNotFoundException` for a missing parent database, and cascade on `DeleteDatabase`.
- Accept `OpenTableFormatInput.IcebergInput`: write the initial metadata (Decision 4) and set `metadata_location`.
- Add an exported read API (`GetTable` and `GetDatabases`) for Athena's metadata operations, injected in `router.go`.
- CloudFormation: carry all of `AWS::Glue::Table`'s properties, and add `AWS::Glue::Partition`.

### Phase 2 — Athena control plane completion (M) — #2065

This phase needs no engine.

- Seed a `primary` workgroup. `CreateWorkGroup` returns `InvalidRequestException` when the workgroup exists. Add `UpdateWorkGroup` (`ConfigurationUpdates`) and give `ListWorkGroups` its full summary shape.
- Complete the `QueryExecution` shape: `QueryExecutionContext`, `StatementType`, `EngineVersion`, `WorkGroup`, `ResultConfiguration` resolved against the workgroup (`EnforceWorkGroupConfiguration`), `Statistics`, `ResultReuseConfiguration`, `ClientRequestToken` idempotency, and `ExecutionParameters`.
- `ListQueryExecutions` filters by workgroup and paginates. Add `BatchGetQueryExecution`.
- Named queries: CRUD plus `BatchGet`. Prepared statements: CRUD plus `BatchGet`. `EXECUTE … USING` is resolved in Phase 3.
- Data catalogs: CRUD, with a built-in `AwsDataCatalog` of type `GLUE`. `GetDatabase`, `ListDatabases`, `GetTableMetadata` and `ListTableMetadata` read Glue through the Phase 1 API.
- Add `ListEngineVersions`, which returns only "Athena engine version 3".
- CloudFormation: `AWS::Athena::NamedQuery`, `DataCatalog` and `PreparedStatement`, plus WorkGroup update in place instead of `errReplacementRequired` (see also #1759).

### Phase 3 — Athena query execution on Trino (L) — #2066

**Engine manager.** Model it on ECR's `ensureRegistry` for the lazy singleton,
and on ElastiCache's `SetDocker`, `Stop`, GC and readiness for everything else.

- Config: `ATHENA_ENGINE`, and `ATHENA_ENGINE_IMAGE` pinned by digest with an override (the EFS precedent). Add `ATHENA_DOCKER_SOCKET`, `ATHENA_KEEP_CONTAINERS` and `ATHENA_ENGINE_MEMORY`.
- Render `etc/catalog/*.properties` into the container:
  - `awsdatacatalog` is Hive on the Glue metastore, with `hive.iceberg-catalog-name` redirecting Iceberg tables.
  - `awsdatacatalog_iceberg` is Iceberg on the Glue catalog.
  - Phase 5 adds one Iceberg REST catalog per table bucket, through dynamic catalog management.
  - Endpoints are minted per `docs/dev/container-networking.md` (a container calling Overcast).

**Trino client.** A small Go client for the REST client protocol (`POST /v1/statement`, follow `nextUri`, `DELETE` to cancel). No driver dependency.

**Execution.**
- A goroutine takes a query through `QUEUED` (engine booting), then `RUNNING`, then `SUCCEEDED`, `FAILED` or `CANCELLED`.
- Failures produce `AthenaError` with `ErrorCategory` and `ErrorType` mapped from Trino's error codes, and a `StateChangeReason`.
- `StopQueryExecution` cancels the running query.
- Executions still running at shutdown are marked `FAILED`, following the Step Functions orphan-reaper precedent.
- `Statistics` and `GetQueryRuntimeStatistics` are filled from Trino's query stats.

**Results.**
- Write `<OutputLocation>/<id>.csv` and `<id>.csv.metadata` to S3 through the Phase 0 accessor, using Athena's CSV quoting.
- `GetQueryResults` pages from the stored result. For `SELECT`, the first row is the header. Types are mapped from Trino to Athena `ColumnInfo` names, and `UpdateCount` is set for DML.
- Results are cached in a `Cached`-tier namespace, which is also what `ResultReuseConfiguration` reuses.

**The rest of the phase.**
- The DDL router and translator (Decision 2).
- Enforce workgroup `BytesScannedCutoffPerQuery`, and publish CloudWatch metrics if they are enabled.
- Move `ServiceTiers`: athena goes from `TierInert` to the Docker-backed tier, Glue goes to Core.
- Tests:
  - unit tests against an `httptest` fake Trino (the MSK/ElastiCache pattern);
  - integration tests behind `SkipWithoutDocker` and `PullOrSkip`: a CSV/Parquet table from S3, partitions, CTAS, an Iceberg `INSERT`/`MERGE` followed by a Glue `metadata_location` check, plus stop and failure cases;
  - a compat group with `requires: ["docker"]`.

### Phase 4 — S3 Tables control plane (M–L) — #2067

> Implemented (2026-09-23): all 49 operations, the signing-name dispatch, the
> warehouse bucket and CloudFormation. `CreateTable` writes its first
> `metadata.json` through a hand-rolled `internal/icebergmeta` (option (b) of
> Decision 4). The console page is #2072.

- A new `internal/services/s3tables` package, copying the REST pattern of `scheduler/`.
- **Dispatch.** Register it under the SigV4 signing-name dispatcher so that `/buckets`, `/namespaces`, `/tables`, `/get-table` and `/tag` reach S3 Tables only when `ServiceFromCredential(r)=="s3tables"`, and S3 otherwise. Add it to the `detectService` route test as `s3`-classified families. Also update `allServices`, `ServiceTiers`, `state/tier.go`, `topology.go` and `serviceidentity.go`.
- **Operations.** Table buckets, namespaces and tables (Create, Get, List with prefix and continuation tokens, Delete, `RenameTable`). Also:
  - `GetTableMetadataLocation` and `UpdateTableMetadataLocation`, with `versionToken` optimistic concurrency returning `ConflictException`;
  - bucket and table policies, encryption, storage class, metrics configuration and tags;
  - maintenance, record-expiration and replication configuration, which are stored and echoed. Their job-status calls report `Disabled` or `Not_Yet_Run`, because Overcast runs no compaction or replication. Document that.
- **Warehouse.** Each table gets a `warehouseLocation` of `s3://<uuid>--table-s3`, created through the Phase 0 `EnsureBucket`. The S3 service has to accept the `--table-s3` suffix, which real S3 reserves, and deny the bucket to ordinary `CreateBucket`.
- **CreateTable with `metadata.iceberg.schema`** writes the initial `metadata.json` (Decision 4).
- **CloudFormation:** `AWS::S3Tables::TableBucket`, `Namespace`, `Table`, `TableBucketPolicy` and `TablePolicy`.
- Docs, console page, compat group.

### Phase 5 — the Iceberg REST catalog endpoint (L) — #2069, spike #2068

> Implemented (2026-09-25), except the Trino wiring: the catalog at `/iceberg`
> (and unsigned at `/_overcast/s3tables/iceberg`), staged create, and commits
> through `internal/icebergmeta`'s `Parse` and `Commit`, sharing one
> compare-and-swap with `UpdateTableMetadataLocation`. Verified with PyIceberg
> 0.12 end to end and against golden commits captured from it. Trino's
> per-bucket REST catalogs and the end-to-end test move to Phase 3 (#2066),
> which has no engine to wire them into yet.

- Serve the Iceberg REST spec that AWS exposes at `https://s3tables.<region>.amazonaws.com/iceberg`:
  - `GET /iceberg/v1/config?warehouse=<bucketARN>`;
  - namespaces (list, create, load, drop, properties);
  - tables (list, create, load, register, drop, rename, `commit` with requirements and updates);
  - the `prefix` is the URL-encoded table bucket ARN.
- Signing-name dispatch as in Phase 4. Also mount it at `/_overcast/s3tables/iceberg` for unsigned clients, because `iceberg` is a legal bucket name.
- Implement **staged create** (`stage-create` plus the `assert-create` requirement). Trino uses a staged create for `INSERT` and CTAS whenever the namespace reports a `location` property (trinodb/trino#26742). AWS's own endpoint lacks staged create, which is why Trino can only read S3 Tables on AWS (trinodb/trino#24916). Overcast should support it anyway, so that local writes work.
- A commit validates its requirements, applies the updates, writes a new metadata file and swaps `metadata_location` under the same lock that `UpdateTableMetadataLocation` uses.
- Wire Trino's per-bucket REST catalogs. Athena addresses them as `"s3tablescatalog/<bucket>"."<ns>"."<table>"`.
- Verify with PyIceberg and Spark (`org.apache.iceberg.aws` REST plus SigV4) in integration tests, and with Trino end to end.

### Phase 6 — the catalog federation Athena uses (M) — #2070

- Glue multi-level catalogs: `GetCatalog`, `GetCatalogs`, and the `s3tablescatalog` federated catalog with one child catalog per table bucket. Accept `CatalogId` of the form `<account>:s3tablescatalog/<bucket>` on the Glue table and database APIs, backed by S3 Tables. This is how the console and Athena list S3 Tables.
- Athena `ListDatabases` and `ListTableMetadata` over those catalogs.
- Lake Formation grants are out of scope. Document that everything is readable.

### Phase 7 — integrations and console (M) — #2071, #2072

- **Step Functions:** add `athena:startQueryExecution` (`.sync`), `getQueryExecution`, `getQueryResults`, `stopQueryExecution`, and the named-query and workgroup integrations to `dispatchTask`. Also add Athena as a Distributed Map `ItemReader` source (#2040).
- **Firehose Iceberg destination:** this needs Firehose delivery to exist first, which is a separate programme. Record it as a follow-up, not part of this plan.
- **Console, system map and developer QoL:** designed in [data-lake-console.md](./data-lake-console.md). It covers the Athena query workspace, the Glue catalog browser with a create-from-S3 wizard, the S3 Tables console with a connect-a-client panel, S3 previews for CSV, Parquet and Iceberg files, map nodes, edges and live overlays, and `overcast athena query`, MCP tools and a sample dataset.
- **Docs:** rewrite `athena.md` and `glue.md`, add `s3tables.md` with `limitations.md`, and write an "Iceberg locally" guide covering PyIceberg, Spark and Athena. Update STATUS.md and add a changelog fragment per PR.

## Out of scope

- Athena Spark: sessions, calculations and notebooks. These stay a modeled 501.
- Capacity reservations: consider store-and-echo later.
- Federated query through Lambda connectors.
- Lake Formation permissions.
- Real S3 Tables compaction, snapshot expiry and replication.
- Emulating Athena's Hive engine for Hive-only functions: DML runs on Trino semantics.

## Risks

| Risk | Mitigation |
| --- | --- |
| Trino's image is large and slow to start | Start the engine lazily, keep one per process, and reuse it across queries. `ATHENA_ENGINE=inert` opts out. Document first-query latency. |
| Docker-backed tests slow down CI | Keep engine tests in the `lambdadocker`-style Docker job, and share a single engine per test binary. |
| Athena and Trino dialects differ (DDL, `$path`, `"$partitions"` tables) | The DDL layer, plus a documented table of divergences. |
| The Iceberg metadata dependency adds binary weight | Spike, measure, and fall back to option (b). |
| The engine container must reach Overcast on Windows and macOS | Mint addresses per the container-networking rules, and test on Docker Desktop. |

## Suggested PR sequence

0 → 1 → 2 can land while Phase 3's engine is being built. Phase 4 depends
only on 0 and can run in parallel with 1–3. Phase 5 needs 4 and the Decision 4
spike, Phase 6 needs 1, 4 and 5, and Phase 7's Step Functions work needs 3.
Every phase is several PRs; split them per `issue-coordination`.
