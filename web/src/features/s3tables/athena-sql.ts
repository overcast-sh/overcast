import type { IcebergTableRef } from "@/components/iceberg/snapshot-sql"

/**
 * The Athena catalog a table bucket is queried through: Athena reaches every
 * table bucket as `s3tablescatalog/<bucket>`.
 * https://docs.aws.amazon.com/athena/latest/ug/gdc-register-s3-table-bucket-cat.html
 */
export function s3tablesCatalog(bucket: string): string {
  return `s3tablescatalog/${bucket}`
}

/** Where Athena finds an S3 Tables table: in its bucket's catalog, with its namespace as the database. */
export function s3tablesTableRef(
  bucket: string,
  namespace: string,
  table: string,
): IcebergTableRef {
  return { catalog: s3tablesCatalog(bucket), database: namespace, table }
}
