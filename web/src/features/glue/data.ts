/**
 * Glue Data Catalog query keys and query/mutation options.
 *
 * Key factory:
 *   glueKeys.all()                     -> [...endpoint, "glue"]
 *   glueKeys.databases()               -> [...endpoint, "glue", "databases"]
 *   glueKeys.database(name)            -> [...databases(), name]
 *   glueKeys.tables()                  -> [...endpoint, "glue", "tables"]
 *   glueKeys.allTables()               -> [...tables(), "all"]
 *   glueKeys.databaseTables(db)        -> [...tables(), "list", db]
 *   glueKeys.table(db, name)           -> [...tables(), "detail", db, name]
 *   glueKeys.tableVersions(db, name)   -> [...tables(), "versions", db, name]
 *   glueKeys.partitions()              -> [...endpoint, "glue", "partitions"]
 *   glueKeys.partitionList(db, t, expr)-> [...partitions(), db, t, expr]
 *   glueKeys.prefixScan(bucket, prefix), glueKeys.sample(bucket, object)
 *
 * Every table query — a database's tables, one table and its versions —
 * hangs under `tables()`, and every partition query under `partitions()`:
 * those are the prefixes `glue:TableChanged` and `glue:PartitionsChanged`
 * invalidate.
 */

import { mutationOptions, queryOptions } from "@tanstack/react-query"
import type { PartitionInput, TableInput } from "@aws-sdk/client-glue"
import { athena, glue } from "@/services/api"
import { endpointStore } from "@/services/endpoint-store"
import type { ListedObject } from "./hive-partitions"
import { sampleSchema, scanPrefix } from "./scan-prefix"

export const glueKeys = {
  all: () => [...endpointStore.getKeys(), "glue"] as const,
  databases: () => [...glueKeys.all(), "databases"] as const,
  database: (name: string) => [...glueKeys.databases(), name] as const,
  tables: () => [...glueKeys.all(), "tables"] as const,
  allTables: () => [...glueKeys.tables(), "all"] as const,
  databaseTables: (database: string) => [...glueKeys.tables(), "list", database] as const,
  table: (database: string, name: string) =>
    [...glueKeys.tables(), "detail", database, name] as const,
  tableVersions: (database: string, name: string) =>
    [...glueKeys.tables(), "versions", database, name] as const,
  partitions: () => [...glueKeys.all(), "partitions"] as const,
  partitionList: (database: string, table: string, expression: string) =>
    [...glueKeys.partitions(), database, table, expression] as const,
  s3: () => [...glueKeys.all(), "s3"] as const,
  prefixScan: (bucket: string, prefix: string) =>
    [...glueKeys.s3(), "scan", bucket, prefix] as const,
  sample: (bucket: string, object: ListedObject | undefined) =>
    [...glueKeys.s3(), "sample", bucket, object?.key ?? "", object?.size ?? 0] as const,
}

// ─── Queries ───────────────────────────────────────────────────────────────

export function glueDatabasesQueryOptions() {
  return queryOptions({ queryKey: glueKeys.databases(), queryFn: () => glue.listDatabases() })
}

export function glueDatabaseQueryOptions(name: string) {
  return queryOptions({ queryKey: glueKeys.database(name), queryFn: () => glue.getDatabase(name) })
}

/** Every table in the catalog — the key the search contributor fills too. */
export function glueAllTablesQueryOptions() {
  return queryOptions({ queryKey: glueKeys.allTables(), queryFn: () => glue.listAllTables() })
}

export function glueTablesQueryOptions(database: string) {
  return queryOptions({
    queryKey: glueKeys.databaseTables(database),
    queryFn: () => glue.listTables(database),
  })
}

export function glueTableQueryOptions(database: string, name: string) {
  return queryOptions({
    queryKey: glueKeys.table(database, name),
    queryFn: () => glue.getTable(database, name),
  })
}

export function glueTableVersionsQueryOptions(database: string, name: string) {
  return queryOptions({
    queryKey: glueKeys.tableVersions(database, name),
    queryFn: () => glue.listTableVersions(database, name),
  })
}

/**
 * The partitions matching a Glue `Expression`. A parse error is the
 * service's InvalidInputException, shown word for word, so it is not retried.
 */
export function gluePartitionsQueryOptions(database: string, table: string, expression: string) {
  return queryOptions({
    queryKey: glueKeys.partitionList(database, table, expression),
    queryFn: () => glue.listPartitions(database, table, expression),
    retry: false,
  })
}

/** A prefix's partition layout and the object its schema is sampled from. */
export function gluePrefixScanQueryOptions(bucket: string, prefix: string) {
  return queryOptions({
    queryKey: glueKeys.prefixScan(bucket, prefix),
    queryFn: () => scanPrefix(bucket, prefix),
    enabled: bucket !== "",
    staleTime: Infinity,
  })
}

/** The schema inferred from one object. The key carries the size, so an overwrite is read again. */
export function glueSampleSchemaQueryOptions(bucket: string, object: ListedObject | undefined) {
  return queryOptions({
    queryKey: glueKeys.sample(bucket, object),
    queryFn: () => sampleSchema(bucket, object ?? { key: "", size: 0 }),
    enabled: object !== undefined,
    retry: false,
    staleTime: Infinity,
  })
}

// ─── Mutations ─────────────────────────────────────────────────────────────

export interface CreateTableVars {
  database: string
  /** The database does not exist yet, so it is created first. */
  createDatabase: boolean
  tableInput: TableInput
  /** Discovered partitions to add once the table exists. */
  partitions: PartitionInput[]
}

/** CreateTable, then the partitions; resolves with any partition BatchCreatePartition refused. */
export function createTableMutationOptions() {
  return mutationOptions({
    mutationKey: [...glueKeys.tables(), "create"] as const,
    mutationFn: async ({ database, createDatabase, tableInput, partitions }: CreateTableVars) => {
      if (createDatabase) await glue.createDatabase(database)
      await glue.createTable(database, tableInput)
      if (partitions.length === 0) return []
      return glue.batchCreatePartitions(database, tableInput.Name ?? "", partitions)
    },
  })
}

export function createPartitionMutationOptions(database: string, table: string) {
  return mutationOptions({
    mutationKey: [...glueKeys.partitions(), "create", database, table] as const,
    mutationFn: (input: PartitionInput) => glue.createPartition(database, table, input),
  })
}

/**
 * *Discover partitions*: `MSCK REPAIR TABLE`, run through Athena the way a
 * developer would run it, so the partitions added are the ones Athena
 * finds. Resolves with the finished execution, a FAILED one included.
 */
export function repairTableMutationOptions(database: string) {
  return mutationOptions({
    mutationKey: [...glueKeys.partitions(), "repair", database] as const,
    mutationFn: (sql: string) => athena.runQuery({ sql, context: { Database: database } }),
  })
}
