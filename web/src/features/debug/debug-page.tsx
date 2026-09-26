import { useEffect, useMemo, useRef, useState, type ReactNode } from "react"
import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { useVirtualizer, type VirtualItem } from "@tanstack/react-virtual"
import {
  ChevronRight,
  Copy,
  Database,
  ExternalLink,
  LinkIcon,
  RefreshCw,
  Search,
} from "lucide-react"
import { PageHeader, QueryListState, Spinner } from "@/components/ui/primitives"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
// eslint-disable-next-line local/prefer-resource-table -- the raw backing store: its paging is driven by the virtual window and the selected row is an attribute on the `<tr>`
import { Table, TableBody, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { sectionLabel } from "@/lib/typography"
import { cn, isRecord } from "@/lib/utils"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import {
  debugNamespaceInfiniteQueryOptions,
  debugStateQueryOptions,
  debugValueQueryOptions,
} from "./data"
import {
  DEBUG_SERVICE_LABELS,
  firstDebugNamespaceForService,
  groupDebugNamespaces,
  serviceForDebugNamespace,
} from "./namespaces"
import { formatCount, formatQuantity } from "@/lib/format"
import { SegmentedControl, type SegmentedOption } from "@/components/ui/segmented-control"

const MAX_HIGHLIGHTED_JSON_BYTES = 256 * 1024
const MAX_NESTED_JSON_DECODE_BYTES = 1024 * 1024

const KEY_VIEWS = [
  { value: "flat", label: "Flat" },
  { value: "tree", label: "Tree" },
] as const satisfies readonly SegmentedOption<"flat" | "tree">[]

/**
 * Row height estimate for both the virtualized flat table and the
 * virtualized key tree — both render a single line of monospace text per
 * row, so one constant covers both (see useVirtualizer calls below).
 */
const DEBUG_ROW_HEIGHT_ESTIMATE = 33

/**
 * Fires `fetchNextPage` once the virtualizer's last rendered row is within
 * reach of the end of the currently loaded row set — the standard
 * TanStack Virtual + infinite-query pattern (mirrors
 * web/src/features/s3/components/bucket-detail.tsx's rowVirtualizer effect).
 *
 * Deliberately does nothing while `enabled` is false — see debug-page.tsx's
 * callers, which disable this while a key-only search filter is active so a
 * narrow (or zero-match) filter doesn't cause it to auto-page through the
 * entire namespace hunting for matches. In that state the UI instead shows a
 * manual "Load more" control (see `LoadMoreNotice`).
 */
function useLoadMoreOnReachEnd({
  enabled,
  itemCount,
  virtualItems,
  hasNextPage,
  isFetchingNextPage,
  fetchNextPage,
}: {
  enabled: boolean
  itemCount: number
  virtualItems: VirtualItem[]
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => void
}) {
  useEffect(() => {
    if (!enabled) return
    const last = virtualItems.at(-1)
    if (!last) return
    if (last.index >= itemCount - 1 && hasNextPage && !isFetchingNextPage) {
      fetchNextPage()
    }
  }, [enabled, virtualItems, itemCount, hasNextPage, isFetchingNextPage, fetchNextPage])
}

function LoadMoreNotice({
  hasNextPage,
  isFetchingNextPage,
  fetchNextPage,
}: {
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => void
}) {
  if (!hasNextPage) return null
  return (
    <div className="flex items-center justify-between gap-2 border-b border-border bg-bg-muted/40 px-3 py-1.5 text-xs text-fg-muted">
      <span>Search only covers keys loaded so far.</span>
      <Button
        variant="ghost"
        size="sm"
        className="h-6 px-2 text-xs"
        onClick={() => fetchNextPage()}
        disabled={isFetchingNextPage}
      >
        {isFetchingNextPage ? <Spinner className="h-3 w-3" /> : "Load more"}
      </Button>
    </div>
  )
}

export function DebugPage({
  initialService,
  initialNamespace,
  initialKey,
  onNamespaceChange,
  onKeyChange,
}: {
  initialService?: string
  initialNamespace?: string
  initialKey?: string
  /**
   * Called whenever the user picks a namespace (directly, via the service
   * breadcrumb, or by clearing back to "All"). The caller (the /debug route)
   * write-throughs this to the URL with a *pushed* history entry — namespace
   * changes are a deliberate navigation step worth going "back" to.
   */
  onNamespaceChange?: (next: { service: string; namespace: string }) => void
  /**
   * Called whenever the user picks (or clears) a key within the active
   * namespace. The caller write-throughs this to the URL with a *replaced*
   * entry so clicking through many keys doesn't fill up browser history —
   * only the namespace-level navigation above does.
   */
  onKeyChange?: (key: string) => void
}) {
  const [serviceFilter, setServiceFilter] = useState(initialService ?? "")
  const [namespace, setNamespace] = useState(initialNamespace ?? "")
  const [selectedKey, setSelectedKey] = useState(initialKey ?? "")
  const [filter, setFilter] = useState("")
  const [keyView, setKeyView] = useState<"flat" | "tree">("tree")

  // Keeps the active key in sync when the URL's `key` param changes without a
  // namespace change (e.g. browser back/forward landing on a different
  // replaced history entry for the same namespace) — namespace changes are
  // already handled by the parent remounting DebugPage (see debug.tsx's
  // `key={service:namespace}` prop), so only the key needs this effect.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- responding to an external prop (URL) change, not deriving from this render's own state
    setSelectedKey(initialKey ?? "")
  }, [initialKey])

  function selectKey(key: string) {
    setSelectedKey(key)
    onKeyChange?.(key)
  }

  const summaryQuery = useQuery(debugStateQueryOptions())
  const availableNamespaces = useMemo(
    () => Object.keys(summaryQuery.data ?? {}).sort(),
    [summaryQuery.data],
  )
  const namespaces = useMemo(() => {
    const all = Object.keys(summaryQuery.data ?? {}).sort()
    if (!serviceFilter) return all
    return all.filter((ns) => serviceForDebugNamespace(ns) === serviceFilter)
  }, [serviceFilter, summaryQuery.data])
  const activeNamespace = namespace || namespaces[0] || ""
  const activeService = serviceForDebugNamespace(activeNamespace) ?? serviceFilter

  // Incremental paging (storage-plan.md item 3.13): each page is fetched
  // only as the virtualized views below actually scroll near the end of
  // what's loaded — see useLoadMoreOnReachEnd.
  const namespaceQuery = useInfiniteQuery(debugNamespaceInfiniteQueryOptions(activeNamespace))
  const loadedValues = useMemo(() => {
    const merged: Record<string, string> = {}
    for (const page of namespaceQuery.data?.pages ?? []) Object.assign(merged, page.values)
    return merged
  }, [namespaceQuery.data])
  const loadedCount = Object.keys(loadedValues).length
  // The summary endpoint (debugState.list) already enumerates every key in
  // the namespace up front (keys only, no values — see debug.go's comment on
  // why that endpoint stays unpaginated), so it's the authoritative "total"
  // count independent of how many pages this component has fetched so far.
  const totalKeys = summaryQuery.data?.[activeNamespace]?.length ?? loadedCount

  // Search is key-only and scoped to loaded rows (storage-plan.md item 3.13,
  // "make search key-only or server-side" — there is no server-side search
  // endpoint, so value-substring search over unloaded pages simply isn't
  // possible once pages are fetched incrementally rather than eagerly).
  const rows = useMemo(() => {
    const lower = filter.trim().toLowerCase()
    return Object.entries(loadedValues)
      .filter(([key]) => (lower ? key.toLowerCase().includes(lower) : true))
      .sort(([a], [b]) => a.localeCompare(b))
  }, [filter, loadedValues])
  const searchActive = filter.trim() !== ""

  const resolvedKey = resolveActiveKey(
    selectedKey,
    rows.map(([key]) => key),
  )
  // If a requested key (deep link / RawStateLink) isn't among the rows
  // loaded so far, fetch it directly via the single-key endpoint instead of
  // forcing every page to load just to find it (storage-plan.md item 3.13,
  // "lazy per-key values"). This only resolves an *exact* key match — the
  // suffix-matching resolveActiveKey does for keys already loaded (e.g. a
  // RawStateLink passing just a resource name) can't be replicated against
  // unloaded pages, since the single-key endpoint requires an exact key.
  const usingValueFallback = selectedKey !== "" && resolvedKey == null
  const valueFallbackQuery = useQuery(
    debugValueQueryOptions(activeNamespace, selectedKey, usingValueFallback),
  )
  const activeKey =
    resolvedKey ??
    (usingValueFallback && valueFallbackQuery.data != null ? selectedKey : undefined) ??
    rows[0]?.[0]
  const activeValue = resolvedKey
    ? loadedValues[resolvedKey]
    : usingValueFallback
      ? (valueFallbackQuery.data ?? undefined)
      : activeKey
        ? loadedValues[activeKey]
        : undefined

  function refreshState() {
    void Promise.all([summaryQuery.refetch(), namespaceQuery.refetch()])
  }

  return (
    <div className="flex w-full flex-col gap-4">
      <PageHeader
        title="Raw State Debugger"
        description={
          serviceFilter
            ? `Read-only raw state for ${DEBUG_SERVICE_LABELS[serviceFilter] ?? serviceFilter}. Requires OVERCAST_DEBUG=true.`
            : "Read-only view of Overcast's internal state store. Requires OVERCAST_DEBUG=true."
        }
      />

      <QueryListState
        isLoading={summaryQuery.isLoading}
        isEmpty={namespaces.length === 0}
        error={summaryQuery.error}
        emptyIcon={<Database className="h-10 w-10" />}
        emptyTitle="No debug state available"
        emptyDescription={
          serviceFilter
            ? `No raw state found for ${DEBUG_SERVICE_LABELS[serviceFilter] ?? serviceFilter}. Enable OVERCAST_DEBUG=true and create resources to inspect stored values.`
            : "Enable OVERCAST_DEBUG=true and create resources to inspect stored values."
        }
      />

      {namespaces.length > 0 && (
        <div className="flex flex-col gap-3">
          <DebugBreadcrumbs
            service={activeService}
            namespace={activeNamespace}
            stateKey={activeKey}
            onAll={() => {
              setServiceFilter("")
              setNamespace("")
              setSelectedKey("")
              onNamespaceChange?.({ service: "", namespace: "" })
            }}
            onService={() => {
              const first = activeService
                ? firstDebugNamespaceForService(activeService, availableNamespaces)
                : ""
              setServiceFilter(activeService)
              setNamespace(first)
              setSelectedKey("")
              onNamespaceChange?.({ service: activeService, namespace: first })
            }}
            onNamespace={() => selectKey("")}
          />

          <div className="grid min-h-0 gap-4 lg:grid-cols-[18rem_minmax(20rem,26rem)_1fr]">
            <section className="flex max-h-[calc(100vh-14rem)] min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-elevated">
              <div className="border-b border-border px-3 py-2 font-mono text-sm font-medium text-fg">
                Hierarchy
              </div>
              <div className="min-h-0 flex-1 overflow-auto p-2">
                {groupDebugNamespaces(namespaces).map((group) => (
                  <div key={group.service} className="mb-3 last:mb-0">
                    <div className={cn(sectionLabel, "mb-1 px-2 text-fg-muted")}>
                      {DEBUG_SERVICE_LABELS[group.service] ?? group.service}
                    </div>
                    {group.namespaces.map(({ namespace: ns, category }) => (
                      <Button
                        key={ns}
                        variant={ns === activeNamespace ? "secondary" : "ghost"}
                        className="mb-1 w-full justify-between font-mono text-xs"
                        onClick={() => {
                          setNamespace(ns)
                          setSelectedKey("")
                          onNamespaceChange?.({ service: serviceFilter, namespace: ns })
                        }}
                      >
                        <span className="truncate">{category}</span>
                        <Badge variant="outline">{summaryQuery.data?.[ns]?.length ?? 0}</Badge>
                      </Button>
                    ))}
                  </div>
                ))}
              </div>
            </section>

            <section className="flex max-h-[calc(100vh-14rem)] min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-elevated">
              <div className="shrink-0 border-b border-border p-3">
                <div className="flex items-center gap-2">
                  <Search className="h-4 w-4 text-fg-muted" />
                  <Input
                    aria-label="Filter raw state keys"
                    value={filter}
                    onChange={(e) => setFilter(e.target.value)}
                    placeholder="Search keys"
                    className="h-8"
                  />
                  <SegmentedControl
                    label="Key layout"
                    value={keyView}
                    options={KEY_VIEWS}
                    onChange={setKeyView}
                    size="md"
                  />
                </div>
                <p className="mt-2 text-xs text-fg-muted">
                  Showing {formatCount(rows.length)} of {formatQuantity(totalKeys, "record")} in{" "}
                  <span className="font-mono">{activeNamespace}</span>
                  {loadedCount < totalKeys && (
                    <span className="text-fg-subtle">
                      {" "}
                      ({loadedCount.toLocaleString()} loaded
                      {namespaceQuery.hasNextPage ? " — scroll to load more" : ""})
                    </span>
                  )}
                </p>
              </div>
              {searchActive && (
                <LoadMoreNotice
                  hasNextPage={namespaceQuery.hasNextPage}
                  isFetchingNextPage={namespaceQuery.isFetchingNextPage}
                  fetchNextPage={() => void namespaceQuery.fetchNextPage()}
                />
              )}
              {namespaceQuery.isLoading ? (
                <QueryListState isLoading isEmpty={false} />
              ) : keyView === "tree" ? (
                <KeyTree
                  rows={rows}
                  activeKey={activeKey}
                  onSelect={selectKey}
                  searchActive={searchActive}
                  hasNextPage={namespaceQuery.hasNextPage}
                  isFetchingNextPage={namespaceQuery.isFetchingNextPage}
                  fetchNextPage={() => void namespaceQuery.fetchNextPage()}
                />
              ) : (
                <FlatKeyTable
                  rows={rows}
                  activeKey={activeKey}
                  onSelect={selectKey}
                  searchActive={searchActive}
                  hasNextPage={namespaceQuery.hasNextPage}
                  isFetchingNextPage={namespaceQuery.isFetchingNextPage}
                  fetchNextPage={() => void namespaceQuery.fetchNextPage()}
                />
              )}
            </section>

            <StateValueViewer
              namespace={activeNamespace}
              stateKey={activeKey}
              value={activeValue}
              isRefreshing={summaryQuery.isFetching || namespaceQuery.isFetching}
              onRefresh={refreshState}
            />
          </div>
        </div>
      )}
    </div>
  )
}

