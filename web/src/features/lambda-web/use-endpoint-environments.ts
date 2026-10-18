import { useMemo } from "react"
import { useTopology } from "@/features/map/use-topology"

/**
 * How many execution environments (containers) each of a function's endpoints
 * has running or starting, by endpoint name — 0 is scaled to zero. Undefined
 * until the topology has answered, or when it does not list the function.
 *
 * The endpoints API does not carry this; the topology does, and the
 * `lambda-web:EnvironmentStarted` / `EnvironmentStopped` events re-read it
 * (hooks/use-event-stream.ts), so it follows traffic without polling.
 */
export function useEndpointEnvironments(
  functionName: string,
  region: string,
): ReadonlyMap<string, number> | undefined {
  const { data } = useTopology(region)
  return useMemo(() => {
    const node = data?.nodes.find((n) => n.service === "lambdaweb" && n.label === functionName)
    if (!node) return undefined
    return new Map((node.webEndpoints ?? []).map((e) => [e.name, e.environments]))
  }, [data, functionName])
}
