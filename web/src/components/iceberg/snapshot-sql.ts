import { trinoIdentifier } from "@/lib/sql-quote"

/** Where Athena finds an Iceberg table: its catalog, database and name. */
export interface IcebergTableRef {
  /** `AwsDataCatalog` for a Glue table, `s3tablescatalog/<bucket>` for an S3 Tables one. */
  catalog: string
  database: string
  table: string
}

/** `SELECT` the table as it was at one snapshot — Iceberg time travel in Athena. */
export function snapshotQuerySql(ref: IcebergTableRef, snapshotId: string): string {
  const name = [ref.catalog, ref.database, ref.table].map(trinoIdentifier).join(".")
  return `SELECT * FROM ${name} FOR VERSION AS OF ${snapshotId} LIMIT 100`
}