interface KeyTreeNode {
  name: string
  path: string
  key?: string
  value?: string
  children: Map<string, KeyTreeNode>
}

interface VirtualizedListProps {
  rows: [string, string][]
  activeKey?: string
  onSelect: (key: string) => void
  searchActive: boolean
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => void
}

/**
 * Flat key/value table (keyView === "flat"). Uses the same spacer-row
 * virtualization technique as web/src/features/s3/components/bucket-detail.tsx
 * — two spacer `<tr>`s bracket the currently-rendered slice of rows inside a
 * real `<table>`, so the DOM node count stays bounded regardless of
 * namespace size while keeping the existing Table/TableRow/TableHead styling.
 *
 * ResourceTable didn't fit because the paging is driven by the virtual window
 * itself. #1327 wave D, decided rather than assumed:
 *   - `useLoadMoreOnReachEnd` needs `virtualItems` to know the reader has
 *     reached the end and fetch the next page. `ResourceTable`'s `virtualize`
 *     owns its virtualizer privately and exposes neither the items nor the
 *     scroll element, so the infinite scroll that makes a million-key namespace
 *     browsable has nothing to hang off.
 *   - The selected row is `data-[selected=true]` on the `<tr>`. `ResourceTable`
 *     renders every row identically; there is no per-row class or attribute
 *     hook, and this table is a master/detail pane where the selection *is* the
 *     state.
 *   - This is the raw backing store, not a service's resource list — the same
 *     reason CONTRIBUTING § Tables names the debug page as a standing
 *     exception. It has no service, no detail route and no row actions.
 * `virtualize` would carry the row windowing; it is the paging and the
 * selection that keep this bespoke.
 */
