/**
 * S3 Tables query keys, query options and mutations.
 *
 * Key factory:
 *   s3tablesKeys.all()                  -> [...endpoint, "s3tables"]
 *   s3tablesKeys.buckets()              -> [...endpoint, "s3tables", "buckets"]
 *   s3tablesKeys.bucketConfig(arn, k)   -> [...buckets(), "config", arn, k]
 *   s3tablesKeys.namespaces()           -> [...endpoint, "s3tables", "namespaces"]
 *   s3tablesKeys.bucketNamespaces(arn)  -> [...namespaces(), arn]
 *   s3tablesKeys.allNamespaces()        -> [...namespaces(), "all"]
 *   s3tablesKeys.tables()               -> [...endpoint, "s3tables", "tables"]
 *   s3tablesKeys.bucketTables(arn)      -> [...tables(), "bucket", arn]
 *   s3tablesKeys.allTables()            -> [...tables(), "all"]
 *   s3tablesKeys.table(arn)             -> [...tables(), "table", arn]
 *   s3tablesKeys.tableConfig(arn, k)    -> [...table(arn), "config", k]
 *
 * Every table query — a bucket's tables, one table, its configuration —
 * hangs under `tables()`, the prefix every `s3tables:Table*` event
 * invalidates, so a commit from PyIceberg in a terminal re-reads the table's
 * metadata pointer and the page follows it.
 */

import { mutationOptions, queryOptions } from "@tanstack/react-query"
import { s3tables } from "@/services/api"
import type { CreateTableInput, S3TablesResource } from "@/services/api/s3tables"
import { endpointStore } from "@/services/endpoint-store"

/** The configurations a bucket and a table each have a tab for. */
export type ConfigKind = "policy" | "maintenance" | "encryption" | "tags"

export const s3tablesKeys = {
  all: () => [...endpointStore.getKeys(), "s3tables"] as const,
  buckets: () => [...s3tablesKeys.all(), "buckets"] as const,
  bucketConfig: (arn: string, kind: ConfigKind) =>
    [...s3tablesKeys.buckets(), "config", arn, kind] as const,
  namespaces: () => [...s3tablesKeys.all(), "namespaces"] as const,
  bucketNamespaces: (arn: string) => [...s3tablesKeys.namespaces(), arn] as const,
  allNamespaces: () => [...s3tablesKeys.namespaces(), "all"] as const,
  tables: () => [...s3tablesKeys.all(), "tables"] as const,
  bucketTables: (arn: string) => [...s3tablesKeys.tables(), "bucket", arn] as const,
  allTables: () => [...s3tablesKeys.tables(), "all"] as const,
  table: (arn: string) => [...s3tablesKeys.tables(), "table", arn] as const,
  tableConfig: (arn: string, kind: ConfigKind) =>
    [...s3tablesKeys.table(arn), "config", kind] as const,
}

/** A table's ARN from its bucket's and its id: `<bucket ARN>/table/<id>`. */
export function tableArn(tableBucketARN: string, tableId: string): string {
  return `${tableBucketARN}/table/${tableId}`
}

/** The id a table's ARN ends with, which its page is addressed by. */
export function tableIdOf(tableARN: string | undefined): string {
  return tableARN?.split("/").pop() ?? ""
}

/**
 * A bucket or a table, as the configuration tabs address it: the resource
 * for the API call, and the ARN its cache entries and tags are keyed by.
 */
export interface ConfigTarget {
  resource: S3TablesResource
  arn: string
}

/** The key a target's configuration of `kind` lives under — also what a change to it invalidates. */
export function configKey({ resource, arn }: ConfigTarget, kind: ConfigKind) {
  return resource.kind === "bucket"
    ? s3tablesKeys.bucketConfig(arn, kind)
    : s3tablesKeys.tableConfig(arn, kind)
}

// ─── Queries ──────────────────────────────────────────────────────────────

export function tableBucketsQueryOptions() {
  return queryOptions({
    queryKey: s3tablesKeys.buckets(),
    queryFn: () => s3tables.listTableBuckets(),
  })
}

export function namespacesQueryOptions(tableBucketARN: string) {
  return queryOptions({
    queryKey: s3tablesKeys.bucketNamespaces(tableBucketARN),
    queryFn: () => s3tables.listNamespaces(tableBucketARN),
    enabled: tableBucketARN !== "",
  })
}

