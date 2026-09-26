import {
  BatchCreatePartitionCommand,
  CreateDatabaseCommand,
  CreatePartitionCommand,
  CreateTableCommand,
  GetDatabaseCommand,
  GetTableCommand,
  paginateGetDatabases,
  paginateGetPartitions,
  paginateGetTables,
  paginateGetTableVersions,
  type Database,
  type Partition,
  type PartitionError,
  type PartitionInput,
  type Table,
  type TableInput,
  type TableVersion,
} from "@aws-sdk/client-glue"
import { awsClients } from "../aws-clients"
import { collectPages } from "./paginate"

/** BatchCreatePartition's limit on partitions per call. */
const PARTITION_BATCH = 100

export const glue = {
  listDatabases: (): Promise<Database[]> =>
    collectPages(
      paginateGetDatabases({ client: awsClients.glue() }, {}),
      (page) => page.DatabaseList,
    ),

  getDatabase: async (name: string): Promise<Database> => {
    const res = await awsClients.glue().send(new GetDatabaseCommand({ Name: name }))
    return res.Database ?? { Name: name }
  },

  createDatabase: async (name: string): Promise<void> => {
    await awsClients.glue().send(new CreateDatabaseCommand({ DatabaseInput: { Name: name } }))
  },

  listTables: (databaseName: string): Promise<Table[]> =>
    collectPages(
      paginateGetTables({ client: awsClients.glue() }, { DatabaseName: databaseName }),
      (page) => page.TableList,
    ),

  /** The tables of every database. */
  listAllTables: async (): Promise<Table[]> => {
    const databases = await glue.listDatabases()
    const perDatabase = await Promise.all(databases.map((db) => glue.listTables(db.Name ?? "")))
    return perDatabase.flat()
  },

  getTable: async (databaseName: string, name: string): Promise<Table> => {
    const res = await awsClients
      .glue()
      .send(new GetTableCommand({ DatabaseName: databaseName, Name: name }))
    return res.Table ?? { Name: name, DatabaseName: databaseName }
  },

  createTable: async (databaseName: string, tableInput: TableInput): Promise<void> => {
    await awsClients
      .glue()
      .send(new CreateTableCommand({ DatabaseName: databaseName, TableInput: tableInput }))
  },

  /** Every version GetTableVersions keeps. */
  listTableVersions: (databaseName: string, tableName: string): Promise<TableVersion[]> =>
    collectPages(
      paginateGetTableVersions(
        { client: awsClients.glue() },
        { DatabaseName: databaseName, TableName: tableName },
      ),
      (page) => page.TableVersions,
    ),

  /**
   * The partitions matching `expression`, sent exactly as written: the
   * service parses it, and its InvalidInputException is the caller's to show.
   */
  listPartitions: (databaseName: string, tableName: string, expression: string) =>
    collectPages(
      paginateGetPartitions(
        { client: awsClients.glue() },
        { DatabaseName: databaseName, TableName: tableName, Expression: expression || undefined },
      ),
      (page): Partition[] | undefined => page.Partitions,
    ),

  createPartition: async (
    databaseName: string,
    tableName: string,
    partitionInput: PartitionInput,
  ): Promise<void> => {
    await awsClients.glue().send(
      new CreatePartitionCommand({
        DatabaseName: databaseName,
        TableName: tableName,
        PartitionInput: partitionInput,
      }),
    )
  },

  /**
   * Adds partitions in batches of BatchCreatePartition's limit. A partition
   * the service refuses (one that already exists, say) comes back in the
   * returned errors rather than failing the rest.
   */
  batchCreatePartitions: async (
    databaseName: string,
    tableName: string,
    partitionInputs: PartitionInput[],
  ): Promise<PartitionError[]> => {
    const client = awsClients.glue()
    const errors: PartitionError[] = []
    for (let i = 0; i < partitionInputs.length; i += PARTITION_BATCH) {
      const res = await client.send(
        new BatchCreatePartitionCommand({
          DatabaseName: databaseName,
          TableName: tableName,
          PartitionInputList: partitionInputs.slice(i, i + PARTITION_BATCH),
        }),
      )
      errors.push(...(res.Errors ?? []))
    }
    return errors
  },
}