function FlatKeyTable({
  rows,
  activeKey,
  onSelect,
  searchActive,
  hasNextPage,
  isFetchingNextPage,
  fetchNextPage,
}: VirtualizedListProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => DEBUG_ROW_HEIGHT_ESTIMATE,
    overscan: 15,
  })
  const virtualItems = virtualizer.getVirtualItems()
  useLoadMoreOnReachEnd({
    enabled: !searchActive,
    itemCount: rows.length,
    virtualItems,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  })

  if (rows.length === 0) {
    return <div className="p-6 text-sm text-fg-muted">No records match the current filter.</div>
  }

  return (
    <div ref={scrollRef} className="min-h-0 flex-1 overflow-auto">
      <Table>
        <TableHeader>
          <TableRow className="cursor-default hover:bg-transparent">
            <TableHead>Key</TableHead>
            <TableHead className="w-16 text-right">Bytes</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {virtualItems.length > 0 && (
            <tr>
              <td colSpan={2} style={{ height: virtualItems[0].start }} />
            </tr>
          )}
          {virtualItems.map((vr) => {
            const [key, value] = rows[vr.index]
            return (
              <TableRow key={key} data-selected={key === activeKey} onClick={() => onSelect(key)}>
                <td className="max-w-0 truncate px-3 py-2 font-mono text-xs text-fg">{key}</td>
                <td className="px-3 py-2 text-right font-mono text-xs text-fg-muted">
                  {value.length.toLocaleString()}
                </td>
              </TableRow>
            )
          })}
          {virtualItems.length > 0 && (
            <tr>
              <td
                colSpan={2}
                style={{ height: virtualizer.getTotalSize() - (virtualItems.at(-1)?.end ?? 0) }}
              />
            </tr>
          )}
        </TableBody>
      </Table>
      {isFetchingNextPage && (
        <div className="flex items-center justify-center gap-2 border-t border-border py-2 text-xs text-fg-muted">
          <Spinner className="h-3.5 w-3.5" /> Loading more…
        </div>
      )}
    </div>
  )
}

