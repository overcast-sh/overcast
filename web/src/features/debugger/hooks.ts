import { useQuery } from "@tanstack/react-query"
import { debuggerTargetQueryOptions, debuggerTargetsQueryOptions } from "./data"

/**
 * Every debug target the emulator knows, polled while mounted. A server
 * without the endpoint answers with an error; callers treat that exactly
 * like an empty list, so a badge never becomes a red banner on a list page.
 */
export function useDebugTargets() {
  return useQuery(debuggerTargetsQueryOptions())
}

/**
 * One target, polled while mounted. `data` is `null` for a resource the
 * server has no entry for at all (a 404), which surfaces render as "off".
 */
export function useDebugTarget(service: string, resource: string, container?: string) {
  return useQuery(debuggerTargetQueryOptions(service, resource, container ?? ""))
}
