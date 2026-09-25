import {
  paginateListNamespaces,
  paginateListTableBuckets,
  paginateListTables,
  type NamespaceSummary,
  type TableBucketSummary,
  type TableSummary,
} from "@aws-sdk/client-s3tables"
import { awsClients } from "../aws-clients"
import { collectPages } from "./paginate"

/**
 * A namespace or table listed across buckets, with the name of the bucket it
 * is in: the summaries carry only the bucket's id.
 */
export type InTableBucket<T> = T & { tableBucketName: string }

/** Runs `list` over every table bucket and concatenates what it returns. */
async function acrossBuckets<T>(
  list: (tableBucketARN: string) => Promise<T[]>,
): Promise<InTableBucket<T>[]> {
  const buckets = await s3tables.listTableBuckets()
  const perBucket = await Promise.all(
    buckets.map(async (b) =>
      (await list(b.arn ?? "")).map((item) => ({ ...item, tableBucketName: b.name ?? "" })),
    ),
  )
  return perBucket.flat()
}

export const s3tables = {
  listTableBuckets: (): Promise<TableBucketSummary[]> =>
    collectPages(
      paginateListTableBuckets({ client: awsClients.s3tables() }, {}),
      (page) => page.tableBuckets,
    ),

  listNamespaces: (tableBucketARN: string): Promise<NamespaceSummary[]> =>
    collectPages(
      paginateListNamespaces({ client: awsClients.s3tables() }, { tableBucketARN }),
      (page) => page.namespaces,
    ),

  /** The tables of one bucket, across all its namespaces. */
  listTables: (tableBucketARN: string): Promise<TableSummary[]> =>
    collectPages(
      paginateListTables({ client: awsClients.s3tables() }, { tableBucketARN }),
      (page) => page.tables,
    ),

  /** The namespaces of every table bucket. */
  listAllNamespaces: () => acrossBuckets(s3tables.listNamespaces),

  /** The tables of every table bucket. */
  listAllTables: () => acrossBuckets(s3tables.listTables),
}
