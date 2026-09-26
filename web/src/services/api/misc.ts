import { apiFetch, API_BASE, endpointHeaders, endpointResolver } from "./base"
import type {
  MetricsSnapshot,
  HealthResponse,
  TopologyResponse,
  CapturedMessage,
  DebugMetricsResponse,
  TraceEntry,
  TraceEvent,
  TraceListResponse,
  TraceCountResponse,
  TraceListParams,
  TraceSearchParams,
  TraceSearchResponse,
} from "@/types"

export const metrics = {
  get: () => apiFetch<MetricsSnapshot>("/metrics"),
}

export const health = {
  check: () => apiFetch<HealthResponse>("/health"),
}

/**
 * Storage diagnostics + server-computed advisories behind the Metrics &
 * Health page (GET /_overcast/debug/metrics, proxied at /api/debug/metrics). Only
 * available when the emulator has OVERCAST_DEBUG=true — a disabled debug
 * namespace responds 404 with `{"error":"DebugDisabled", ...}`, which
 * apiFetch turns into a rejected promise. debugMetricsQueryOptions in
 * web/src/features/metrics/data.ts catches that and resolves it as an
 * expected "unavailable" result rather than letting the query fail.
 */
export const debugMetrics = {
  get: () => apiFetch<DebugMetricsResponse>("/debug/metrics"),
}

export const topology = {
  get: (region?: string) =>
    apiFetch<TopologyResponse>(
      region ? `/topology?region=${encodeURIComponent(region)}` : "/topology",
    ),
}

export const inbox = {
  list: (limit?: number) =>
    apiFetch<CapturedMessage[]>(`/inbox/messages${limit ? `?limit=${limit}` : ""}`),
  get: (id: string) => apiFetch<CapturedMessage>(`/inbox/messages/${encodeURIComponent(id)}`),
  clear: () => apiFetch<void>("/inbox/messages", { method: "DELETE" }),
  delete: (id: string) =>
    apiFetch<void>(`/inbox/messages/${encodeURIComponent(id)}`, { method: "DELETE" }),
}

export type DebugStateSummary = Record<string, string[]>
export type DebugNamespaceValues = Record<string, string>

/** Paginated response shape of GET /_overcast/debug/state/{namespace}. */
export type DebugNamespacePage = {
  values: DebugNamespaceValues
  /** Exclusive cursor for the next page; absent/empty on the last page. */
  nextKey?: string
}

export const debugState = {
  list: () => apiFetch<DebugStateSummary>("/debug/state"),
  /**
   * Fetches a single page of a namespace's raw state (storage-plan.md item
   * 3.13, frontend half). `after` is the exclusive cursor from a previous
   * page's `nextKey` (omit/empty for the first page). Callers page
   * incrementally via `useInfiniteQuery` (see `debugNamespaceInfiniteQueryOptions`
   * in `web/src/features/debug/data.ts`) rather than merging every page
   * eagerly — the whole point of the paginated contract is that a caller
   * only fetches as many pages as it actually renders.
   */
  namespacePage: (
    namespace: string,
    after?: string,
    limit?: number,
  ): Promise<DebugNamespacePage> => {
    const params = new URLSearchParams()
    if (after) params.set("after", after)
    if (limit) params.set("limit", String(limit))
    const query = params.toString()
    return apiFetch<DebugNamespacePage>(
      `/debug/state/${encodeURIComponent(namespace)}${query ? `?${query}` : ""}`,
    )
  },
  /**
   * Fetches a single key's raw value via `GET /_overcast/debug/state/{namespace}?key=`,
   * bypassing pagination entirely. Used as a lazy fallback when a deep-linked
   * key hasn't appeared in any loaded page yet (see `debug-page.tsx`).
   *
   * The endpoint returns the raw stored value as the response body — it is
   * not JSON-enveloped like other debug endpoints (it may not even be JSON:
   * plain strings are returned as `text/plain`) — so this bypasses `apiFetch`
   * and reads the body as text directly. Returns `null` on a 404 (key not
   * found) rather than throwing, since "not found" is an expected,
   * handleable outcome here, not an error condition. (`null`, not
   * `undefined`: this feeds a TanStack Query `queryFn`, which forbids
   * resolving `undefined` — see @tanstack/query/no-void-query-fn.)
   */
  value: async (namespace: string, key: string): Promise<string | null> => {
    const endpoint = endpointResolver.get()
    const res = await fetch(
      `${API_BASE}/debug/state/${encodeURIComponent(namespace)}?key=${encodeURIComponent(key)}`,
      { headers: endpointHeaders(endpoint) },
    )
    if (res.status === 404) return null
    if (!res.ok) {
      const body = (await res.json().catch(() => ({ error: res.statusText }))) as {
        error?: string
        message?: string
      }
      throw new Error(body.message ?? body.error ?? `HTTP ${res.status}`)
    }
    return res.text()
  },
}

/**
 * Query string for `GET /_overcast/debug/traces`, including the leading `?` (empty when
 * there is nothing to send).
 *
 * `method` and `status` are **repeated** params rather than one comma-joined
 * value — `?status=4xx&status=5xx` — because the server treats each occurrence
 * as an alternative and matches an entry against any of them. Hence `append`,
 * not `set`: `set` would keep only the last value and silently narrow the
 * filter to it.
 */
export function traceListQuery(params?: TraceListParams): string {
  const q = new URLSearchParams()
  if (params?.service) q.set("service", params.service)
  for (const method of params?.method ?? []) q.append("method", method)
  if (params?.path) q.set("path", params.path)
  for (const status of params?.status ?? []) q.append("status", status)
  if (params?.search) q.set("search", params.search)
  if (params?.after) q.set("after", params.after)
  if (params?.before) q.set("before", params.before)
  if (params?.hopsFor) q.set("hopsFor", params.hopsFor)
  if (params?.limit) q.set("limit", String(params.limit))
  const query = q.toString()
  return query ? `?${query}` : ""
}

export const debugTrace = {
  get: (requestId: string) => apiFetch<TraceEntry>(`/debug/trace/${encodeURIComponent(requestId)}`),

  list: (params?: TraceListParams): Promise<TraceListResponse> =>
    apiFetch<TraceListResponse>(`/debug/traces${traceListQuery(params)}`),

  events: (requestId: string) =>
    apiFetch<TraceEvent[]>(`/debug/trace/${encodeURIComponent(requestId)}/events`),

  count: () => apiFetch<TraceCountResponse>("/debug/traces/count"),

  /**
   * Scans retained traces for a string in their bodies, hop errors and log
   * entries — what `list`'s own `search` deliberately does not reach.
   *
   * One call scans a budget's worth and returns a cursor; the caller keeps
   * asking until `done`. The signal is not optional in spirit: a scan runs on
   * the emulator for as long as its budget allows, so abandoning one without
   * aborting the fetch leaves it running for a result nobody will read. React
   * Query hands `queryFn` a signal for exactly this.
   */
  search: (params: TraceSearchParams, signal?: AbortSignal): Promise<TraceSearchResponse> => {
    const query = new URLSearchParams({ q: params.q })
    if (params.cursor) query.set("cursor", params.cursor)
    if (params.internal) query.set("internal", "true")
    return apiFetch<TraceSearchResponse>(`/debug/traces/search?${query.toString()}`, { signal })
  },
}
