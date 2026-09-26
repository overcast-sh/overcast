/**
 * The editor's query tabs: several per viewer, each with its own SQL,
 * workgroup, query context, parameter values and last execution.
 *
 * Kept in `localStorage` through `useLocalStorage`, which is a per-viewer
 * convenience and survives a failing store. Every function here is pure, so
 * the transitions are tested without a DOM, and a stored value from an older
 * shape is repaired by `restoreQueryTabs` rather than trusted.
 */

import { isRecord } from "@/lib/utils"

export const QUERY_TABS_STORAGE_KEY = "overcast:athena:query-tabs"

export const DEFAULT_CATALOG = "AwsDataCatalog"
export const DEFAULT_DATABASE = "default"
export const DEFAULT_WORKGROUP = "primary"

export interface QueryTab {
  id: string
  title: string
  sql: string
  workGroup: string
  catalog: string
  database: string
  /** Values for the SQL's `?` placeholders, in order. */
  parameters: string[]
  /** The last execution started from this tab. */
  executionId?: string
}

export interface QueryTabs {
  tabs: QueryTab[]
  activeId: string
}

let counter = 0

function newId(): string {
  counter += 1
  return `${Date.now().toString(36)}-${counter}`
}

/** The next free "Query N" title. */
function nextTitle(tabs: readonly QueryTab[]): string {
  const taken = new Set(tabs.map((t) => t.title))
  let n = tabs.length + 1
  while (taken.has(`Query ${n}`)) n++
  return `Query ${n}`
}

export function createQueryTab(tabs: readonly QueryTab[], init: Partial<QueryTab> = {}): QueryTab {
  return {
    title: init.title ?? nextTitle(tabs),
    sql: "",
    workGroup: DEFAULT_WORKGROUP,
    catalog: DEFAULT_CATALOG,
    database: DEFAULT_DATABASE,
    parameters: [],
    ...init,
    id: newId(),
  }
}

export function initialQueryTabs(): QueryTabs {
  const tab = createQueryTab([])
  return { tabs: [tab], activeId: tab.id }
}

/** Opens a new tab after the others and selects it. */
export function openQueryTab(state: QueryTabs, init: Partial<QueryTab> = {}): QueryTabs {
  return addQueryTab(state, createQueryTab(state.tabs, init))
}

/** Adds a tab made with `createQueryTab` after the others, and selects it. */
export function addQueryTab(state: QueryTabs, tab: QueryTab): QueryTabs {
  return { tabs: [...state.tabs, tab], activeId: tab.id }
}

export function updateQueryTab(state: QueryTabs, id: string, patch: Partial<QueryTab>): QueryTabs {
  return { ...state, tabs: state.tabs.map((t) => (t.id === id ? { ...t, ...patch, id } : t)) }
}

export function selectQueryTab(state: QueryTabs, id: string): QueryTabs {
  return state.tabs.some((t) => t.id === id) ? { ...state, activeId: id } : state
}

/**
 * Closes a tab, selecting its neighbour when it was the selected one. The
 * last tab is never left closed: closing it leaves one empty tab.
 */
export function closeQueryTab(state: QueryTabs, id: string): QueryTabs {
  const index = state.tabs.findIndex((t) => t.id === id)
  if (index < 0) return state
  const tabs = state.tabs.filter((t) => t.id !== id)
  if (tabs.length === 0) return initialQueryTabs()
  const activeId =
    state.activeId === id ? tabs[Math.min(index, tabs.length - 1)].id : state.activeId
  return { tabs, activeId }
}

export function activeQueryTab(state: QueryTabs): QueryTab {
  return state.tabs.find((t) => t.id === state.activeId) ?? state.tabs[0]
}

function text(value: unknown, fallback: string): string {
  return typeof value === "string" ? value : fallback
}

/** A stored tab, repaired field by field; null when it is not a tab at all. */
function restoreTab(value: unknown): QueryTab | null {
  if (!isRecord(value) || typeof value.id !== "string") return null
  return {
    id: value.id,
    title: text(value.title, "Query"),
    sql: text(value.sql, ""),
    workGroup: text(value.workGroup, DEFAULT_WORKGROUP),
    catalog: text(value.catalog, DEFAULT_CATALOG),
    database: text(value.database, DEFAULT_DATABASE),
    parameters: Array.isArray(value.parameters)
      ? value.parameters.filter((p): p is string => typeof p === "string")
      : [],
    executionId: typeof value.executionId === "string" ? value.executionId : undefined,
  }
}

/** Whatever `localStorage` held, as valid tabs: at least one, with a selected one. */
export function restoreQueryTabs(value: unknown): QueryTabs {
  if (!isRecord(value) || !Array.isArray(value.tabs)) return initialQueryTabs()
  const tabs: QueryTab[] = []
  for (const tab of value.tabs.map(restoreTab)) {
    // A tab whose id repeats an earlier one's is dropped: ids key edits and React.
    if (tab && !tabs.some((t) => t.id === tab.id)) tabs.push(tab)
  }
  if (tabs.length === 0) return initialQueryTabs()
  const activeId = tabs.some((t) => t.id === value.activeId) ? String(value.activeId) : tabs[0].id
  return { tabs, activeId }
}
