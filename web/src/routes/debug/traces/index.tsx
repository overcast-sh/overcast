import { createFileRoute, Link } from "@tanstack/react-router"
import { Fragment, useMemo, useState, useCallback } from "react"
import { Search, RefreshCw, EyeOff, Terminal, GitFork } from "lucide-react"
import { useInfiniteQuery, useQuery, useQueryClient, infiniteQueryOptions } from "@tanstack/react-query"
import type { InfiniteData } from "@tanstack/react-query"
import { traceCountQueryOptions, debugTraceKeys } from "@/features/debug-traces/data"
import { describeRetention } from "@/features/debug-traces/retention"
import { debugTrace } from "@/services/api/misc"
import { endpointResolver } from "@/services/api/base"
import { nsToHuman, statusColor, statusMessage, formatTimestamp, shellQuote, traceRequestUrl, mergePolledTraces } from "@/features/debug-traces/utils"
import {
  METHOD_ITEMS,
  NO_SELECTION,
  STATUS_ITEMS,
  filterListParam,
  serviceKey,
  showsTrace,
  traceListParams,
  validateTracesSearch,
  type TracesSearch,
} from "@/features/debug-traces/filters"
import { serviceColor } from "@/features/debug-traces/service-color"
import { useDeepSearch } from "@/features/debug-traces/use-deep-search"
import { MatchBadge, MatchExcerpt } from "@/features/debug-traces/components/match-excerpt"
import { PageHeader, Spinner } from "@/components/ui/primitives"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { CheckboxFilterDropdown, type CheckboxFilterItem } from "@/components/ui/checkbox-filter-dropdown"
import { useCheckboxFilter } from "@/components/ui/use-checkbox-filter"
import { useScrollTrigger } from "@/hooks/use-scroll-trigger"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import { useDebouncedTextParam } from "@/hooks/use-debounced-text-param"
import { cn } from "@/lib/utils"
import { useDebugEnabled } from "@/hooks/use-server-info"
import type { TraceListResponse, TraceSummary } from "@/types"
import { formatQuantity } from "@/lib/format"

export const Route = createFileRoute("/debug/traces/")({
  head: () => ({ meta: [{ title: "Request Traces — Overcast" }] }),
  validateSearch: validateTracesSearch,
  component: TracesPage,
})

const COL_COUNT = 9

function curlCmd(t: TraceSummary): string {
  const { baseUrl } = endpointResolver.get()
  return `curl -X ${t.method} ${shellQuote(traceRequestUrl(baseUrl, t.path))}`
}

