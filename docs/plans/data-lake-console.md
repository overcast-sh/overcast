# Data-lake console, system map and developer QoL

> Status: design, 2026-09-23. Companion to
> [athena-s3tables-iceberg.md](./athena-s3tables-iceberg.md) (tracking #2073).
> The backend phases there decide *what* works; this decides how a developer
> *sees and drives* it. Nothing here is implemented yet.

## What a developer is doing

Someone building a data stack locally loops through five jobs:

1. **Land data.** Upload files to S3, or have a Lambda, Firehose or Spark job write them.
2. **Describe it.** Register a Glue table (CDK, `CREATE EXTERNAL TABLE`, PyIceberg) or create an S3 Tables table.
3. **Query it.** Run the SQL their application runs, see rows, and fix the query.
4. **Change it.** `INSERT`, `MERGE` or schema evolution on Iceberg, then check what the commit did.
5. **Wire it in.** Their code (Step Functions, a Lambda, an API) runs the query, and they check it ran with the right results.

Every screen below exists to shorten one of those loops. Anything that doesn't
is cut. The test for each feature is the question CONTRIBUTING's map
guidance asks: *what is the first thing a developer wants to do here, and can
they do it without losing their place?*

## Quality bar

These pages follow CONTRIBUTING § Web UI standards and the decisions in
[web-ui-polish-wave-2.md](./web-ui-polish-wave-2.md) without exception. The
rules that matter most here, as acceptance criteria on every console issue:

- **Lists and fields:** resource lists use `ResourceTable` inside `ResourceListPage`, `ResourceListSection` or `variant="embedded"`, and single-resource fields use `DefinitionCard`.
  - The only bespoke table is the query-result grid (see *Result grid*), which records the exception at its call site and on the import, as the `local/prefer-resource-table` rule requires.
- **Loading and busy states:** static skeletons (nothing shimmers). There are no spinners in content areas, and busy buttons follow the "`Running ▍`" cursor pattern.
- **Empty states:** "nothing exists yet" has a create call to action plus a sample-data action. "Nothing matches" offers to clear the filter. A load error beats both.
- **Dialogs** follow the `⏎`/`esc` contract. Every identifier gets a `CopyButton` or an `ArnLink`.
- **Colour:** semantic tokens and a `--cat-*` service colour only, with no raw palette hues. Both themes are checked.
- **Deep links:** filter and tab state bind to search params (`q`, `tab`), and a query execution is linkable by id.
- **Every home page** carries `ServiceDocsButton` first in its header actions, then Raw state, Refresh and Create, in the scaffold's order.
- **Every service** registers a global-search contributor.
- **Keyboard:**
  - `⌘/Ctrl+⏎` runs the query and `⇧⌘⏎` runs the selection;
  - `esc` in the editor stops the running query, and only then;
  - `⌘K` finds databases, tables, workgroups and named queries.
- **Emulation is never hidden.** When Overcast differs from AWS in a way the developer could trip on, the screen says so where it matters. For example: the engine is off, a Trino-only function was used, or the warehouse bucket is managed by S3 Tables. Use the shared `<Advisory>` treatment, compact, in sentence case, and linked to the docs. Never use a tooltip for something a developer must know.
- **PRs:** each console PR carries light and dark screenshots at a normal and a narrow (1024 px) width, following the pull-request skill's Visual Evidence section.

## Service registration and plumbing

Shared by every screen, so it lands first (W0, #2085):

- **Browser SDK clients:** `@aws-sdk/client-athena`, `client-glue` and `client-s3tables` in `services/aws-clients.ts`, with API modules under `services/api/`. That keeps the SDK-first policy: no `fetch` for AWS surfaces.
- **`service-registry.ts` entries:** Athena, Glue Data Catalog and S3 Tables, each with a lucide icon (trademark rule), a `--cat-*` slot, the `analytics` category, sub-nav `children`, dashboard copy and `docKey`.
- **ARN routing:** add workgroup, data catalog, Glue database and table, and S3 Tables bucket and table ARNs to `arn-routes.ts`, so every `ArnLink` in the app (CloudFormation resources, Step Functions I/O, traces) lands on the right page.
- **Search contributors:** `athena.ts` (workgroups, named queries), `glue.ts` (databases, and tables with their database as sublabel) and `s3tables.ts` (buckets, namespaces, tables).
- **Live-update events:** the Go services publish events on the bus, mirrored in `services/event-types.ts`, and `getEventQueryMap()` invalidates the matching keys:
  - `athena:QueryStateChanged` carries the id, workgroup, state, and the database and tables when known.
  - `glue:TableChanged`, `glue:PartitionsChanged`.
  - `s3tables:TableCommitted` carries the table ARN, the old and new metadata location, and the snapshot id and operation.
  - `s3tables:TableCreated` and `s3tables:TableDeleted`.

  Today none of these services publishes anything, so the console would otherwise have to poll.
- **Raw state:** `features/debug/namespaces.ts` labels for the new namespaces, so `RawStateLink` works.

## Athena — the query workspace

`/athena` opens on the **Editor**. That's the job; the lists are secondary. The
tabs are Editor · History · Saved queries · Workgroups · Data catalogs, each
bound to `?tab=`.

```
┌ Athena ──────────────────────────────── [Docs] [Raw state] ┐
│ Editor  History  Saved queries  Workgroups  Data catalogs  │
├──────────────┬─────────────────────────────────────────────┤
│ Catalog ▾    │ [query 1] [orders by day ×] [+]             │
│ Database ▾   │ ┌─────────────────────────────────────────┐ │
│ ⌕ filter     │ │ SELECT order_date, sum(amount)          │ │
│ ▸ orders  ⧉  │ │ FROM orders                             │ │
│   id  bigint │ │ GROUP BY 1 ORDER BY 1                   │ │
│   amount dbl │ └─────────────────────────────────────────┘ │
│   dt  ◆part  │ workgroup: primary ▾  [Run ⌘⏎] [Save] [⋯]   │
│ ▸ events ICE │ ─────────────────────────────────────────── │
│              │ ● SUCCEEDED · 0.8 s · 1.2 KB scanned · 31   │
│              │ ┌ result grid ───────────────────────────┐  │
│              │ │ order_date │ _col1                     │  │
│              │ └────────────────────────────────────────┘  │
│              │ [Copy ▾] [Download CSV] [Open in S3] [CLI]  │
└──────────────┴─────────────────────────────────────────────┘
```

### Data browser (left rail)

- The catalog and database pickers set the query context: the `QueryExecutionContext` sent with each run.
- Tables list with a format badge each: `CSV`, `JSON`, `PARQUET`, `ORC` or `ICEBERG`. Expand one to see its columns and types, with partition keys marked.
- **Click** a table or column to insert its qualified name at the cursor.
- **Row actions:**
  - *Preview*, which runs `SELECT * … LIMIT 10` in a new tab;
  - *Show DDL*;
  - *Open in Glue* (or *Open in S3 Tables*).
- **A filter box** for large catalogs. A catalog with no tables shows an empty state with two actions: *Create a table from S3 data* (the Glue wizard below) and *Load sample dataset*.

### Editor

- **Monaco** with the `sql` language, reusing `code-browser.tsx`'s setup and theme.
- **Completion:**
  - keywords;
  - the catalog, database, table and column names already loaded for the rail;
  - a static list of Trino functions with their signatures, generated from the pinned engine version so it can't drift.
- **Query tabs:**
  - several per user, each with its own SQL, workgroup, context and last execution id;
  - kept in `localStorage` (a per-viewer convenience; try/catch'd);
  - double-click a tab to rename it.
- **Parameters:** when the SQL has `?` placeholders, a parameters strip appears and its values are sent as `ExecutionParameters`. `EXECUTE stmt USING` works the same way.
- **Toolbar:**
  - a workgroup picker, which shows an *enforced* badge when the workgroup overrides the output location;
  - Run and Run selection;
  - Stop (visible only while a query runs);
  - Format, which normalises whitespace and keyword case;
  - Save as named query;
  - an overflow menu: Copy as AWS CLI, Copy as boto3, Copy as SDK v3 (JS).

  The three copy snippets have `--endpoint-url` filled in. That is a small, generic `copyAwsCommand` helper; none exists today.

### Running and results

- **The state chip** follows AWS's own lifecycle: `QUEUED` → `RUNNING` → `SUCCEEDED`, `FAILED` or `CANCELLED`, with a ticking elapsed time. A query that finishes too fast to see keeps a short dwell, as the map rules allow (display only; the API stays truthful).
- **Engine status** is part of the chip when it matters. The first query after start-up shows *Starting query engine · pulling image* or *· starting* with the step timings, so a ten-second cold start reads as progress, not a hang. It is fed by a small `/_overcast/athena/engine` status endpoint: an emulator-only path, `fetch` is allowed.
- **Inert mode** (`ATHENA_ENGINE=inert`, or no Docker) shows a persistent advisory above the editor: queries succeed with empty results, and the advisory says how to turn the engine on. The run button still works, so control-plane flows can be tested.
- **Statistics strip:** run time, data scanned, row count and statement type. *Details* opens the full `Statistics`, and `GetQueryRuntimeStatistics` when present.
- **DDL results** show one line, *Created table `sales.orders`*, linked to the Glue table. DML shows *`UpdateCount` rows affected*.
- **Failed queries:**
  - the error box shows `AthenaError`'s category and type and the message;
  - when Trino reports a line and column, the editor underlines it and scrolls there;
  - errors from the dialect-difference list in `athena.md` carry an advisory naming the difference.
- **Result actions:**
  - *Copy* as CSV, TSV, JSON or Markdown;
  - *Download CSV*, which is the real object at `OutputLocation`, with the `.metadata` beside it;
  - *Open in S3*, which goes to the result object in the S3 browser;
  - *Copy execution id*.

### Result grid

This is the one deliberate exception to `ResourceTable`. A result set has
arbitrary, typed columns and can hold thousands of rows, and it is not a
resource list. It is a virtualized grid (`@tanstack/react-virtual`, already a
dependency), reading pages from `GetQueryResults` as the user scrolls.

- **Cell rendering by type:**
  - numbers are right-aligned and mono;
  - `NULL` renders as a muted token, distinct from an empty string;
  - timestamps come with their timezone;
  - `array`, `map`, `row` and JSON cells are collapsed and expand in place through the existing `JsonValue` tree;
  - binary shows a length and hex preview.
- **Header tooltips** give the column's type. Column widths are resizable and remembered per query tab.
- **Sorting and filtering are client-side only** over the loaded rows, and are labelled so ("sorted locally"). Anything else would suggest the database sorted them.
- **Selecting cells** and pressing `⌘C` copies them as TSV.

### History, Saved queries, Workgroups, Data catalogs

- **History:**
  - a `ResourceTable` of executions: state, a one-line SQL snippet, workgroup, submitted time, duration, data scanned and statement type;
  - `defaultSort` by submitted time, newest first;
  - `expandedContent` shows the full SQL and the error;
  - row actions: *Open in editor*, *Re-run*, *Open result*;
  - `?execution=<id>` deep-links to one execution.
- **Saved queries:** named queries with description and workgroup. *Open in editor*, plus rename and delete.
- **Workgroups:** list, then detail.
  - Configuration goes in a `DefinitionCard`: output location, enforcement, engine version and bytes-scanned cutoff.
  - The prepared statements list lives here.
  - Create and edit use a `ResourceFormDialog`.
- **Data catalogs:** the built-in `AwsDataCatalog`, the `s3tablescatalog` federation once Phase 6 lands, and user-registered catalogs.

## Glue Data Catalog

The route is `/glue`, with Databases as the home page.

**Database detail:**
- a `DefinitionCard` for the database;
- a tables `ResourceTable` showing name, format badge, location (an `ArnLink`-style S3 link), partitioned or not, column count and updated time.

**Table detail tabs:**
- **Schema:**
  - columns with type and comment, then partition keys, marked;
  - Iceberg tables show field ids and required/optional;
  - a *Copy DDL* action generates the `CREATE EXTERNAL TABLE` or Iceberg `CREATE TABLE` that recreates the table.
- **Partitions:**
  - a `ResourceTable` of partition values, location and created time;
  - the filter box takes a Glue `Expression` (`dt >= '2026-09-01' AND region = 'eu'`), sent to `GetPartitions` exactly as an SDK would send it;
  - parse errors show inline under the box, word for word from the service;
  - on-page actions: add a partition, and *Discover partitions*, which runs `MSCK REPAIR TABLE` through Athena.
- **Data:**
  - the first rows, via *Preview* (Athena), with a *Query in Athena* action that opens the editor with the table in context;
  - when the engine is off, it instead lists the S3 objects under the location, with links to the S3 preview.
- **Iceberg** (only for `table_type=ICEBERG`): the current snapshot, the snapshot list (operation, summary counts, timestamp), and the metadata location linked to the object. The same component is reused by S3 Tables below.
- **Versions:** the `GetTableVersions` list. Selecting two versions shows a side-by-side diff of their table input. That's the quickest way to see what a CDK deploy or a PyIceberg commit changed.
- **Properties:** `Parameters`, SerDe and storage descriptor details as `DefinitionCard`s.

**Create table from S3 data** is the local stand-in for a crawler, which Overcast doesn't emulate. It's a three-step dialog:

1. Pick an S3 prefix with the S3 browser's own picker.
2. Overcast samples one object and infers the format and schema:
   - CSV header and value types;
   - JSON Lines keys;
   - Parquet's own schema, read in the browser with `hyparquet` (MIT, no dependencies, lazy-loaded).

   Hive-style `key=value/` prefixes become partition keys.
3. Review and edit the columns and types, name the database and table, and create through `CreateTable`, then optionally add the discovered partitions.

The result is a real Glue table that the developer could also have created in CDK. The dialog offers *Copy as CDK* (`CfnTable`) so they can.

## S3 Tables

The route is `/s3tables`, with Table buckets as the home page.

**Bucket detail tabs:**
- **Tables:** grouped by namespace, with create namespace and create table.
- **Maintenance:** stored configuration, with an advisory saying Overcast does not compact or expire snapshots.
- **Policy:** the policy JSON with an editor.
- **Encryption.**
- **Tags.**

**Table detail tabs:**
- **Overview:** a `DefinitionCard` with ARN, format, version token, created and modified times, and the metadata location. Its links go to the S3 browser, where the warehouse bucket is labelled as managed by S3 Tables.
- **Schema:** the current schema from `metadata.json`, plus its evolution: schema ids with the fields added and dropped.
- **Snapshots:** the same Iceberg component as Glue. Each row offers *Query as of this snapshot*, which opens Athena with `FOR VERSION AS OF <id>`, and *Diff with previous*, which shows the summary deltas.
- **Metadata:**
  - the raw `metadata.json` in a read-only viewer;
  - a picker across `metadata-log` versions, with a diff between any two;
  - the page updates live on `s3tables:TableCommitted`, so a developer running PyIceberg in a terminal watches commits arrive.
- **Policy** and **Maintenance.**

**Connect a client** is an always-visible panel on the bucket and table pages, and the highest-value QoL item here. It has ready-to-paste snippets with the endpoint, region, warehouse ARN and SigV4 settings filled in:

- PyIceberg (`load_catalog(… "rest.sigv4-enabled": "true", "rest.signing-name": "s3tables")`);
- Spark (`spark.sql.catalog.*` confs);
- Trino and DuckDB (`ATTACH … TYPE iceberg`);
- the AWS CLI.

Each snippet has a copy button. The same panel appears in the docs page, generated from one source so the two can't drift.

**Create table:** a schema builder (name, type, required, with nested structs added by indentation) and an optional partition spec. It calls `CreateTable` with `metadata.iceberg`. A *Copy as CDK* action gives `CfnTable` (S3 Tables).

## S3 console changes

- **Tabular preview:** CSV and TSV render as a table of the first rows, with a toggle back to raw text. JSON Lines renders as a table when its keys are uniform.
- **Parquet preview:** schema plus the first rows via `hyparquet`, lazy-loaded, reading only the footer and the first row group with Range requests. The existing 1 MiB cap stays for text; Parquet reads by range.
- **Iceberg metadata:** `*.metadata.json` gets the Iceberg metadata viewer. Avro manifests show *Avro — not previewed* rather than garbage.
- **Warehouse buckets** (`--table-s3`) carry a *Managed by S3 Tables* badge and a link to the owning table bucket. They are excluded from the S3 bucket list by default behind a *Show managed buckets* toggle, bound to `?managed=1`.
- **Prefix action** *Create Glue table from this prefix* opens the Glue wizard pre-filled.

## System map

The map is a workspace, not a status board (CONTRIBUTING § Topology map
methodology), and data services are where a developer most often loses track
of what reads from and writes to what. `internal/router/topology.go` has no
contributor interface, so the new nodes follow its existing pattern:
namespace scan, decode struct, and node and edge loops, plus `tsgen` for the
generated types. Pulling topology contributions out into a per-service interface
would be a cross-service refactor, and it's filed separately (W6, #2090) rather than
smuggled in.

### Nodes

| Node | Shape | Always visible | On-node action (the one first thing) |
| --- | --- | --- | --- |
| Athena workgroup | service node; `primary` shown only once it has executions | Last 3 executions as rows: state dot, SQL snippet, duration. Running rows pulse, and finished rows ghost out after a short dwell (the SQS row model). An engine chip in the header when starting or off. | **Run query**: a compact popover editor with the workgroup's last SQL and a result count. *Open in editor* for anything more. |
| Glue database | group node, collapsing on zoom like `stackGroup` | Table rows with a format badge and partition count | **Preview** a table: the first rows in a peek panel, the same pattern as `log-stream-peek` |
| S3 Tables bucket | group node | Namespace, then table rows with snapshot count | **Latest commit** peek: operation, added and removed records, time |

Iceberg tables in Glue and S3 Tables share one table-row component and one
visual-state model: a *write flash* on commit, and a *ghost* on drop.

### Edges

| Edge type | From → to | Source of truth |
| --- | --- | --- |
| `table-location` | Glue table → S3 bucket | `StorageDescriptor.Location` (bucket part); Iceberg: the metadata location's bucket |
| `query-results` | Athena workgroup → S3 bucket | Workgroup `OutputLocation` (enforced), or the last execution's `ResultConfiguration` |
| `queries` | Athena workgroup → Glue database or S3 Tables bucket | Databases and tables referenced by recent executions (`QueryExecutionContext`, plus the engine's table list), aged out after a window. **Dynamic**, so labelled "recent". |
| `federation` | Glue `s3tablescatalog` → S3 Tables bucket | Phase 6 |
| `writes` (later) | Firehose stream, Lambda or state machine → table | Future Firehose Iceberg destination, and Step Functions Athena tasks |

S3 Tables' warehouse buckets are **not** drawn as S3 nodes. They are an
implementation detail of the table bucket, and drawing them would double every
table. The table bucket node shows *warehouse* as a sub-label, linked to it.

Every new resource carries `stackName`, so it folds into CloudFormation stack
groups like everything else.

### Live overlay

These go in `use-event-animations.ts`, showing observability rather than
literal timing:

- **`athena:QueryStateChanged`:**
  - on `RUNNING`, the `queries` edge glows toward the database being read;
  - on `SUCCEEDED`, a pulse runs along `query-results` to the results bucket;
  - on `FAILED`, a red flash on the workgroup row.
- **`s3tables:TableCommitted` and Glue `UpdateTable` on an Iceberg table:** a write flash on the table row, with a burst count showing appended records.
- **`glue:PartitionsChanged`:** a partition count tick on the table row.

### Node routes

`nodeRoute()` entries for the workgroup (Athena editor with that workgroup
selected), Glue database and table, S3 Tables bucket and table, plus
open-in-new-tab.

## Beyond the console

The developer isn't always in the browser. These make the same loop work from a terminal or an agent:

- **`overcast athena query "<sql>"`:** runs in a workgroup and context, waits, and prints the result as a table (or `--json`, `--csv`). The first run prints the engine start-up progress to stderr. It is also the fastest smoke test of a stack. Flags: `--database`, `--workgroup`, `--output`.
- **MCP tools** (runtime provider), so an agent can check a data stack:
  - `athena_run_query`, which starts the query, waits with a bounded timeout, and returns the first N rows with the column types, the error, and the statistics;
  - `glue_describe_table`, which returns the schema, location, format and partition count;
  - `s3tables_table_snapshots`.
- **Sample dataset:**
  - *Load sample dataset* (console empty states) and `overcast samples load analytics` (CLI) both create a small bucket with partitioned CSV and Parquet, a Glue database with a table over it, and, when the engine is running, an Iceberg copy via CTAS;
  - the data is generated deterministically, so docs and screenshots match;
  - it is removed by `overcast reset athena glue` like anything else.
- **Health:**
  - `/_overcast/health` and the Metrics & Health page report the engine state: off, starting, ready or stopped-idle, with memory and uptime;
  - `overcast status` prints it;
  - a starting engine never makes the service report unhealthy.
- **Docs:**
  - an *Iceberg locally* guide (PyIceberg, Spark, Athena), using the same connect snippets as the console;
  - `athena.md`'s dialect-differences table, which the console's advisories link to.

## Issues

#2072 is replaced by these, each carrying the quality bar above as its
acceptance criteria:

| Id | Work | Depends on | Size |
| --- | --- | --- | --- |
| W0 #2085 | Registry, SDK clients, ARN routes, search contributors, raw-state labels; bus events published by Athena, Glue and S3 Tables | #2064, #2065, #2067 (per service) | M |
| W1 #2072 | Athena workspace: editor, results grid, history, saved queries, workgroups, engine status | W0, #2065 (#2066 for real results) | L |
| W2 #2086 | Glue catalog browser + create-from-S3 wizard | W0, #2064 | M |
| W3 #2087 | S3 Tables console + connect panel + Iceberg snapshot and metadata viewer | W0, #2067 (#2069 for snapshots) | M |
| W4 #2088 | S3 console: tabular, Parquet and Iceberg previews; managed warehouse buckets | #2067 for the badge | S–M |
| W5 #2089 | System map: nodes, edges, overlays and node routes for Athena, Glue and S3 Tables | W0 #2085 | M |
| W6 #2090 | Refactor: topology contributor interface (optional, separate) | — | M |
| W7 #2091 | Terminal and agent QoL: `overcast athena query`, MCP tools, sample dataset, engine health | #2066 | M |

W1 can build against the inert engine and a stubbed status endpoint. It
doesn't have to wait for Trino: the result grid is tested against recorded
`GetQueryResults` pages.