interface FlatTreeRow {
  node: KeyTreeNode
  depth: number
}

/** Flattens the tree into the visible-row list a virtualizer can index — a node's children are omitted once its path is in `collapsed`. */
function flattenVisibleTree(roots: KeyTreeNode[], collapsed: Set<string>): FlatTreeRow[] {
  const out: FlatTreeRow[] = []
  const walk = (nodes: KeyTreeNode[], depth: number) => {
    for (const node of nodes) {
      out.push({ node, depth })
      if (node.children.size > 0 && !collapsed.has(node.path)) {
        walk(
          Array.from(node.children.values()).sort((a, b) => a.name.localeCompare(b.name)),
          depth + 1,
        )
      }
    }
  }
  walk(roots, 0)
  return out
}

/**
 * Key tree (keyView === "tree"). Nodes default to expanded (matching the
 * pre-virtualization behavior this replaces) with per-node collapse state;
 * collapsing a branch removes its subtree from the flattened visible-row
 * list fed to the virtualizer, so a deep/wide namespace never has to mount
 * every leaf at once even before the user collapses anything — the
 * virtualizer alone already bounds the DOM to the visible window.
 */
function KeyTree({
  rows,
  activeKey,
  onSelect,
  searchActive,
  hasNextPage,
  isFetchingNextPage,
  fetchNextPage,
}: VirtualizedListProps) {
  const roots = useMemo(() => buildKeyTree(rows), [rows])
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set())
  const flatRows = useMemo(() => flattenVisibleTree(roots, collapsed), [roots, collapsed])
  const scrollRef = useRef<HTMLDivElement>(null)
  const virtualizer = useVirtualizer({
    count: flatRows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => DEBUG_ROW_HEIGHT_ESTIMATE,
    overscan: 20,
  })
  const virtualItems = virtualizer.getVirtualItems()
  useLoadMoreOnReachEnd({
    enabled: !searchActive,
    itemCount: flatRows.length,
    virtualItems,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  })

  if (rows.length === 0) {
    return <div className="p-6 text-sm text-fg-muted">No records match the current filter.</div>
  }

  function toggleCollapsed(path: string) {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  return (
    <div ref={scrollRef} className="min-h-0 flex-1 overflow-auto p-2">
      <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
        {virtualItems.map((vr) => {
          const { node, depth } = flatRows[vr.index]
          const hasChildren = node.children.size > 0
          const isCollapsed = collapsed.has(node.path)
          const isLeaf = node.key != null
          return (
            <div
              key={vr.key}
              data-index={vr.index}
              style={{
                position: "absolute",
                top: 0,
                left: 0,
                width: "100%",
                transform: `translateY(${vr.start}px)`,
              }}
            >
              <button
                type="button"
                data-selected={isLeaf && node.key === activeKey}
                className={cn(
                  "flex w-full items-center justify-between gap-3 rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-bg-muted",
                  isLeaf ? "text-fg data-[selected=true]:bg-accent-muted" : "text-fg-muted",
                )}
                style={{ paddingLeft: `${depth * 0.75 + 0.5}rem` }}
                onClick={() => {
                  if (hasChildren) toggleCollapsed(node.path)
                  if (isLeaf && node.key) onSelect(node.key)
                }}
              >
                <span className="flex min-w-0 items-center gap-1 truncate">
                  {hasChildren ? (
                    <ChevronRight
                      className={cn(
                        "h-3 w-3 shrink-0 transition-transform",
                        !isCollapsed && "rotate-90",
                      )}
                    />
                  ) : (
                    <span className="w-3 shrink-0" />
                  )}
                  {node.name}
                </span>
                {isLeaf && node.value != null ? (
                  <span className="shrink-0 text-fg-muted">
                    {node.value.length.toLocaleString()} B
                  </span>
                ) : null}
              </button>
            </div>
          )
        })}
      </div>
      {isFetchingNextPage && (
        <div className="flex items-center justify-center gap-2 py-2 text-xs text-fg-muted">
          <Spinner className="h-3.5 w-3.5" /> Loading more…
        </div>
      )}
    </div>
  )
}

