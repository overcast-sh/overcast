import {
  CreateNamespaceCommand,
  CreateTableBucketCommand,
  CreateTableCommand,
  DeleteNamespaceCommand,
  DeleteTableBucketCommand,
  DeleteTableBucketPolicyCommand,
  DeleteTableCommand,
  DeleteTablePolicyCommand,
  GetTableBucketEncryptionCommand,
  GetTableBucketMaintenanceConfigurationCommand,
  GetTableBucketPolicyCommand,
  GetTableCommand,
  GetTableEncryptionCommand,
  GetTableMaintenanceConfigurationCommand,
  GetTablePolicyCommand,
  ListTagsForResourceCommand,
  PutTableBucketPolicyCommand,
  PutTablePolicyCommand,
  TagResourceCommand,
  UntagResourceCommand,
  paginateListNamespaces,
  paginateListTableBuckets,
  paginateListTables,
  type CreateTableCommandInput,
  type EncryptionConfiguration,
  type GetTableCommandOutput,
  type NamespaceSummary,
  type TableBucketSummary,
  type TableSummary,
} from "@aws-sdk/client-s3tables"
import { nullWhen } from "@/lib/aws-error"
import { awsClients } from "../aws-clients"
import { collectPages } from "./paginate"

/**
 * A namespace or table listed across buckets, with the name of the bucket it
 * is in: the summaries carry only the bucket's id.
 */
export type InTableBucket<T> = T & { tableBucketName: string }

export type Table = Omit<GetTableCommandOutput, "$metadata">
export type CreateTableInput = CreateTableCommandInput

/**
 * Which resource a policy, maintenance or encryption call is about. Buckets
 * and tables have parallel operations (`GetTableBucketPolicy` /
 * `GetTablePolicy`), and the tabs that show them are shared.
 */
export type S3TablesResource =
  | { kind: "bucket"; tableBucketARN: string }
  | { kind: "table"; tableBucketARN: string; namespace: string; name: string }

/** Maintenance settings as the API returns them: one entry per maintenance type. */
export type MaintenanceConfiguration = Record<
  string,
  { status?: string; settings?: Record<string, unknown> } | undefined
>

/** An optional configuration's absence is `NotFoundException`, not an empty answer. */
const orNone = <T>(call: Promise<T>) => nullWhen("NotFoundException", call)

/** The request members of a resource: everything but the `kind` tag. */
function members<R extends S3TablesResource>(resource: R): Omit<R, "kind"> {
  const { kind: _, ...rest } = resource
  return rest
}

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

const client = () => awsClients.s3tables()

export const s3tables = {
  listTableBuckets: (): Promise<TableBucketSummary[]> =>
    collectPages(paginateListTableBuckets({ client: client() }, {}), (page) => page.tableBuckets),

  createTableBucket: async (name: string): Promise<string> =>
    (await client().send(new CreateTableBucketCommand({ name }))).arn ?? "",

  deleteTableBucket: async (tableBucketARN: string): Promise<void> => {
    await client().send(new DeleteTableBucketCommand({ tableBucketARN }))
  },

  listNamespaces: (tableBucketARN: string): Promise<NamespaceSummary[]> =>
    collectPages(
      paginateListNamespaces({ client: client() }, { tableBucketARN }),
      (page) => page.namespaces,
    ),

  createNamespace: async (tableBucketARN: string, namespace: string): Promise<void> => {
    await client().send(new CreateNamespaceCommand({ tableBucketARN, namespace: [namespace] }))
  },

  deleteNamespace: async (tableBucketARN: string, namespace: string): Promise<void> => {
    await client().send(new DeleteNamespaceCommand({ tableBucketARN, namespace }))
  },

  /** The tables of one bucket, across all its namespaces. */
  listTables: (tableBucketARN: string): Promise<TableSummary[]> =>
    collectPages(
      paginateListTables({ client: client() }, { tableBucketARN }),
      (page) => page.tables,
    ),

  getTable: async (tableArn: string): Promise<Table> => {
    const { $metadata: _, ...table } = await client().send(new GetTableCommand({ tableArn }))
    return table
  },

  createTable: async (input: CreateTableInput): Promise<string> =>
    (await client().send(new CreateTableCommand(input))).tableARN ?? "",

  deleteTable: async (table: {
    tableBucketARN: string
    namespace: string
    name: string
  }): Promise<void> => {
    await client().send(new DeleteTableCommand(table))
  },

  /** The resource policy, or null when none is set. */
  getPolicy: async (resource: S3TablesResource): Promise<string | null> => {
    const answer = await orNone(
      resource.kind === "bucket"
        ? client().send(new GetTableBucketPolicyCommand(members(resource)))
        : client().send(new GetTablePolicyCommand(members(resource))),
    )
    return answer?.resourcePolicy ?? null
  },

  putPolicy: async (resource: S3TablesResource, resourcePolicy: string): Promise<void> => {
    await (resource.kind === "bucket"
      ? client().send(new PutTableBucketPolicyCommand({ ...members(resource), resourcePolicy }))
      : client().send(new PutTablePolicyCommand({ ...members(resource), resourcePolicy })))
  },

  deletePolicy: async (resource: S3TablesResource): Promise<void> => {
    await (resource.kind === "bucket"
      ? client().send(new DeleteTableBucketPolicyCommand(members(resource)))
      : client().send(new DeleteTablePolicyCommand(members(resource))))
  },

  getMaintenance: async (resource: S3TablesResource): Promise<MaintenanceConfiguration> => {
    const answer =
      resource.kind === "bucket"
        ? await client().send(new GetTableBucketMaintenanceConfigurationCommand(members(resource)))
        : await client().send(new GetTableMaintenanceConfigurationCommand(members(resource)))
    return (answer.configuration ?? {}) as MaintenanceConfiguration
  },

  getEncryption: async (resource: S3TablesResource): Promise<EncryptionConfiguration | null> => {
    const answer = await orNone(
      resource.kind === "bucket"
        ? client().send(new GetTableBucketEncryptionCommand(members(resource)))
        : client().send(new GetTableEncryptionCommand(members(resource))),
    )
    return answer?.encryptionConfiguration ?? null
  },

  listTags: async (resourceArn: string): Promise<Record<string, string>> =>
    (await client().send(new ListTagsForResourceCommand({ resourceArn }))).tags ?? {},

  tagResource: async (resourceArn: string, tags: Record<string, string>): Promise<void> => {
    await client().send(new TagResourceCommand({ resourceArn, tags }))
  },

  untagResource: async (resourceArn: string, tagKeys: string[]): Promise<void> => {
    await client().send(new UntagResourceCommand({ resourceArn, tagKeys }))
  },

  /** The namespaces of every table bucket. */
  listAllNamespaces: () => acrossBuckets(s3tables.listNamespaces),

  /** The tables of every table bucket. */
  listAllTables: () => acrossBuckets(s3tables.listTables),
}