function TracesPage() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const debugEnabled = useDebugEnabled()
  const queryClient = useQueryClient()

  const [autoRefresh, setAutoRefresh] = useState(true)
  const { copy } = useCopyToClipboard()

  // Every filter lives in the URL, so Back from a trace's detail page restores
  // the whole filter bar with the history entry. `replace: true`: a session of
  // ticking boxes must not bury the previous page under a dozen entries.
  const setSearch = useCallback(
    (patch: Partial<TracesSearch>) => {
      void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true })
    },
    [navigate],
  )

  // Debounced so typing costs one navigation, not one per keystroke.
  const [searchInput, setSearchInput] = useDebouncedTextParam(
    search.search ?? "",
    useCallback((next: string) => setSearch({ search: next || undefined }), [setSearch]),
  )

  const params = useMemo(() => traceListParams(search, 50), [search])

  // Infinite-query: page 1 = newest, scroll down loads older via after-cursor.
  const infiniteOpts = infiniteQueryOptions({
    queryKey: [...debugTraceKeys.list(params), debugEnabled] as const,
    queryFn: ({ pageParam }) => debugTrace.list({ ...params, after: pageParam }),
    getNextPageParam: (lastPage: TraceListResponse) => lastPage.nextCursor || undefined,
    initialPageParam: undefined as string | undefined,
    enabled: debugEnabled,
  })
  const { data, isLoading, error, fetchNextPage, hasNextPage, isFetchingNextPage } = useInfiniteQuery(infiniteOpts)

  const bottomSentinelRef = useScrollTrigger({
    onTrigger: fetchNextPage,
    enabled: hasNextPage && !isFetchingNextPage,
  })

  const { data: countData } = useQuery({
    ...traceCountQueryOptions(),
  })
  const retention = useMemo(() => describeRetention(countData), [countData])

  // Live poll: fetch traces newer than the newest one in the cache (same
  // filters as the list) and fold them into the infinite query's first page.
  // Prepending advances `newestId` naturally, so each poll only fetches the
  // delta since the previous one, and an initially empty list still goes
  // live once traces start arriving.
  const newestId = data?.pages[0]?.traces[0]?.requestId
  // eslint-disable-next-line @tanstack/query/exhaustive-deps -- newestId is deliberately NOT in the key: the poll's identity must stay stable while setQueryData advances the cursor (a keyed poll would reset every second); queryClient and infiniteOpts.queryKey are stable derivations of what is already in the key.
  useQuery({
    queryKey: [...infiniteOpts.queryKey, "poll"],
    queryFn: async () => {
      const fresh = await debugTrace.list(
        newestId ? { ...params, before: newestId } : { ...params },
      )
      if (fresh.traces.length > 0) {
        queryClient.setQueryData<InfiniteData<TraceListResponse, string | undefined>>(
          infiniteOpts.queryKey,
          (old) => (old ? mergePolledTraces(old, fresh.traces) : old),
        )
      }
      return fresh
    },
    enabled: debugEnabled && autoRefresh && data !== undefined,
    refetchInterval: 1000,
  })

  const allTraces = useMemo(() => (data?.pages ?? []).flatMap((p) => p.traces), [data])

  const serviceOptions: CheckboxFilterItem[] = useMemo(() => {
    const counts = new Map<string, number>()
    for (const t of allTraces) {
      if (t.internal) continue
      const svc = serviceKey(t.service)
      counts.set(svc, (counts.get(svc) ?? 0) + 1)
    }
    return Array.from(counts.entries())
      .sort((a, b) => b[1] - a[1])
      .map(([id, count]) => ({ id, label: id.toUpperCase(), count }))
  }, [allTraces])

  // Three dropdowns, one pattern: useCheckboxFilter owns the toggle,
  // select-all/clear-all and trigger-label logic for all of them. Services is a
  // deny-list ("hide these"), status and method are include-lists ("show only
  // these") that the server applies — which is why they must not be filtered
  // here: the list is server-paginated, and dropping rows after they arrive
  // would produce sparse pages and starve the load-more sentinel.
  const serviceFilter = useCheckboxFilter({
    items: serviceOptions,
    model: "hide",
    value: search.hideServices ?? NO_SELECTION,
    onChange: useCallback((next: string[]) => setSearch({ hideServices: filterListParam(next) }), [setSearch]),
    noun: "services",
  })
  const statusFilter = useCheckboxFilter({
    items: STATUS_ITEMS,
    model: "show",
    value: search.status ?? NO_SELECTION,
    onChange: useCallback((next: string[]) => setSearch({ status: filterListParam(next) }), [setSearch]),
    noun: "statuses",
  })
  const methodFilter = useCheckboxFilter({
    items: METHOD_ITEMS,
    model: "show",
    value: search.method ?? NO_SELECTION,
    onChange: useCallback((next: string[]) => setSearch({ method: filterListParam(next) }), [setSearch]),
    noun: "methods",
  })

  const hiddenServices = serviceFilter.selected
  const hideInternal = !search.showInternal
  const rowFilter = useMemo(() => ({ hideInternal, hiddenServices }), [hideInternal, hiddenServices])

  const displayTraces = useMemo(
    () => allTraces.filter((t) => showsTrace(t, rowFilter)),
    [allTraces, rowFilter],
  )

  // The deep scan runs only when the cheap search has come up short — which is
  // exactly the case it exists for, and keeps a query the list already answered
  // from spending the budget walking gigabytes of bodies to add rows nobody
  // asked for. `searchInput` rather than the settled URL value so the scan's own
  // longer settle starts from the last keystroke, not from the list's commit.
  const deep = useDeepSearch(searchInput, searchInput.trim().length > 0 && displayTraces.length === 0)

  // A trace the list already shows must not appear again below it. The deep
  // scan reaches fields the list does not, so the same trace can legitimately
  // match both ways.
  // The same two filters apply here. They used not to: a deep match rendered
  // whatever the filter bar said, so ticking a service away or asking for
  // internal traces to be hidden left the scan's own rows behind (#1613).
  const deepMatches = useMemo(() => {
    const shown = new Set(displayTraces.map((t) => t.requestId))
    return deep.matches.filter((m) => !shown.has(m.requestId) && showsTrace(m.summary, rowFilter))
  }, [deep.matches, displayTraces, rowFilter])

  if (!debugEnabled) {
    return (
      <div className="flex flex-col gap-4 p-6">
        <PageHeader title="Request Traces" description="OVERCAST_DEBUG must be enabled on the emulator to use request tracing." />
        <div className="text-fg-muted text-sm">Set OVERCAST_DEBUG=true and restart the emulator.</div>
      </div>
    )
  }

  return (
    // @container: the path column sizes itself against the page's own width,
    // which changes when the sidebar collapses without the viewport moving.
    <div className="@container flex flex-col gap-4 p-6">
      <div className="flex items-start justify-between">
        <PageHeader
          title="Request Traces"
          description={countData ? `${countData.count} of ${countData.capacity} buffer slots used` : "Recent HTTP request traces"}
        />
        <Button
          variant={autoRefresh ? "default" : "ghost"}
          size="sm"
          onClick={() => setAutoRefresh((v) => !v)}
          className="mt-1"
        >
          <RefreshCw className={cn("h-4 w-4 mr-1", autoRefresh && "animate-spin")} />
          {autoRefresh ? "Live" : "Auto-refresh"}
        </Button>
      </div>

      <div className="flex flex-wrap items-stretch gap-2">
        <div className="relative flex-1 min-w-[200px] max-w-md">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-4 w-4 text-fg-muted" />
          {/*
            The placeholder is the only statement of what search covers, so it
            has to keep up with the server: it matches request ID, path,
            service, operation and the AWS error a request answered with. It
            does not reach hop bodies or log lines — see
            docs/plans/trace-deep-search.md — and promising those here would be
            worse than the narrower promise it makes.
          */}
          {/*
            The accessible name is deliberately separate from the placeholder.
            The placeholder is prose that changes whenever the search grows —
            it just did, and it broke the one test that reached for the box by
            reading it. A screen reader needs a name here regardless, so the
            label serves both: a stable handle for tests and the thing that
            makes an unlabelled input announce as something other than "edit
            text".
          */}
          <Input
            className="pl-8"
            aria-label="Search traces"
            placeholder="Search ID, path, service, operation, error…"
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
          />
        </div>
        <CheckboxFilterDropdown {...serviceFilter} />
        <CheckboxFilterDropdown {...statusFilter} />
        <CheckboxFilterDropdown {...methodFilter} />
        <label className="flex items-center gap-1.5 rounded-md border border-border bg-bg-elevated px-2.5 py-1.5 text-sm cursor-pointer select-none">
          <EyeOff className="h-3.5 w-3.5 text-fg-muted" />
          <span className="text-fg-muted">Hide internal</span>
          <input type="checkbox" checked={hideInternal} onChange={(e) => setSearch({ showInternal: e.target.checked ? undefined : true })} className="ml-1" />
        </label>
      </div>

      {/*
        The deep scan's progress. It only appears while one is running, because
        a status line that is always there is one nobody reads — and its whole
        job is to explain why results are still arriving after the table
        already settled.
      */}
      {deep.active && !deep.done && (
        <div className="flex items-center gap-2 text-xs text-fg-muted" role="status" aria-live="polite">
          <Spinner className="h-3 w-3" />
          <span>
            Searching bodies, hop errors and log lines for “{deep.query}”
            {deep.scanned > 0 ? ` — ${formatQuantity(deep.scanned, "trace")} scanned` : ""}
            {deep.remaining > 0 ? `, ${deep.remaining} to go` : ""}
          </span>
        </div>
      )}

      {isLoading ? (
        <Spinner />
      ) : error ? (
        <div className="text-danger text-sm">Failed to load traces.</div>
      ) : (
        <>
          {/*
            No nested horizontal scroller. A wide table used to scroll inside
            this box, which put its scrollbar at the bottom of the table
            content — the bottom of an infinite list, so reaching for it loaded
            another page and moved it further away. Letting the table overflow
            instead hands both axes to <main>, whose scrollbars sit on viewport
            edges and stay put. `w-max min-w-full` keeps the border wrapped
            around the table at its real width rather than clipping to the
            visible column.
          */}
          <div className="w-max min-w-full rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-fg-muted text-left">
                  <th scope="col" className="px-3 py-2 font-medium">Time</th>
                  <th scope="col" className="px-3 py-2 font-medium">Method</th>
                  <th scope="col" className="px-3 py-2 font-medium">Path</th>
                  <th scope="col" className="px-3 py-2 font-medium">Service</th>
                  <th scope="col" className="px-3 py-2 font-medium">Op</th>
                  <th scope="col" className="px-3 py-2 font-medium">Status</th>
                  <th scope="col" className="px-3 py-2 font-medium">Duration</th>
                  <th scope="col" className="px-3 py-2 font-medium">Request ID</th>
                  {/* A <td>, not a <th>: the chevron column has no name, and a header
                      that names nothing announces "blank" ahead of every row. */}
                  <td className="w-10 px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {displayTraces.length === 0 && deepMatches.length === 0 ? (
                  <tr>
                    <td colSpan={COL_COUNT} className="px-3 py-8 text-center text-fg-muted">
                      {/*
                        The old wording here was "No traces yet. Send a request
                        to see it here." — a confident answer that was wrong
                        whenever the thing being searched for was sitting in a
                        hop body. An empty list during a search means the search
                        found nothing, which is a different statement from the
                        buffer being empty, and while the deep scan is still
                        running it is not even that yet.
                      */}
                      {deep.active && !deep.done
                        ? "No matches in paths, IDs or operations — searching bodies and logs…"
                        : searchInput.trim()
                          ? "No matches, including in bodies, hop errors and log lines."
                          : "No traces yet. Send a request to see it here."}
                    </td>
                  </tr>
                ) : (
                  displayTraces.map((t) => (
                    <tr key={t.requestId} className="border-b border-border hover:bg-bg-elevated cursor-pointer transition-colors" onClick={() => void navigate({ to: "/debug/traces/$requestId", params: { requestId: t.requestId } })}>
                      <td className="px-3 py-2 whitespace-nowrap text-fg-muted font-mono text-xs">
                        {formatTimestamp(t.timestamp)}
                        {/*
                          Retention keeps failures past the point where their
                          neighbours are reclaimed, so an old row sitting among
                          new ones needs to say why it is still here — otherwise
                          it reads as eviction being broken.
                        */}
                        {t.pinned && (
                          <Badge
                            variant="outline"
                            className="ml-2 border-warning/40 text-warning"
                            title="Kept because this request failed; retention keeps failures past the window"
                          >
                            kept: error
                          </Badge>
                        )}
                      </td>
                      <td className="px-3 py-2"><Badge variant="outline" className="text-xs font-mono">{t.method}</Badge></td>
                      {/*
                        `truncate` cannot work on the <td> itself — a table cell
                        has no width of its own to overflow. The wrapper carries
                        the max-width, and it scales with the *page* container
                        (@container on the page root), not the viewport, because
                        the space available here depends on whether the sidebar
                        is collapsed. The title attribute keeps the full path
                        one hover away.
                      */}
                      <td className="px-3 py-2">
                        <div className="max-w-40 truncate font-mono text-xs @lg:max-w-xs @3xl:max-w-md @5xl:max-w-xl" title={t.path}>{t.path}</div>
                      </td>
                      <td className="px-3 py-2">
                        <div className="flex items-center gap-1">
                          <Badge variant="outline" className="text-xs" style={{ borderColor: serviceColor(t.service || "") }}>{t.service || "—"}</Badge>
                          {t.hopCount ? <span className="inline-flex items-center gap-0.5 text-2xs text-fg-muted" title={formatQuantity(t.hopCount, "hop")}><GitFork className="h-3 w-3" />{t.hopCount}</span> : null}
                        </div>
                      </td>
                      <td className="px-3 py-2 text-fg-muted text-xs">{t.operation ?? "—"}</td>
                      <td className={cn("px-3 py-2 font-mono text-xs whitespace-nowrap", statusColor(t.statusCode))}>{t.statusCode > 0 ? `${t.statusCode}${statusMessage(t.statusCode) ? ` ${statusMessage(t.statusCode)}` : ""}` : "—"}</td>
                      <td className="px-3 py-2 font-mono text-xs text-fg-muted">{nsToHuman(t.duration)}</td>
                      <td className="px-3 py-2 min-w-0 truncate">
                        <Link to="/debug/traces/$requestId" params={{ requestId: t.requestId }} className="font-mono text-xs text-accent hover:underline" onClick={(e) => e.stopPropagation()}>{t.requestId}</Link>
                      </td>
                      <td className="px-3 py-2">
                        <button className="inline-flex items-center justify-center h-7 w-7 rounded hover:bg-fg/5 text-fg-muted hover:text-fg shrink-0" title="Copy as curl" aria-label="Copy as curl" onClick={(e) => { e.stopPropagation(); copy(curlCmd(t), { noun: "curl command", id: t.requestId }) }}>
                          <Terminal className="h-3.5 w-3.5" />
                        </button>
                      </td>
                    </tr>
                  ))
                )}
                {/*
                  Deep matches join the same table rather than forming a second
                  list. A trace list is a chronology, and the reason anyone is
                  looking at it is to line it up against a deploy log — so a
                  match found in a body belongs in the same timeline as one
                  found in a path, distinguished by its badge and its excerpt
                  rather than by being somewhere else on the page.
                */}
                {deepMatches.map((m) => (
                  <Fragment key={`deep-${m.requestId}`}>
                    <tr
                      className="border-b border-border/50 hover:bg-bg-elevated cursor-pointer transition-colors"
                      onClick={() => void navigate({ to: "/debug/traces/$requestId", params: { requestId: m.requestId }, search: m.hopId ? { hop: m.hopId } : {} })}
                    >
                      <td className="px-3 py-2 whitespace-nowrap text-fg-muted font-mono text-xs">{formatTimestamp(m.summary.timestamp)}</td>
                      <td className="px-3 py-2"><Badge variant="outline" className="text-xs font-mono">{m.summary.method}</Badge></td>
                      <td className="px-3 py-2">
                        <div className="max-w-40 truncate font-mono text-xs @lg:max-w-xs @3xl:max-w-md @5xl:max-w-xl" title={m.summary.path}>{m.summary.path}</div>
                      </td>
                      <td className="px-3 py-2">
                        <div className="flex items-center gap-1">
                          <Badge variant="outline" className="text-xs" style={{ borderColor: serviceColor(m.summary.service || "") }}>{m.summary.service || "—"}</Badge>
                          <MatchBadge match={m} />
                        </div>
                      </td>
                      <td className="px-3 py-2 text-fg-muted text-xs">{m.summary.operation ?? "—"}</td>
                      <td className={cn("px-3 py-2 font-mono text-xs whitespace-nowrap", statusColor(m.summary.statusCode))}>{m.summary.statusCode > 0 ? m.summary.statusCode : "—"}</td>
                      <td className="px-3 py-2 font-mono text-xs text-fg-muted">{nsToHuman(m.summary.duration)}</td>
                      <td className="px-3 py-2 min-w-0 truncate">
                        <Link to="/debug/traces/$requestId" params={{ requestId: m.requestId }} className="font-mono text-xs text-accent hover:underline" onClick={(e) => e.stopPropagation()}>{m.requestId}</Link>
                      </td>
                      <td className="px-3 py-2" />
                    </tr>
                    <tr className="border-b border-border">
                      <td colSpan={COL_COUNT} className="px-3 pb-2">
                        <MatchExcerpt match={m} />
                      </td>
                    </tr>
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
          {hasNextPage && <div ref={bottomSentinelRef} className="h-1" />}
          {/*
            The end of the list, and what lies past it. Without this a list that
            stops is indistinguishable from a bug: a reader cannot tell whether
            the request they want was never traced, or was traced and reclaimed.
          */}
          {!hasNextPage && retention && (
            <div className="mt-3 rounded-md border border-border bg-bg-elevated px-3 py-2 text-xs text-fg-muted">
              <span className="font-medium text-fg">{retention.headline}</span>
              {retention.reasons.length > 0 && <> — {retention.reasons.join("; ")}</>}
              {countData?.oldestRetained && (
                <> · oldest retained {formatTimestamp(countData.oldestRetained)}</>
              )}
            </div>
          )}
        </>
      )}
    </div>
  )
}
