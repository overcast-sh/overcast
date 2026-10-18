import { useQuery } from "@tanstack/react-query"
import type { Datapoint } from "@aws-sdk/client-cloudwatch"
import { cloudwatchMetricStatisticsQueryOptions } from "@/features/cloudwatch/metrics/data"

/** The CloudWatch namespace the data plane writes request metrics to. */
export const TRAFFIC_NAMESPACE = "AWS/Lambda/Web"
/** The window the Traffic sparkline covers, in minutes — one bar per minute. */
export const TRAFFIC_MINUTES = 60
/** How often the sparkline is re-read (ms): metrics are flushed in batches, so no faster. */
const TRAFFIC_POLL_MS = 30_000

/**
 * `datapoints` summed into `minutes` one-minute bins ending at `now`, oldest
 * first. A minute without a datapoint is 0: the data plane writes the 4XX/5XX
 * metrics only when there was one, and the request count only when there was
 * traffic.
 */
export function binPerMinute(
  datapoints: readonly Datapoint[],
  now: number,
  minutes = TRAFFIC_MINUTES,
): number[] {
  const bins = new Array<number>(minutes).fill(0)
  const start = now - minutes * 60_000
  for (const d of datapoints) {
    const at = d.Timestamp?.getTime()
    if (at === undefined || at < start || at > now) continue
    const i = Math.min(minutes - 1, Math.floor((at - start) / 60_000))
    bins[i] += d.Sum ?? 0
  }
  return bins
}

export type TrafficMetric =
  "ExecutedRequests" | "HTTPResponseStatusClass4XX" | "HTTPResponseStatusClass5XX"

/** The last hour of one of an endpoint's request metrics, summed per minute. */
export function endpointTrafficQueryOptions(
  functionName: string,
  endpointName: string,
  metricName: TrafficMetric,
) {
  return {
    ...cloudwatchMetricStatisticsQueryOptions({
      namespace: TRAFFIC_NAMESPACE,
      metricName,
      dimensions: [
        { Name: "FunctionName", Value: functionName },
        { Name: "EndpointName", Value: endpointName },
      ],
      stat: "Sum",
      period: 60,
      rangeHours: 1,
    }),
    refetchInterval: TRAFFIC_POLL_MS,
  }
}

export interface EndpointTraffic {
  /** Requests per minute, oldest first; undefined while loading or when the query failed. */
  requests?: number[]
  clientErrors?: number[]
  serverErrors?: number[]
}

export function useEndpointTraffic(
  functionName: string,
  endpointName: string,
  { errors = true }: { errors?: boolean } = {},
): EndpointTraffic {
  const requests = useQuery(
    endpointTrafficQueryOptions(functionName, endpointName, "ExecutedRequests"),
  )
  const client = useQuery({
    ...endpointTrafficQueryOptions(functionName, endpointName, "HTTPResponseStatusClass4XX"),
    enabled: errors,
  })
  const server = useQuery({
    ...endpointTrafficQueryOptions(functionName, endpointName, "HTTPResponseStatusClass5XX"),
    enabled: errors,
  })
  // Each series ends when its data was read, which keeps the render pure.
  return {
    requests: requests.data && binPerMinute(requests.data.datapoints, requests.dataUpdatedAt),
    clientErrors: client.data && binPerMinute(client.data.datapoints, client.dataUpdatedAt),
    serverErrors: server.data && binPerMinute(server.data.datapoints, server.dataUpdatedAt),
  }
}
