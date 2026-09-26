import { awsCommand, type AwsService } from "@/lib/aws-command"

/**
 * The *Connect a client* snippets: ready-to-paste configuration for the
 * Iceberg clients developers point at S3 Tables, with this emulator's
 * endpoint, region and table bucket filled in.
 *
 * One source for two readers. The console's panel renders these for the
 * bucket or table on screen; `docs/services/s3tables/iceberg-rest.md` and
 * `docs/iceberg-locally.md` carry the same snippets for the documented example
 * bucket, and `connect-snippets.docs.test.ts` fails when they drift. Change a snippet
 * here, then regenerate the docs with
 * `UPDATE_DOCS=1 pnpm vitest run connect-snippets.docs`.
 */

export interface ConnectTarget {
  /** The emulator's base URL, `http://localhost:4566`. */
  endpoint: string
  region: string
  /** The table bucket's ARN, which Iceberg clients call the warehouse. */
  warehouseArn: string
  /** Set on a table's page, so each snippet ends by reading that table. */
  table?: { namespace: string; name: string }
}

export type ConnectClient = "pyiceberg" | "spark" | "trino" | "duckdb" | "aws-cli"

export interface ConnectSnippet {
  client: ConnectClient
  label: string
  /** The fence language in the docs; also the highlight grammar where the console has one. */
  language: "python" | "bash" | "properties" | "sql"
  code: string
}

export const S3TABLES_SERVICE: AwsService = {
  cli: "s3tables",
  sdkPackage: "@aws-sdk/client-s3tables",
  sdkClient: "S3TablesClient",
}

/** The name each client gives the catalog, used again in its queries. */
const CATALOG = "s3tables"

const catalogUri = (t: ConnectTarget) => `${t.endpoint}/iceberg`

/** The same catalog without SigV4, for a client that cannot sign for `s3tables`. */
export const unsignedCatalogUri = (t: ConnectTarget) => `${t.endpoint}/_overcast/s3tables/iceberg`

const qualified = (t: ConnectTarget, catalog: string) =>
  t.table ? `${catalog}.${t.table.namespace}.${t.table.name}` : undefined

/** A client in a container reaches the host's emulator by another name. */
function containerNote(t: ConnectTarget, comment: string): string[] {
  return /\/\/(localhost|127\.0\.0\.1)[:/]/.test(`${t.endpoint}/`)
    ? [`${comment} From a container, use host.docker.internal in place of localhost.`]
    : []
}

function pyiceberg(t: ConnectTarget): string {
  const read = t.table
    ? [
        `table = catalog.load_table("${t.table.namespace}.${t.table.name}")`,
        "print(table.scan().to_arrow())",
      ]
    : ["print(catalog.list_namespaces())"]
  return [
    "from pyiceberg.catalog import load_catalog",
    "",
    `catalog = load_catalog("${CATALOG}", **{`,
    `    "type": "rest",`,
    `    "uri": "${catalogUri(t)}",`,
    `    "warehouse": "${t.warehouseArn}",`,
    `    "rest.sigv4-enabled": "true",`,
    `    "rest.signing-name": "s3tables",`,
    `    "rest.signing-region": "${t.region}",`,
    "})",
    ...read,
  ].join("\n")
}

function spark(t: ConnectTarget): string {
  const conf = (key: string, value: string) =>
    `  --conf spark.sql.catalog.${CATALOG}${key}=${value}`
  const lines = [
    "spark-shell \\",
    "  --packages org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.9.2,org.apache.iceberg:iceberg-aws-bundle:1.9.2 \\",
    "  --conf spark.sql.extensions=org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions \\",
    `${conf("", "org.apache.iceberg.spark.SparkCatalog")} \\`,
    `${conf(".type", "rest")} \\`,
    `${conf(".uri", catalogUri(t))} \\`,
    `${conf(".warehouse", t.warehouseArn)} \\`,
    `${conf(".rest.sigv4-enabled", "true")} \\`,
    `${conf(".rest.signing-name", "s3tables")} \\`,
    `${conf(".rest.signing-region", t.region)} \\`,
    conf(".io-impl", "org.apache.iceberg.aws.s3.S3FileIO"),
  ]
  const table = qualified(t, CATALOG)
  return table ? [...lines, `# then: spark.table("${table}").show()`].join("\n") : lines.join("\n")
}

function trino(t: ConnectTarget): string {
  return [
    `# etc/catalog/${CATALOG}.properties`,
    ...containerNote(t, "#"),
    "connector.name=iceberg",
    "iceberg.catalog.type=rest",
    `iceberg.rest-catalog.uri=${catalogUri(t)}`,
    `iceberg.rest-catalog.warehouse=${t.warehouseArn}`,
    "iceberg.rest-catalog.security=SIGV4",
    "iceberg.rest-catalog.signing-name=s3tables",
    "fs.native-s3.enabled=true",
    `s3.endpoint=${t.endpoint}`,
    `s3.region=${t.region}`,
    "s3.path-style-access=true",
    "s3.aws-access-key=test",
    "s3.aws-secret-key=test",
  ].join("\n")
}

function duckdb(t: ConnectTarget): string {
  const host = t.endpoint.replace(/^https?:\/\//, "")
  const table = qualified(t, CATALOG)
  return [
    "INSTALL iceberg;",
    "LOAD iceberg;",
    "-- DuckDB signs for S3 Tables only against AWS, so use Overcast's unsigned catalog.",
    "CREATE SECRET (",
    `  TYPE s3, KEY_ID 'test', SECRET 'test', REGION '${t.region}',`,
    `  ENDPOINT '${host}', URL_STYLE 'path', USE_SSL ${t.endpoint.startsWith("https:")}`,
    ");",
    `ATTACH '${t.warehouseArn}' AS ${CATALOG} (`,
    `  TYPE iceberg, ENDPOINT '${unsignedCatalogUri(t)}', AUTHORIZATION_TYPE 'none'`,
    ");",
    table ? `SELECT * FROM ${table} LIMIT 10;` : `SHOW ALL TABLES;`,
  ].join("\n")
}

function awsCli(t: ConnectTarget): string {
  const call = t.table
    ? {
        operation: "GetTableMetadataLocation",
        input: { tableBucketARN: t.warehouseArn, namespace: t.table.namespace, name: t.table.name },
      }
    : { operation: "ListTables", input: { tableBucketARN: t.warehouseArn } }
  return awsCommand({ service: S3TABLES_SERVICE, ...call }, "cli", t)
}

export function connectSnippets(target: ConnectTarget): ConnectSnippet[] {
  return [
    { client: "pyiceberg", label: "PyIceberg", language: "python", code: pyiceberg(target) },
    { client: "spark", label: "Spark", language: "bash", code: spark(target) },
    { client: "trino", label: "Trino", language: "properties", code: trino(target) },
    { client: "duckdb", label: "DuckDB", language: "sql", code: duckdb(target) },
    { client: "aws-cli", label: "AWS CLI", language: "bash", code: awsCli(target) },
  ]
}

/** The target the docs page documents: the default endpoint and an example bucket. */
export const DOCS_CONNECT_TARGET: ConnectTarget = {
  endpoint: "http://localhost:4566",
  region: "us-east-1",
  warehouseArn: "arn:aws:s3tables:us-east-1:000000000000:bucket/analytics",
  table: { namespace: "sales", name: "orders" },
}
