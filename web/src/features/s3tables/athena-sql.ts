/**
 * SQL for reading an S3 Tables table from Athena, which reaches every table
 * bucket through the `s3tablescatalog/<bucket>` catalog.
 * https://docs.aws.amazon.com/athena/latest/ug/gdc-register-s3-table-bucket-cat.html
 */

/** An identifier double-quoted, as Trino's SQL quotes one. */
function quoted(identifier: string): string {
  return `"${identifier.replaceAll('"', '""')}"`
}

/** The Athena catalog a table bucket is queried through. */
export function s3tablesCatalog(bucket: string): string {
  return `s3tablescatalog/${bucket}`
}

/** `SELECT` the table as it was at one snapshot — Iceberg time travel. */
export function snapshotQuerySql(
  bucket: string,
  namespace: string,
  table: string,
  snapshotId: string,
): string {
  const name = [s3tablesCatalog(bucket), namespace, table].map(quoted).join(".")
  return `SELECT * FROM ${name} FOR VERSION AS OF ${snapshotId} LIMIT 100`
}