export function bucketTablesQueryOptions(tableBucketARN: string) {
  return queryOptions({
    queryKey: s3tablesKeys.bucketTables(tableBucketARN),
    queryFn: () => s3tables.listTables(tableBucketARN),
    enabled: tableBucketARN !== "",
  })
}

export function tableQueryOptions(tableARN: string) {
  return queryOptions({
    queryKey: s3tablesKeys.table(tableARN),
    queryFn: () => s3tables.getTable(tableARN),
    enabled: tableARN !== "",
  })
}

export function policyQueryOptions(target: ConfigTarget) {
  return queryOptions({
    queryKey: configKey(target, "policy"),
    queryFn: () => s3tables.getPolicy(target.resource),
  })
}

export function maintenanceQueryOptions(target: ConfigTarget) {
  return queryOptions({
    queryKey: configKey(target, "maintenance"),
    queryFn: () => s3tables.getMaintenance(target.resource),
  })
}

export function encryptionQueryOptions(target: ConfigTarget) {
  return queryOptions({
    queryKey: configKey(target, "encryption"),
    queryFn: () => s3tables.getEncryption(target.resource),
  })
}

export function tagsQueryOptions(target: ConfigTarget) {
  return queryOptions({
    queryKey: configKey(target, "tags"),
    queryFn: () => s3tables.listTags(target.arn),
  })
}

// ─── Mutations ────────────────────────────────────────────────────────────

export function createTableBucketMutationOptions() {
  return mutationOptions({
    mutationKey: [...s3tablesKeys.buckets(), "create"] as const,
    mutationFn: (name: string) => s3tables.createTableBucket(name),
  })
}

export function deleteTableBucketMutationOptions() {
  return mutationOptions({
    mutationKey: [...s3tablesKeys.buckets(), "delete"] as const,
    mutationFn: (tableBucketARN: string) => s3tables.deleteTableBucket(tableBucketARN),
  })
}

export function createNamespaceMutationOptions(tableBucketARN: string) {
  return mutationOptions({
    mutationKey: [...s3tablesKeys.bucketNamespaces(tableBucketARN), "create"] as const,
    mutationFn: (namespace: string) => s3tables.createNamespace(tableBucketARN, namespace),
  })
}

export function deleteNamespaceMutationOptions(tableBucketARN: string) {
  return mutationOptions({
    mutationKey: [...s3tablesKeys.bucketNamespaces(tableBucketARN), "delete"] as const,
    mutationFn: (namespace: string) => s3tables.deleteNamespace(tableBucketARN, namespace),
  })
}

export function createTableMutationOptions() {
  return mutationOptions({
    mutationKey: [...s3tablesKeys.tables(), "create"] as const,
    mutationFn: (input: CreateTableInput) => s3tables.createTable(input),
  })
}

export function deleteTableMutationOptions() {
  return mutationOptions({
    mutationKey: [...s3tablesKeys.tables(), "delete"] as const,
    mutationFn: (table: { tableBucketARN: string; namespace: string; name: string }) =>
      s3tables.deleteTable(table),
  })
}

export function putPolicyMutationOptions(target: ConfigTarget) {
  return mutationOptions({
    mutationKey: [...configKey(target, "policy"), "put"] as const,
    mutationFn: (policy: string) => s3tables.putPolicy(target.resource, policy),
  })
}

export function deletePolicyMutationOptions(target: ConfigTarget) {
  return mutationOptions({
    mutationKey: [...configKey(target, "policy"), "delete"] as const,
    mutationFn: () => s3tables.deletePolicy(target.resource),
  })
}

export function tagResourceMutationOptions(target: ConfigTarget) {
  return mutationOptions({
    mutationKey: [...configKey(target, "tags"), "tag"] as const,
    mutationFn: (tags: Record<string, string>) => s3tables.tagResource(target.arn, tags),
  })
}

export function untagResourceMutationOptions(target: ConfigTarget) {
  return mutationOptions({
    mutationKey: [...configKey(target, "tags"), "untag"] as const,
    mutationFn: (keys: string[]) => s3tables.untagResource(target.arn, keys),
  })
}
