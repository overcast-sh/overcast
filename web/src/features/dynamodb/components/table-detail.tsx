import { useState, useMemo } from "react"
import { useQuery, useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { useScrollTrigger } from "@/hooks/use-scroll-trigger"
import { useNavigate } from "@tanstack/react-router"
import {
  Database,
  Plus,
  Trash2,
  Pencil,
  RefreshCw,
  ChevronDown,
  ChevronRight,
  Search,
  Filter,
  X,
  GitBranch,
  AlertTriangle,
} from "lucide-react"
import {
  dynamoTableQueryOptions,
  dynamoItemsQueryOptions,
  dynamoQueryItemsOptions,
  dynamoMetricsQueryOptions,
  dynamoKeys,
  putItemMutationOptions,
  deleteItemMutationOptions,
  updateItemMutationOptions,
  updateStreamMutationOptions,
  bulkDeleteItemsMutationOptions,
} from "@/features/dynamodb/data"
import {
  MonitorPanel,
  type MonitorCardConfig,
} from "@/features/monitoring/components/monitor-panel"
import { DEFAULT_CHART_RANGE, type ChartRangeToken } from "@/features/monitoring/types"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Badge } from "@/components/ui/badge"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
// eslint-disable-next-line local/prefer-resource-table -- the Items table needs row selection and DynamoDB's own paging, and derives its columns per result set
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { ResourceTable } from "@/components/ui/resource-table"
import { PageHeader, Spinner, EmptyState, CodeBlock } from "@/components/ui/primitives"
import { ApplicationOwnershipBanner } from "@/components/application-ownership-banner"
import { RawStateLink } from "@/features/debug/raw-state-link"
import { useToast } from "@/components/ui/toast"
import { ItemEditorDialog } from "./item-editor"
import { FilterBuilder, matchesFilters, type FilterCondition } from "./filter-builder"
import type { DynamoItem, DynamoAttrValue, DynamoGSI, DynamoTable, DynamoKeySchema } from "@/types"
import { fieldLabel, sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { formatQuantity } from "@/lib/format"
import { SegmentedControl, type SegmentedOption } from "@/components/ui/segmented-control"

// ─── Helpers ───────────────────────────────────────────────────────────────

/** Renders a DynamoDB attribute value as a short, human-readable string. */
function renderAttr(attr: DynamoAttrValue): string {
  if ("S" in attr) return attr.S
  if ("N" in attr) return attr.N
  if ("BOOL" in attr) return String(attr.BOOL)
  if ("NULL" in attr) return "null"
  if ("SS" in attr) return `[${attr.SS.join(", ")}]`
  if ("NS" in attr) return `[${attr.NS.join(", ")}]`
  if ("BS" in attr) return `<binary set>`
  if ("B" in attr) return `<binary>`
  if ("L" in attr) return `List(${attr.L.length})`
  if ("M" in attr) return `{${Object.keys(attr.M).join(", ")}}`
  return JSON.stringify(attr)
}

/** Build the minimal key object from an item given the table's key schema. */
function extractKey(item: DynamoItem, table: DynamoTable): DynamoItem {
  const key: DynamoItem = {}
  for (const k of table.keySchema) {
    if (k.attributeName && item[k.attributeName]) {
      key[k.attributeName] = item[k.attributeName]
    }
  }
  return key
}

/** An option in the index selector: table itself, or a GSI/LSI. */
interface IndexOption {
  label: string
  value: string // "" = table, otherwise index name
  keySchema: DynamoKeySchema[]
  type: "table" | "gsi" | "lsi"
}

/** Build the list of index options for the selector. */
function buildIndexOptions(table: DynamoTable): IndexOption[] {
  const opts: IndexOption[] = [
    { label: `Table: ${table.tableName}`, value: "", keySchema: table.keySchema, type: "table" },
  ]
  for (const gsi of table.globalSecondaryIndexes) {
    opts.push({
      label: `GSI: ${gsi.indexName ?? ""}`,
      value: gsi.indexName ?? "",
      keySchema: gsi.keySchema,
      type: "gsi",
    })
  }
  for (const lsi of table.localSecondaryIndexes) {
    opts.push({
      label: `LSI: ${lsi.indexName ?? ""}`,
      value: lsi.indexName ?? "",
      keySchema: lsi.keySchema,
      type: "lsi",
    })
  }
  return opts
}

// ─── Sort key operators (matches DynamoDB KeyConditionExpression) ───────

type SortKeyOp = "=" | "<" | "<=" | ">" | ">=" | "begins_with" | "between"

const SORT_KEY_OPS: { value: SortKeyOp; label: string }[] = [
  { value: "=", label: "=" },
  { value: "<", label: "<" },
  { value: "<=", label: "≤" },
  { value: ">", label: ">" },
  { value: ">=", label: "≥" },
  { value: "begins_with", label: "Begins with" },
  { value: "between", label: "Between" },
]

// The AWS/DynamoDB phase 4 table-level catalogue (handler_metrics.go):
// consumed capacity only — see that file's doc comment for why
// SuccessfulRequestLatency/UserErrors/SystemErrors aren't charted per table.
const DYNAMODB_MONITOR_CARDS: MonitorCardConfig[] = [
  {
    title: "Consumed capacity",
    unit: "Count",
    series: [
      { metric: "ConsumedReadCapacityUnits", statistic: "Sum", label: "Read" },
      { metric: "ConsumedWriteCapacityUnits", statistic: "Sum", label: "Write" },
    ],
  },
]

// ─── Component ─────────────────────────────────────────────────────────────

const FILTER_MODES = [
  { value: "scan", label: "Scan" },
  { value: "query", label: "Query", icon: Filter },
] as const satisfies readonly SegmentedOption<"scan" | "query">[]

interface Props {
  tableName: string
}

export function TableDetail({ tableName }: Props) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast } = useToast()

  const [activeTab, setActiveTab] = useState<"items" | "schema" | "monitor">("items")
  const [monitorRange, setMonitorRange] = useState<ChartRangeToken>(DEFAULT_CHART_RANGE)
  const [monitorRefreshMs, setMonitorRefreshMs] = useState<number | false>(30_000)
  const [showPutItem, setShowPutItem] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<DynamoItem>()
  const [expandedRow, setExpandedRow] = useState<number>()
  const [editTarget, setEditTarget] = useState<DynamoItem>()

  // Bulk selection state
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(new Set())
  const [showBulkDeleteConfirm, setShowBulkDeleteConfirm] = useState(false)

  // Stream view type for the toggling UI (controlled via select)
  const [pendingStreamViewType, setPendingStreamViewType] = useState<string>("NEW_AND_OLD_IMAGES")

  // ── Index & filter state ──
  const [selectedIndex, setSelectedIndex] = useState("") // "" = main table
  const [filterMode, setFilterMode] = useState<"scan" | "query">("scan")
  const [filterHashVal, setFilterHashVal] = useState("")
  const [filterSortVal, setFilterSortVal] = useState("")
  const [filterSortVal2, setFilterSortVal2] = useState("") // second value for BETWEEN
  const [sortKeyOp, setSortKeyOp] = useState<SortKeyOp>("=")
  const [scanFilters, setScanFilters] = useState<FilterCondition[]>([])
  const [queryError, setQueryError] = useState<string>()

  // ── Query filter params (drives the infinite query when non-null) ──
  const [queryParams, setQueryParams] = useState<{
    indexName?: string
    keyConditionExpression: string
    expressionAttributeValues: DynamoItem
    expressionAttributeNames?: Record<string, string>
    filterExpression?: string
    limit?: number
    scanIndexForward?: boolean
  } | null>(null)

  const {
    data: table,
    isLoading: tableLoading,
    error: tableError,
  } = useQuery(dynamoTableQueryOptions(tableName))
  const metricsQuery = useQuery(
    dynamoMetricsQueryOptions(tableName, monitorRange, monitorRefreshMs),
  )

  const {
    data: scanPages,
    isLoading: itemsLoading,
    isFetching,
    refetch,
    hasNextPage: scanHasNextPage,
    fetchNextPage: scanFetchNextPage,
    isFetchingNextPage: scanIsFetchingNextPage,
  } = useInfiniteQuery(dynamoItemsQueryOptions(tableName))

  const {
    data: queryPages,
    hasNextPage: queryHasNextPage,
    fetchNextPage: queryFetchNextPage,
    isFetchingNextPage: queryIsFetchingNextPage,
    isFetching: queryIsFetching,
  } = useInfiniteQuery({
    ...dynamoQueryItemsOptions(
      tableName,
      queryParams ?? {
        keyConditionExpression: "",
        expressionAttributeValues: {},
      },
    ),
    enabled: queryParams !== null,
  })

  const scanSentinelRef = useScrollTrigger({
    onTrigger: () => void scanFetchNextPage(),
    enabled: scanHasNextPage && !scanIsFetchingNextPage,
  })

  const querySentinelRef = useScrollTrigger({
    onTrigger: () => void queryFetchNextPage(),
    enabled: queryHasNextPage && !queryIsFetchingNextPage,
  })

  const putMut = useMutation({
    ...putItemMutationOptions(tableName),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: dynamoKeys.itemList(tableName) })
      void qc.invalidateQueries({ queryKey: dynamoKeys.table(tableName) })
      setShowPutItem(false)
      toast({ title: "Item saved", variant: "success" })
    },
    onError: (err: Error) =>
      toast({ title: "Put failed", description: err.message, variant: "danger" }),
  })

  const updateMut = useMutation({
    ...updateItemMutationOptions(tableName),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: dynamoKeys.itemList(tableName) })
      void qc.invalidateQueries({ queryKey: dynamoKeys.table(tableName) })
      setEditTarget(undefined)
      toast({ title: "Item updated", variant: "success" })
    },
    onError: (err: Error) =>
      toast({ title: "Update failed", description: err.message, variant: "danger" }),
  })

  const deleteMut = useMutation({
    ...deleteItemMutationOptions(tableName),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: dynamoKeys.itemList(tableName) })
      void qc.invalidateQueries({ queryKey: dynamoKeys.table(tableName) })
      setDeleteTarget(undefined)
      toast({ title: "Item deleted" })
    },
    onError: (err: Error) =>
      toast({ title: "Delete failed", description: err.message, variant: "danger" }),
  })

  const streamMut = useMutation({
    ...updateStreamMutationOptions(tableName),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: dynamoKeys.table(tableName) })
      void qc.invalidateQueries({ queryKey: dynamoKeys.tables() })
      toast({ title: "Stream settings saved", variant: "success" })
    },
    onError: (err: Error) =>
      toast({ title: "Stream update failed", description: err.message, variant: "danger" }),
  })

  const bulkDeleteMut = useMutation({
    ...bulkDeleteItemsMutationOptions(tableName),
    onSuccess: (_, keys) => {
      void qc.invalidateQueries({ queryKey: dynamoKeys.itemList(tableName) })
      void qc.invalidateQueries({ queryKey: dynamoKeys.table(tableName) })
      setSelectedKeys(new Set())
      setShowBulkDeleteConfirm(false)
      toast({
        title: `${formatQuantity(keys.length, "item")} deleted`,
        variant: "success",
      })
    },
    onError: (err: Error) =>
      toast({ title: "Bulk delete failed", description: err.message, variant: "danger" }),
  })

  // Resolve current index key schema
  const indexOptions = table ? buildIndexOptions(table) : []
  const activeIndexOption =
    indexOptions.find((o) => o.value === selectedIndex) ?? indexOptions.at(0)
  const activeHashKey = activeIndexOption?.keySchema.find((k) => k.keyType === "HASH")
  const activeSortKey = activeIndexOption?.keySchema.find((k) => k.keyType === "RANGE")

  function handleRunQuery() {
    if (!table || !activeHashKey?.attributeName) return

    const hashAttrDef = table.attributeDefinitions.find(
      (a) => a.attributeName === activeHashKey.attributeName,
    )
    const hashType = hashAttrDef?.attributeType ?? "S"

    const exprVal: DynamoItem = {
      ":pk": hashType === "N" ? { N: filterHashVal } : { S: filterHashVal },
    }
    let keyCondExpr = `${activeHashKey.attributeName} = :pk`

    // Add sort key condition if provided
    if (activeSortKey?.attributeName && filterSortVal.trim()) {
      const sortAttrDef = table.attributeDefinitions.find(
        (a) => a.attributeName === activeSortKey.attributeName,
      )
      const sortType = sortAttrDef?.attributeType ?? "S"
      const mkVal = (v: string) => (sortType === "N" ? { N: v } : { S: v })
      exprVal[":sk"] = mkVal(filterSortVal)

      const sk = activeSortKey.attributeName
      switch (sortKeyOp) {
        case "=":
          keyCondExpr += ` AND ${sk} = :sk`
          break
        case "<":
          keyCondExpr += ` AND ${sk} < :sk`
          break
        case "<=":
          keyCondExpr += ` AND ${sk} <= :sk`
          break
        case ">":
          keyCondExpr += ` AND ${sk} > :sk`
          break
        case ">=":
          keyCondExpr += ` AND ${sk} >= :sk`
          break
        case "begins_with":
          keyCondExpr += ` AND begins_with(${sk}, :sk)`
          break
        case "between":
          exprVal[":sk2"] = mkVal(filterSortVal2)
          keyCondExpr += ` AND ${sk} BETWEEN :sk AND :sk2`
          break
      }
    }

    setQueryError(undefined)
    setQueryParams({
      keyConditionExpression: keyCondExpr,
      expressionAttributeValues: exprVal,
      indexName: selectedIndex || undefined,
    })
  }

  function clearFilter() {
    setFilterMode("scan")
    setFilterHashVal("")
    setFilterSortVal("")
    setFilterSortVal2("")
    setSortKeyOp("=")
    setScanFilters([])
    setQueryParams(null)
    setQueryError(undefined)
    setSelectedKeys(new Set())
  }

  const hashKey = table?.keySchema.find((k) => k.keyType === "HASH")
  const items = useMemo(() => scanPages?.pages.flatMap((p) => p.items) ?? [], [scanPages])
  const scanCount = scanPages?.pages.reduce((n, p) => n + p.count, 0) ?? 0

  const queryItems = useMemo(() => queryPages?.pages.flatMap((p) => p.items) ?? [], [queryPages])

  // Apply client-side scan filters
  const filteredScanItems = useMemo(() => {
    if (filterMode !== "scan" || scanFilters.length === 0) return items
    return items.filter((item) =>
      matchesFilters(item as Record<string, Record<string, unknown>>, scanFilters),
    )
  }, [items, scanFilters, filterMode])

  const displayItems =
    filterMode === "query" && queryParams !== null ? queryItems : filteredScanItems
  const keyAttrNames = new Set(
    (table?.keySchema ?? []).map((k) => k.attributeName ?? "").filter(Boolean),
  )

  // Collect all attribute names seen across ALL scan items (not filtered) for autocomplete
  const knownAttributes = useMemo(
    () => Array.from(new Set(items.flatMap((item) => Object.keys(item)))).sort(),
    [items],
  )

  if (tableLoading) {
    return (
      <div className="flex justify-center py-16">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  // `return null` used to be the whole of it: a failed DescribeTable rendered a blank
  // page — no heading, no message, nothing to say what went wrong or that anything had.
  // The error branch follows QueryListState's convention (the icon, the title, the
  // message), and the header stays so the page keeps its <h1> and its identity.
  if (!table) {
    return (
      <div className="flex w-full flex-col gap-4">
        <PageHeader title={tableName} description="DynamoDB table" />
        <EmptyState
          icon={<AlertTriangle className="h-10 w-10" />}
          title="Unable to load this table"
          description={
            tableError instanceof Error
              ? tableError.message
              : `No table named ${tableName} was found in this region.`
          }
        />
      </div>
    )
  }

  /** Stable string key for an item based on its primary key attributes. */
  const getItemKey = (item: DynamoItem) => JSON.stringify(extractKey(item, table))

  function toggleSelect(item: DynamoItem) {
    const k = getItemKey(item)
    setSelectedKeys((prev) => {
      const next = new Set(prev)
      if (next.has(k)) next.delete(k)
      else next.add(k)
      return next
    })
  }

  function toggleSelectAll(items: DynamoItem[]) {
    const allSelected =
      items.length > 0 && items.every((item) => selectedKeys.has(getItemKey(item)))
    if (allSelected) {
      setSelectedKeys(new Set())
    } else {
      setSelectedKeys(new Set(items.map(getItemKey)))
    }
  }

  /** Pre-fill the inline Query filter on the items tab */
  function applyKeyFilter(hashValue: string) {
    setFilterMode("query")
    setFilterHashVal(hashValue)
    setQueryParams(null)
    setActiveTab("items")
  }

  // Collect all attribute names seen across items for column headers
  const allAttrs = Array.from(new Set(displayItems.flatMap((item) => Object.keys(item)))).sort(
    (a, b) => {
      // Key attributes first
      const aIsKey = table.keySchema.some((k) => k.attributeName === a)
      const bIsKey = table.keySchema.some((k) => k.attributeName === b)
      if (aIsKey && !bIsKey) return -1
      if (!aIsKey && bIsKey) return 1
      return a.localeCompare(b)
    },
  )

  const requiredKeys = table.keySchema
    .map((k) => {
      const def = table.attributeDefinitions.find((a) => a.attributeName === k.attributeName)
      return {
        name: k.attributeName ?? "",
        type: (def?.attributeType ?? "S") as "S" | "N" | "B",
      }
    })
    .filter((k) => k.name)

  const tabs = [
    { id: "items" as const, label: `Items (${scanCount})` },
    { id: "schema" as const, label: "Schema" },
    { id: "monitor" as const, label: "Monitor" },
  ]

  return (
    <div className="flex w-full flex-col gap-4">
      <PageHeader
        title={tableName}
        description={`${table.tableStatus} · ${table.itemCount.toLocaleString()} items`}
        actions={
          <>
            <Button size="sm" variant="ghost" onClick={() => refetch()} disabled={isFetching}>
              <RefreshCw className={cn("mr-1.5 h-3.5 w-3.5", isFetching && "animate-spin")} />
              Refresh
            </Button>
            <RawStateLink namespace="dynamodb:tables" stateKey={tableName} />
          </>
        }
      />

      <ApplicationOwnershipBanner candidates={[table.tableArn, tableName]} />

      {/* Tabs */}
      <div className="flex gap-1 border-b border-border">
        {tabs.map((tab) => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              "px-3 py-1.5 text-sm transition-colors",
              activeTab === tab.id
                ? "border-b-2 border-accent font-medium text-fg"
                : "text-fg-muted hover:text-fg",
            )}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {/* Items tab */}
      {activeTab === "items" && (
        <>
          {/* Filter bar */}
          <div className="flex flex-col gap-2 rounded-md border border-border bg-bg-muted px-3 py-2.5">
            {/* Top row: always-visible controls */}
            <div className="flex items-center gap-3">
              {/* Index selector */}
              {indexOptions.length > 1 && (
                <div className="flex flex-col gap-1">
                  {/* A <label> next to a control is not attached to it — the pair needs
                      an htmlFor/id or an aria-label. */}
                  <label className={cn(fieldLabel, "text-fg-muted")} htmlFor="ddb-index-select">
                    Index
                  </label>
                  <select
                    id="ddb-index-select"
                    className="rounded border border-border bg-bg px-2 py-1.5 text-sm text-fg"
                    value={selectedIndex}
                    onChange={(e) => {
                      setSelectedIndex(e.target.value)
                      setFilterHashVal("")
                      setFilterSortVal("")
                      setQueryParams(null)
                      setQueryError(undefined)
                    }}
                  >
                    {indexOptions.map((opt) => (
                      <option key={opt.value} value={opt.value}>
                        {opt.label}
                      </option>
                    ))}
                  </select>
                </div>
              )}

              {/* Mode toggle */}
              <SegmentedControl
                label="Read mode"
                value={filterMode}
                options={FILTER_MODES}
                onChange={(mode) => {
                  setFilterMode(mode)
                  if (mode === "scan") {
                    setQueryParams(null)
                    setQueryError(undefined)
                  }
                }}
                size="md"
              />

              {(queryParams !== null || filterHashVal || scanFilters.length > 0) && (
                <Button size="sm" variant="ghost" onClick={clearFilter}>
                  <X className="mr-1.5 h-3.5 w-3.5" />
                  Clear
                </Button>
              )}

              <div className="ml-auto">
                <Button size="sm" onClick={() => setShowPutItem(true)}>
                  <Plus className="mr-1.5 h-3.5 w-3.5" />
                  Put Item
                </Button>
              </div>
            </div>

            {/* Second row: mode-specific controls (always occupies space) */}
            {filterMode === "query" ? (
              <div className="flex items-end gap-3">
                <div className="flex flex-col gap-1">
                  <label className={cn(fieldLabel, "text-fg-muted")}>
                    {activeHashKey?.attributeName ?? "Partition key"} =
                  </label>
                  <Input
                    value={filterHashVal}
                    onChange={(e) => setFilterHashVal(e.target.value)}
                    placeholder="value"
                    className="w-44"
                    onKeyDown={(e) => e.key === "Enter" && handleRunQuery()}
                  />
                </div>
                {activeSortKey?.attributeName && (
                  <div className="flex flex-col gap-1">
                    <label className={cn(fieldLabel, "text-fg-muted")}>
                      {activeSortKey.attributeName}
                    </label>
                    <div className="flex items-center gap-1.5">
                      <select
                        aria-label={`${activeSortKey.attributeName} comparator`}
                        className="rounded border border-border bg-bg px-2 py-1.5 text-sm text-fg"
                        value={sortKeyOp}
                        onChange={(e) => setSortKeyOp(e.target.value as SortKeyOp)}
                      >
                        {SORT_KEY_OPS.map((op) => (
                          <option key={op.value} value={op.value}>
                            {op.label}
                          </option>
                        ))}
                      </select>
                      <Input
                        value={filterSortVal}
                        onChange={(e) => setFilterSortVal(e.target.value)}
                        placeholder={sortKeyOp === "between" ? "from" : "(optional)"}
                        className="w-32"
                        onKeyDown={(e) => e.key === "Enter" && handleRunQuery()}
                      />
                      {sortKeyOp === "between" && (
                        <>
                          <span className="text-xs text-fg-muted">and</span>
                          <Input
                            value={filterSortVal2}
                            onChange={(e) => setFilterSortVal2(e.target.value)}
                            placeholder="to"
                            className="w-32"
                            onKeyDown={(e) => e.key === "Enter" && handleRunQuery()}
                          />
                        </>
                      )}
                    </div>
                  </div>
                )}
                <Button
                  size="sm"
                  onClick={handleRunQuery}
                  disabled={!filterHashVal.trim() || queryIsFetching}
                >
                  {queryIsFetching ? (
                    <Spinner className="mr-1.5" />
                  ) : (
                    <Search className="mr-1.5 h-3.5 w-3.5" />
                  )}
                  Run query
                </Button>
              </div>
            ) : (
              <FilterBuilder
                knownAttributes={knownAttributes}
                onApply={(filters) => setScanFilters(filters)}
                onClear={() => setScanFilters([])}
              />
            )}
          </div>

          {queryError && (
            <p className="rounded-md border border-danger/30 bg-danger-muted px-3 py-2 text-sm text-danger">
              {queryError}
            </p>
          )}

          {itemsLoading ? (
            <div className="flex justify-center py-16">
              <Spinner className="h-6 w-6" />
            </div>
          ) : displayItems.length === 0 ? (
            filterMode === "query" && queryParams !== null ? (
              <EmptyState title="No results" description="No items matched the query." />
            ) : filterMode === "scan" && scanFilters.length > 0 ? (
              <EmptyState title="No results" description="No items matched the filters." />
            ) : filterMode === "query" && queryParams === null ? (
              <p className="py-6 text-center text-sm text-fg-muted">
                Enter a partition key value and press Run.
              </p>
            ) : (
              <EmptyState
                icon={<Database className="h-10 w-10" />}
                title="No items"
                description="Put an item to get started."
                action={
                  <Button size="sm" onClick={() => setShowPutItem(true)}>
                    <Plus className="mr-1.5 h-3.5 w-3.5" />
                    Put Item
                  </Button>
                }
              />
            )
          ) : (
            <>
              {filterMode === "query" && queryParams !== null && (
                <p className="text-xs text-fg-muted">{queryItems.length} item(s) returned</p>
              )}
              {filterMode === "scan" && scanFilters.length > 0 && (
                <p className="text-xs text-fg-muted">
                  {displayItems.length} of {items.length} item(s) match filters
                </p>
              )}
              {/* Bulk selection action bar */}
              {selectedKeys.size > 0 && (
                <div className="flex items-center gap-3 rounded-md border border-border bg-bg-muted px-3 py-2">
                  <span className="text-sm text-fg-muted">
                    {formatQuantity(selectedKeys.size, "item")} selected
                  </span>
                  <Button size="sm" variant="danger" onClick={() => setShowBulkDeleteConfirm(true)}>
                    <Trash2 className="mr-1 h-3.5 w-3.5" />
                    Delete Selected
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setSelectedKeys(new Set())}>
                    Clear
                  </Button>
                </div>
              )}
              <ItemsTable
                items={displayItems}
                allAttrs={allAttrs}
                keyAttrNames={keyAttrNames}
                hashKeyName={hashKey?.attributeName}
                expandedRow={expandedRow}
                onToggleExpand={(i) => setExpandedRow(expandedRow === i ? undefined : i)}
                onEdit={(item) => setEditTarget(item)}
                onDelete={(item) => setDeleteTarget(extractKey(item, table))}
                onFilterByKey={applyKeyFilter}
                selectedKeys={selectedKeys}
                getItemKey={getItemKey}
                onToggleSelect={toggleSelect}
                onToggleSelectAll={() => toggleSelectAll(displayItems)}
              />
              {/* Scroll triggers for infinite loading */}
              {filterMode === "scan" && (scanIsFetchingNextPage || scanHasNextPage) && (
                <div ref={scanSentinelRef} className="flex justify-center py-2">
                  {scanIsFetchingNextPage && <Spinner />}
                </div>
              )}
              {filterMode === "query" && (queryIsFetchingNextPage || queryHasNextPage) && (
                <div ref={querySentinelRef} className="flex justify-center py-2">
                  {queryIsFetchingNextPage && <Spinner />}
                </div>
              )}
            </>
          )}
        </>
      )}

      {/* Schema tab */}
      {activeTab === "schema" && (
        <div className="flex flex-col gap-6">
          <section>
            <h3 className={cn(sectionLabel, "mb-2 text-fg-muted")}>Key Schema</h3>
            <div className="rounded-md border border-border">
              {/*
                ResourceTable didn't fit because this is not a list of
                resources: it is the one-or-two-row description of *this*
                table's primary key. There is nothing to sort, nothing to hide,
                and no empty/error state — the rows exist iff the table does.
              */}
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Attribute</TableHead>
                    <TableHead>Key type</TableHead>
                    <TableHead>Data type</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {table.keySchema.map((k) => {
                    const def = table.attributeDefinitions.find(
                      (a) => a.attributeName === k.attributeName,
                    )
                    return (
                      <TableRow key={k.attributeName}>
                        <TableCell>{k.attributeName}</TableCell>
                        <TableCell>
                          <Badge variant={k.keyType === "HASH" ? "accent" : "default"}>
                            {k.keyType}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-fg-muted">{def?.attributeType ?? "—"}</TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            </div>
          </section>

          {table.globalSecondaryIndexes.length > 0 && (
            <section>
              <h3 className={cn(sectionLabel, "mb-2 text-fg-muted")}>Global Secondary Indexes</h3>
              <div className="rounded-md border border-border">
                <IndexTable
                  indexes={table.globalSecondaryIndexes}
                  noun="global secondary indexes"
                />
              </div>
            </section>
          )}

          {table.localSecondaryIndexes.length > 0 && (
            <section>
              <h3 className={cn(sectionLabel, "mb-2 text-fg-muted")}>Local Secondary Indexes</h3>
              <div className="rounded-md border border-border">
                <IndexTable indexes={table.localSecondaryIndexes} noun="local secondary indexes" />
              </div>
            </section>
          )}

          <section>
            <h3 className={cn(sectionLabel, "mb-2 text-fg-muted")}>Table details</h3>
            <DefinitionList className="gap-y-2 rounded-md border border-border bg-bg-muted p-4">
              <Definition label="Status" value={table.tableStatus} />
              <Definition label="Billing mode" value={table.billingMode} />
              {/* On-demand tables have no provisioned capacity to report. */}
              <Definition
                label="Capacity"
                value={
                  table.provisionedThroughput
                    ? `${table.provisionedThroughput.readCapacityUnits.toLocaleString()} RCU / ${table.provisionedThroughput.writeCapacityUnits.toLocaleString()} WCU`
                    : "On-demand"
                }
              />
              <Definition
                label="Created"
                value={
                  table.creationDateTime ? new Date(table.creationDateTime).toLocaleString() : null
                }
              />
              <Definition label="Item count" value={table.itemCount.toLocaleString()} />
              <Definition label="Size" value={`${table.tableSizeBytes.toLocaleString()} bytes`} />
              <Definition label="ARN" value={table.tableArn} full />
            </DefinitionList>
          </section>

          {/* Streams section */}
          <section>
            <h3 className={cn(sectionLabel, "mb-2 text-fg-muted")}>DynamoDB Streams</h3>
            <div className="rounded-md border border-border bg-bg-muted p-4">
              {table.streamSpecification?.streamEnabled ? (
                <div className="flex flex-col gap-3">
                  <div className="flex items-center justify-between">
                    <div className="flex flex-col gap-0.5">
                      <span className="font-mono text-sm font-medium text-fg">Streams enabled</span>
                      <span className="text-xs text-fg-muted">
                        View type:{" "}
                        <span className="font-mono">
                          {table.streamSpecification.streamViewType ?? "—"}
                        </span>
                      </span>
                    </div>
                    <div className="flex items-center gap-2">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() =>
                          navigate({
                            to: "/pipes",
                            hash: `create?source=${encodeURIComponent(table.latestStreamArn ?? "")}`,
                          })
                        }
                      >
                        <GitBranch className="mr-1.5 h-3.5 w-3.5" />
                        Create Pipe
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={streamMut.isPending}
                        onClick={() => streamMut.mutate({ streamEnabled: false })}
                      >
                        Disable
                      </Button>
                    </div>
                  </div>
                  {table.latestStreamArn && (
                    <div className="flex flex-col gap-0.5">
                      <span className="font-mono text-xs text-fg-muted">Stream ARN</span>
                      <span className="font-mono text-xs break-all text-fg">
                        {table.latestStreamArn}
                      </span>
                    </div>
                  )}
                </div>
              ) : (
                <div className="flex items-center gap-3">
                  <select
                    className="rounded border border-border bg-bg px-2 py-1 text-sm text-fg"
                    value={pendingStreamViewType}
                    onChange={(e) => setPendingStreamViewType(e.target.value)}
                  >
                    <option value="KEYS_ONLY">KEYS_ONLY</option>
                    <option value="NEW_IMAGE">NEW_IMAGE</option>
                    <option value="OLD_IMAGE">OLD_IMAGE</option>
                    <option value="NEW_AND_OLD_IMAGES">NEW_AND_OLD_IMAGES</option>
                  </select>
                  <Button
                    size="sm"
                    disabled={streamMut.isPending}
                    onClick={() =>
                      streamMut.mutate({
                        streamEnabled: true,
                        streamViewType: pendingStreamViewType,
                      })
                    }
                  >
                    Enable stream
                  </Button>
                </div>
              )}
            </div>
          </section>
        </div>
      )}

      {/* Monitor tab */}
      {activeTab === "monitor" && (
        <MonitorPanel
          range={monitorRange}
          onRangeChange={setMonitorRange}
          refreshIntervalMs={monitorRefreshMs}
          onRefreshIntervalChange={setMonitorRefreshMs}
          isLoading={metricsQuery.isLoading}
          isFetching={metricsQuery.isFetching}
          error={metricsQuery.error}
          data={metricsQuery.data}
          cards={DYNAMODB_MONITOR_CARDS}
          onRefresh={() =>
            void qc.invalidateQueries({ queryKey: dynamoKeys.metrics(tableName, monitorRange) })
          }
        />
      )}

      {/* Put Item dialog */}
      <ItemEditorDialog
        open={showPutItem}
        onOpenChange={setShowPutItem}
        requiredKeys={requiredKeys}
        isPending={putMut.isPending}
        onSubmit={(item) => putMut.mutate(item)}
      />

      {/* Edit Item dialog */}
      <ItemEditorDialog
        open={!!editTarget}
        onOpenChange={(open) => {
          if (!open) setEditTarget(undefined)
        }}
        requiredKeys={requiredKeys}
        initialItem={editTarget}
        isPending={updateMut.isPending}
        onSubmit={(item) => {
          if (editTarget) {
            updateMut.mutate({ key: extractKey(editTarget, table), item })
          }
        }}
      />

      {/* Delete Item confirmation */}
      <Dialog open={!!deleteTarget} onOpenChange={() => setDeleteTarget(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete item</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-fg-muted">Delete the item with key:</p>
          {deleteTarget && (
            <CodeBlock className="mt-1">{JSON.stringify(deleteTarget, null, 2)}</CodeBlock>
          )}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteTarget(undefined)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() => deleteTarget && deleteMut.mutate(deleteTarget)}
              disabled={deleteMut.isPending}
            >
              {deleteMut.isPending ? <Spinner className="mr-1.5" /> : null}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Bulk delete confirmation */}
      <Dialog open={showBulkDeleteConfirm} onOpenChange={setShowBulkDeleteConfirm}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {formatQuantity(selectedKeys.size, "item")}</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-fg-muted">
            This will permanently delete {formatQuantity(selectedKeys.size, "selected item")}. This
            action cannot be undone.
          </p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setShowBulkDeleteConfirm(false)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              disabled={bulkDeleteMut.isPending}
              onClick={() => {
                const keys = displayItems
                  .filter((item) => selectedKeys.has(getItemKey(item)))
                  .map((item) => extractKey(item, table))
                bulkDeleteMut.mutate(keys)
              }}
            >
              {bulkDeleteMut.isPending ? <Spinner className="mr-1.5" /> : null}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// ─── Secondary-index table ─────────────────────────────────────────────────

/**
 * The GSI/LSI listing on the Schema tab. Both index kinds carry the same three
 * facts, so one embedded `ResourceTable` renders either; the caller supplies
 * the plural noun the empty/error copy reads with.
 */
function IndexTable({ indexes, noun }: { indexes: DynamoGSI[]; noun: string }) {
  return (
    <ResourceTable
      variant="embedded"
      query={{ data: indexes, isLoading: false }}
      noun={noun}
      rowKey={(index) => index.indexName ?? ""}
      columns={[
        {
          id: "index-name",
          header: "Index name",
          sortValue: (index) => index.indexName,
          cell: (index) => index.indexName,
        },
        {
          header: "Keys",
          cellClassName: "text-fg-muted",
          cell: (index) => index.keySchema.map((k) => k.attributeName).join(" / "),
        },
        {
          id: "items",
          header: "Items",
          headerClassName: "text-right",
          cellClassName: "text-right tabular-nums",
          sortValue: (index) => index.itemCount,
          cell: (index) => index.itemCount.toLocaleString(),
        },
      ]}
    />
  )
}

// ─── Shared items table ────────────────────────────────────────────────────

interface ItemsTableProps {
  items: DynamoItem[]
  allAttrs: string[]
  /** Set of attribute names that are part of the primary key */
  keyAttrNames: Set<string>
  /** Name of the HASH key attribute (for Query tab targeting) */
  hashKeyName?: string
  expandedRow: number | undefined
  onToggleExpand: (i: number) => void
  onEdit: (item: DynamoItem) => void
  onDelete: (item: DynamoItem) => void
  /** Called when the user clicks "Filter by this value" on a key cell */
  onFilterByKey?: (hashValue: string) => void
  /** Bulk selection */
  selectedKeys: Set<string>
  getItemKey: (item: DynamoItem) => string
  onToggleSelect: (item: DynamoItem) => void
  onToggleSelectAll: () => void
}

/*
 * ResourceTable didn't fit because this table needs two things it deliberately
 * does not have (see `resource-table.tsx` — row selection is one of the v9
 * features the shared component does not register):
 *
 *   1. Row selection — the checkbox column, the header's tri-state
 *      select-all, and the selected-row tint that drives bulk delete.
 *   2. Server-side paging — the rows arrive from Scan/Query through
 *      `useInfiniteQuery` a page at a time (`LastEvaluatedKey`), so a
 *      client-side sort over `items` would silently reorder only the pages
 *      fetched so far and present it as the table's order. Sorting here has
 *      to be DynamoDB's (`scanIndexForward` on a Query), not the table's.
 *
 * Columns are also derived per result set rather than declared. Tracked on
 * #1327 (wave D's "specials" question, extended to this table).
 */
function ItemsTable({
  items,
  allAttrs,
  keyAttrNames,
  hashKeyName,
  expandedRow,
  onToggleExpand,
  onEdit,
  onDelete,
  onFilterByKey,
  selectedKeys,
  getItemKey,
  onToggleSelect,
  onToggleSelectAll,
}: ItemsTableProps) {
  const [hoveredKey, setHoveredKey] = useState<string | null>(null) // "rowIndex:attrName"
  // Show at most 5 columns in the table; the rest appear in the expanded row
  const visibleCols = allAttrs.slice(0, 5)
  const hasMore = allAttrs.length > 5

  const allSelected = items.length > 0 && items.every((item) => selectedKeys.has(getItemKey(item)))
  const someSelected = items.some((item) => selectedKeys.has(getItemKey(item)))

  return (
    <div className="rounded-md border border-border">
      <Table>
        <TableHeader>
          <TableRow>
            {/* The header cell holds the select-all box, so it is a real header with a
                name — an empty <th> announces "blank" ahead of every row. The box gets
                its own label because a wrapping <label> with no text names nothing. */}
            <TableHead className="w-10">
              <span className="sr-only">Select</span>
              <label className="flex cursor-pointer items-center justify-center">
                <input
                  type="checkbox"
                  aria-label="Select every listed item"
                  className="h-6 w-6 rounded accent-accent"
                  checked={allSelected}
                  ref={(el) => {
                    if (el) el.indeterminate = someSelected && !allSelected
                  }}
                  onChange={onToggleSelectAll}
                />
              </label>
            </TableHead>
            {hasMore && <TableHead className="w-8" />}
            {visibleCols.map((col) => (
              <TableHead key={col} className="font-mono text-xs">
                {col}
              </TableHead>
            ))}
            {hasMore && (
              <TableHead className="text-xs text-fg-muted">+{allAttrs.length - 5} more</TableHead>
            )}
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((item, i) => (
            <>
              <TableRow
                key={i}
                className={cn(
                  hasMore && "cursor-pointer",
                  selectedKeys.has(getItemKey(item)) && "bg-accent-muted",
                )}
                onClick={() => hasMore && onToggleExpand(i)}
              >
                <TableCell className="w-10 p-0">
                  <label
                    className="flex cursor-pointer items-center justify-center p-3"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <input
                      type="checkbox"
                      aria-label={`Select item ${getItemKey(item)}`}
                      className="h-6 w-6 rounded accent-accent"
                      checked={selectedKeys.has(getItemKey(item))}
                      onChange={() => onToggleSelect(item)}
                    />
                  </label>
                </TableCell>
                {hasMore && (
                  <TableCell className="w-8 text-fg-muted">
                    {expandedRow === i ? (
                      <ChevronDown className="h-3.5 w-3.5" />
                    ) : (
                      <ChevronRight className="h-3.5 w-3.5" />
                    )}
                  </TableCell>
                )}
                {visibleCols.map((col) => (
                  <TableCell
                    key={col}
                    className="relative"
                    onMouseEnter={() =>
                      keyAttrNames.has(col) ? setHoveredKey(`${i}:${col}`) : undefined
                    }
                    onMouseLeave={() => setHoveredKey(null)}
                  >
                    {item[col] ? renderAttr(item[col]) : <span className="text-fg-subtle">—</span>}
                    {/* Filter button — only on HASH key column cells */}
                    {keyAttrNames.has(col) &&
                      col === hashKeyName &&
                      item[col] &&
                      onFilterByKey &&
                      hoveredKey === `${i}:${col}` && (
                        <button
                          title="Filter by this value"
                          className="absolute top-1/2 right-1 -translate-y-1/2 rounded p-0.5 text-accent opacity-0 transition-opacity group-has-[td:hover]:opacity-100 hover:bg-accent-muted"
                          style={{ opacity: 1 }}
                          onClick={(e) => {
                            e.stopPropagation()
                            onFilterByKey(item[col] ? renderAttr(item[col]) : "")
                          }}
                        >
                          <Filter className="h-3 w-3" />
                        </button>
                      )}
                  </TableCell>
                ))}
                {hasMore && <TableCell />}
                <TableCell className="text-right">
                  <div className="flex items-center justify-end gap-1">
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 w-7 p-0 text-fg-muted hover:text-fg"
                      title="Edit item"
                      onClick={(e) => {
                        e.stopPropagation()
                        onEdit(item)
                      }}
                    >
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-label="Delete item"
                      className="h-7 w-7 p-0 text-fg-muted hover:text-danger"
                      onClick={(e) => {
                        e.stopPropagation()
                        onDelete(item)
                      }}
                    >
                      <Trash2 aria-hidden className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
              {expandedRow === i && (
                <TableRow key={`${i}-expanded`}>
                  <TableCell colSpan={visibleCols.length + 4} className="bg-bg-muted p-0">
                    <div className="p-3">
                      <CodeBlock>{JSON.stringify(item, null, 2)}</CodeBlock>
                    </div>
                  </TableCell>
                </TableRow>
              )}
            </>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
