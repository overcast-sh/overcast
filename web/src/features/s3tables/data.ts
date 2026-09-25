/**
 * S3 Tables query keys.
 *
 * Key factory:
 *   s3tablesKeys.all()            -> [...endpoint, "s3tables"]
 *   s3tablesKeys.buckets()        -> [...endpoint, "s3tables", "buckets"]
 *   s3tablesKeys.namespaces()     -> [...endpoint, "s3tables", "namespaces"]
 *   s3tablesKeys.allNamespaces()  -> [...endpoint, "s3tables", "namespaces", "all"]
 *   s3tablesKeys.tables()         -> [...endpoint, "s3tables", "tables"]
 *   s3tablesKeys.allTables()      -> [...endpoint, "s3tables", "tables", "all"]
 *
 * Every table query — a bucket's tables, one table, its metadata — hangs
 * under `tables()`, the prefix every `s3tables:Table*` event invalidates.
 */

import { endpointStore } from "@/services/endpoint-store"

export const s3tablesKeys = {
  all: () => [...endpointStore.getKeys(), "s3tables"] as const,
  buckets: () => [...s3tablesKeys.all(), "buckets"] as const,
  namespaces: () => [...s3tablesKeys.all(), "namespaces"] as const,
  allNamespaces: () => [...s3tablesKeys.namespaces(), "all"] as const,
  tables: () => [...s3tablesKeys.all(), "tables"] as const,
  allTables: () => [...s3tablesKeys.tables(), "all"] as const,
}
