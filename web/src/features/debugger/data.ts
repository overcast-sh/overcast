/**
 * Compute debugger query definitions (docs/plans/compute-debugger.md § 7).
 *
 * Key factory:
 *   debuggerKeys.all()                          -> [...endpoint, "debugger"]
 *   debuggerKeys.targets()                      -> [...endpoint, "debugger", "targets"]
 *   debuggerKeys.target(service, resource, c)   -> [...endpoint, "debugger", "targets", service, resource, c]
 *
 * Both queries poll every two seconds. react-query only runs an interval
 * while a component observes the query, so the polling is "while the Debug
 * tab or a badge is mounted, off otherwise" without any surface having to
 * start or stop it. SSE is deliberately not used for v1.
 */

import { queryOptions } from "@tanstack/react-query"
import { debuggerTargets } from "@/services/api"
import { endpointStore } from "@/services/endpoint-store"
import type { DebuggerTarget } from "@/types"

export const DEBUGGER_POLL_MS = 2_000

export const debuggerKeys = {
  all: () => [...endpointStore.getKeys(), "debugger"] as const,
  targets: () => [...debuggerKeys.all(), "targets"] as const,
  target: (service: string, resource: string, container = "") =>
    [...debuggerKeys.targets(), service, resource, container] as const,
}

export function debuggerTargetsQueryOptions() {
  return queryOptions({
    queryKey: debuggerKeys.targets(),
    queryFn: () => debuggerTargets.list(),
    refetchInterval: DEBUGGER_POLL_MS,
  })
}

/** Resolves to `null` for a resource the server has no entry for — see `debuggerTargets.get`. */
export function debuggerTargetQueryOptions(service: string, resource: string, container = "") {
  return queryOptions({
    queryKey: debuggerKeys.target(service, resource, container),
    queryFn: () => debuggerTargets.get(service, resource, container || undefined),
    refetchInterval: DEBUGGER_POLL_MS,
    enabled: Boolean(service && resource),
  })
}

/**
 * The list keyed by resource for one service, so a list page can look up its
 * rows against one cached query rather than asking once per row. An ECS task
 * has one entry per container; the first by id wins here, which is the same
 * choice the server makes for `GET …/{service}/{resource}` without a
 * `?container=`.
 */
export function indexTargetsByResource(
  targets: readonly DebuggerTarget[] | undefined,
  service: string,
): Map<string, DebuggerTarget> {
  const index = new Map<string, DebuggerTarget>()
  for (const target of targets ?? []) {
    if (target.service !== service || index.has(target.resource)) continue
    index.set(target.resource, target)
  }
  return index
}