function buildKeyTree(rows: [string, string][]): KeyTreeNode[] {
  const roots = new Map<string, KeyTreeNode>()
  for (const [key, value] of rows) {
    const segments = key.split("/").filter(Boolean)
    const parts = segments.length > 0 ? segments : [key]
    let level = roots
    let path = ""
    for (const [index, part] of parts.entries()) {
      path = path ? `${path}/${part}` : part
      let node = level.get(part)
      if (!node) {
        node = { name: part, path, children: new Map() }
        level.set(part, node)
      }
      if (index === parts.length - 1) {
        node.key = key
        node.value = value
      }
      level = node.children
    }
  }
  return Array.from(roots.values()).sort((a, b) => a.name.localeCompare(b.name))
}

function DebugBreadcrumbs({
  service,
  namespace,
  stateKey,
  onAll,
  onService,
  onNamespace,
}: {
  service?: string
  namespace: string
  stateKey?: string
  onAll: () => void
  onService: () => void
  onNamespace: () => void
}) {
  return (
    <nav aria-label="Debug hierarchy" className="flex flex-wrap items-center gap-1 text-sm">
      <Button variant="ghost" size="sm" onClick={onAll}>
        Raw state
      </Button>
      {service && (
        <>
          <ChevronRight className="h-3.5 w-3.5 text-fg-subtle" />
          <Button variant="ghost" size="sm" onClick={onService}>
            {DEBUG_SERVICE_LABELS[service] ?? service}
          </Button>
        </>
      )}
      {namespace && (
        <>
          <ChevronRight className="h-3.5 w-3.5 text-fg-subtle" />
          <Button variant="ghost" size="sm" className="font-mono" onClick={onNamespace}>
            {namespace}
          </Button>
        </>
      )}
      {stateKey && (
        <>
          <ChevronRight className="h-3.5 w-3.5 text-fg-subtle" />
          <span className="max-w-96 truncate rounded-md bg-bg-muted px-2.5 py-1 font-mono text-xs text-fg">
            {stateKey}
          </span>
        </>
      )}
    </nav>
  )
}

