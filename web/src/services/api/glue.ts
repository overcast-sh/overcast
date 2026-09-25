import {
  paginateGetDatabases,
  paginateGetTables,
  type Database,
  type Table,
} from "@aws-sdk/client-glue"
import { awsClients } from "../aws-clients"
import { collectPages } from "./paginate"

export const glue = {
  listDatabases: (): Promise<Database[]> =>
    collectPages(
      paginateGetDatabases({ client: awsClients.glue() }, {}),
      (page) => page.DatabaseList,
    ),

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
}
