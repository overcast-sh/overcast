import {
  BatchGetNamedQueryCommand,
  paginateListNamedQueries,
  paginateListWorkGroups,
  type NamedQuery,
  type WorkGroupSummary,
} from "@aws-sdk/client-athena"
import { awsClients } from "../aws-clients"
import { collectPages } from "./paginate"

/** BatchGetNamedQuery's limit on ids per call. */
const NAMED_QUERY_BATCH = 50

export const athena = {
  listWorkGroups: (): Promise<WorkGroupSummary[]> =>
    collectPages(
      paginateListWorkGroups({ client: awsClients.athena() }, {}),
      (page) => page.WorkGroups,
    ),

  /** The saved queries of one workgroup, in full. */
  listNamedQueries: async (workGroup: string): Promise<NamedQuery[]> => {
    const client = awsClients.athena()
    const ids = await collectPages(
      paginateListNamedQueries({ client }, { WorkGroup: workGroup }),
      (page) => page.NamedQueryIds,
    )
    const batches: string[][] = []
    for (let i = 0; i < ids.length; i += NAMED_QUERY_BATCH) {
      batches.push(ids.slice(i, i + NAMED_QUERY_BATCH))
    }
    const pages = await Promise.all(
      batches.map((NamedQueryIds) => client.send(new BatchGetNamedQueryCommand({ NamedQueryIds }))),
    )
    return pages.flatMap((page) => page.NamedQueries ?? [])
  },

  /** The saved queries of every workgroup. */
  listAllNamedQueries: async (): Promise<NamedQuery[]> => {
    const workGroups = await athena.listWorkGroups()
    const perWorkGroup = await Promise.all(
      workGroups.map((wg) => athena.listNamedQueries(wg.Name ?? "")),
    )
    return perWorkGroup.flat()
  },
}