function resolveActiveKey(requested: string, keys: string[]): string | undefined {
  if (!requested) return undefined
  return (
    keys.find((key) => key === requested) ??
    keys.find((key) => key.endsWith(`/${requested}`)) ??
    keys.find((key) => key.endsWith(`:${requested}`))
  )
}

function StateValueViewer({
  namespace,
  stateKey,
  value,
  isRefreshing,
  onRefresh,
}: {
  namespace: string
  stateKey?: string
  value?: string
  isRefreshing: boolean
  onRefresh: () => void
}) {
  // These copy controls carry a text label, so they are `Button`s driven by the
  // hook rather than the icon-only `<CopyButton>`.
  const { copy } = useCopyToClipboard()
  const parsed = parseStoredValue(value)

  return (
    <section className="flex max-h-[calc(100vh-14rem)] min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-elevated">
      <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-border px-3 py-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <Badge variant="info" className="font-mono">
            {namespace}
          </Badge>
          {stateKey ? (
            <span className="truncate font-mono text-xs text-fg-muted">{stateKey}</span>
          ) : null}
        </div>
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" onClick={onRefresh} disabled={isRefreshing}>
            <RefreshCw className={cn("h-3.5 w-3.5", isRefreshing && "animate-spin")} />
            Refresh
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => stateKey && copy(stateKey, { noun: "state key" })}
            disabled={!stateKey}
          >
            <Copy className="h-3.5 w-3.5" />
            Key
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => value != null && copy(value, { noun: "raw value" })}
            disabled={value == null}
          >
            <Copy className="h-3.5 w-3.5" />
            Value
          </Button>
          <Button
            variant="ghost"
            size="sm"
            asChild
            className={cn(!stateKey && "pointer-events-none opacity-50")}
          >
            <a
              href={stateKey ? buildDebugRawValueURL(namespace, stateKey) : undefined}
              target="_blank"
              rel="noreferrer"
              aria-disabled={!stateKey}
            >
              <ExternalLink className="h-3.5 w-3.5" />
              Open
            </a>
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() =>
              stateKey && copy(buildDebugDeepLink(namespace, stateKey), { noun: "debug link" })
            }
            disabled={!stateKey}
          >
            <LinkIcon className="h-3.5 w-3.5" />
            Link
          </Button>
        </div>
      </div>
      {stateKey ? (
        <pre tabIndex={0} className="min-h-0 flex-1 overflow-auto p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap text-fg">
          {parsed.note ? <span className="mb-3 block text-fg-muted">{parsed.note}</span> : null}
          {parsed.isJSON && parsed.highlight ? (
            <HighlightedJSON text={parsed.text} />
          ) : (
            <span className="text-fg-muted">{parsed.text}</span>
          )}
        </pre>
      ) : (
        <div className="p-8 text-sm text-fg-muted">
          Select a state key to inspect its raw value.
        </div>
      )}
    </section>
  )
}

