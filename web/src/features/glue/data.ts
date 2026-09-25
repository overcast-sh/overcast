/**
 * Glue Data Catalog query keys.
 *
 * Key factory:
 *   glueKeys.all()         -> [...endpoint, "glue"]
 *   glueKeys.databases()   -> [...endpoint, "glue", "databases"]
 *   glueKeys.tables()      -> [...endpoint, "glue", "tables"]
 *   glueKeys.allTables()   -> [...endpoint, "glue", "tables", "all"]
 *   glueKeys.partitions()  -> [...endpoint, "glue", "partitions"]
 *
 * Every table query — a database's tables, one table and its detail — hangs
 * under `tables()`, and every partition query under `partitions()`: those are
 * the prefixes `glue:TableChanged` and `glue:PartitionsChanged` invalidate.
 */

import { endpointStore } from "@/services/endpoint-store"

export const glueKeys = {
  all: () => [...endpointStore.getKeys(), "glue"] as const,
  databases: () => [...glueKeys.all(), "databases"] as const,
  tables: () => [...glueKeys.all(), "tables"] as const,
  allTables: () => [...glueKeys.tables(), "all"] as const,
  partitions: () => [...glueKeys.all(), "partitions"] as const,
}
