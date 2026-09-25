/**
 * Athena query keys.
 *
 * Key factory:
 *   athenaKeys.all()           -> [...endpoint, "athena"]
 *   athenaKeys.workGroups()    -> [...endpoint, "athena", "workgroups"]
 *   athenaKeys.namedQueries()  -> [...endpoint, "athena", "named-queries"]
 *   athenaKeys.executions()    -> [...endpoint, "athena", "executions"]
 *
 * Every query execution query — the history list, one execution, its
 * results — hangs under `executions()`, which is what an
 * `athena:QueryStateChanged` event invalidates.
 */

import { endpointStore } from "@/services/endpoint-store"

export const athenaKeys = {
  all: () => [...endpointStore.getKeys(), "athena"] as const,
  workGroups: () => [...athenaKeys.all(), "workgroups"] as const,
  namedQueries: () => [...athenaKeys.all(), "named-queries"] as const,
  executions: () => [...athenaKeys.all(), "executions"] as const,
}