function parseStoredValue(value: string | undefined): {
  text: string
  isJSON: boolean
  highlight: boolean
  note?: string
} {
  if (value == null || value === "") return { text: "", isJSON: false, highlight: false }
  const shouldDecodeNested = value.length <= MAX_NESTED_JSON_DECODE_BYTES
  try {
    const parsed = shouldDecodeNested ? decodeNestedJSON(JSON.parse(value)) : JSON.parse(value)
    const text = JSON.stringify(parsed, null, 2)
    if (text.length > MAX_HIGHLIGHTED_JSON_BYTES) {
      return {
        text,
        isJSON: true,
        highlight: false,
        note: `Syntax highlighting skipped for large JSON (${formatBytes(text.length)}).`,
      }
    }
    return { text, isJSON: true, highlight: true }
  } catch {
    return { text: value, isJSON: false, highlight: false }
  }
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`
}

function decodeNestedJSON(value: unknown, depth = 0): unknown {
  if (depth > 8) return value
  if (typeof value === "string") {
    const trimmed = value.trim()
    if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
      try {
        return decodeNestedJSON(JSON.parse(trimmed), depth + 1)
      } catch {
        return value
      }
    }
    return value
  }
  if (Array.isArray(value)) return value.map((item) => decodeNestedJSON(item, depth + 1))
  if (isRecord(value)) {
    return Object.fromEntries(
      Object.entries(value).map(([key, item]) => [key, decodeNestedJSON(item, depth + 1)]),
    )
  }
  return value
}

function HighlightedJSON({ text }: { text: string }) {
  return <>{highlightJSON(text)}</>
}

function highlightJSON(text: string): ReactNode[] {
  const tokenPattern =
    /("(?:\\.|[^"\\])*"(?=\s*:))|("(?:\\.|[^"\\])*")|\b(true|false)\b|\b(null)\b|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g
  const nodes: ReactNode[] = []
  let lastIndex = 0
  let tokenIndex = 0
  for (const match of text.matchAll(tokenPattern)) {
    const index = match.index
    if (index > lastIndex) nodes.push(text.slice(lastIndex, index))
    const token = match[0]
    // The shared syntax-token classes (styles/syntax-tokens.css), so this
    // hand-rolled highlighter reads the same as every Prism-rendered block
    // and flips with the theme for free.
    const className = match[1]
      ? "token property"
      : match[2]
        ? "token string"
        : match[3]
          ? "token boolean"
          : match[4]
            ? "token null"
            : "token number"
    nodes.push(
      <span key={`${index}-${tokenIndex++}`} className={className}>
        {token}
      </span>,
    )
    lastIndex = index + token.length
  }
  if (lastIndex < text.length) nodes.push(text.slice(lastIndex))
  return nodes
}

function buildDebugDeepLink(namespace: string, stateKey: string): string {
  const params = new URLSearchParams({ namespace, key: stateKey })
  return `${window.location.origin}/debug?${params.toString()}`
}

function buildDebugRawValueURL(namespace: string, stateKey: string): string {
  return `/api/debug/state/${encodeURIComponent(namespace)}?key=${encodeURIComponent(stateKey)}`
}
