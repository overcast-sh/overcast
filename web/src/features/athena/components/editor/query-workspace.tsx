import { useEffect, useRef } from "react"
import { useQuery } from "@tanstack/react-query"
import { useLocalStorage } from "@/hooks/use-local-storage"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import {
  athenaKeys,
  dataCatalogsQueryOptions,
  databasesQueryOptions,
  engineStatusQueryOptions,
  startQueryMutationOptions,
  tablesQueryOptions,
} from "../../data"
import { startQueryInput } from "../../query-input"
import {
  activeQueryTab,
  closeQueryTab,
  createQueryTab,
  initialQueryTabs,
  openQueryTab,
  QUERY_TABS_STORAGE_KEY,
  restoreQueryTabs,
  selectQueryTab,
  updateQueryTab,
  type QueryTab,
  type QueryTabs,
} from "../../query-tabs"
import type { CompletionContext } from "../../sql-completion"
import { DataBrowser } from "./data-browser"
import { QueryPane } from "./query-pane"
import { QueryTabStrip } from "./query-tab-strip"
import type { SqlEditorHandle } from "./sql-editor"

/** SQL another page asked the editor to open (`athenaEditorLink`). */
export interface EditorLink {
  catalog?: string
  database?: string
  sql: string
  /** History's *Open result*: the tab opens on this execution's result. */
  executionId?: string
}

/**
 * The Editor tab: the data browser, the query tabs and the active tab's
 * pane. A deep link opens its SQL in a new tab with its context set and
 * does not run it; `onLinkOpened` then takes it out of the URL, so a reload
 * does not open it again.
 */
export function QueryWorkspace({
  link,
  onLinkOpened,
}: {
  link?: EditorLink
  onLinkOpened: () => void
}) {
  const [state, setState] = useLocalStorage<QueryTabs>(
    QUERY_TABS_STORAGE_KEY,
    initialQueryTabs,
    restoreQueryTabs,
  )
  const tab = activeQueryTab(state)
  const editorRef = useRef<SqlEditorHandle | null>(null)

  // ─── A deep link, opened once ───────────────────────────────────────────
  // Keyed by value: the route hands a new object each render, and React runs
  // this effect twice in development, and neither may open a second tab.
  const linkKey = link
    ? JSON.stringify([link.catalog, link.database, link.sql, link.executionId])
    : null
  const opened = useRef<string | null>(null)
  useEffect(() => {
    if (!link || linkKey === null || opened.current === linkKey) return
    opened.current = linkKey
    setState((current) => {
      const from = activeQueryTab(current)
      return openQueryTab(current, {
        sql: link.sql,
        catalog: link.catalog ?? from.catalog,
        database: link.database ?? from.database,
        workGroup: from.workGroup,
        executionId: link.executionId,
      })
    })
    onLinkOpened()
    // The link's value is what matters; `setState` and `onLinkOpened` are new each render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [linkKey])

  // ─── The loaded catalog, for the rail and for completion ────────────────
  const engine = useQuery(engineStatusQueryOptions()).data
  const catalogs = useQuery(dataCatalogsQueryOptions()).data
  const databases = useQuery(databasesQueryOptions(tab.catalog)).data
  const tables = useQuery(tablesQueryOptions(tab.catalog, tab.database)).data
  const completion: CompletionContext = {
    catalogs: catalogs?.map((c) => c.CatalogName ?? "") ?? [],
    databases: databases?.map((d) => d.Name ?? "") ?? [],
    database: tab.database,
    tables: tables ?? [],
  }

  // ─── Preview and Show DDL: a new tab, run at once ───────────────────────
  const start = useResourceMutation({
    options: startQueryMutationOptions(),
    invalidateKeys: [athenaKeys.executionList()],
    errorTitle: "Could not run the query",
  })
  const runInNewTab = (sql: string, title: string) => {
    const draft = createQueryTab(state.tabs, {
      ...tab,
      title,
      sql,
      parameters: [],
      executionId: undefined,
    })
    start.mutate(startQueryInput(draft), {
      onSuccess: (executionId) =>
        setState((current) => openQueryTab(current, { ...draft, executionId })),
    })
  }

  const update = (id: string, patch: Partial<QueryTab>) =>
    setState((current) => updateQueryTab(current, id, patch))

  return (
    <div className="grid min-h-0 flex-1 grid-cols-[15rem_minmax(0,1fr)] gap-4 xl:grid-cols-[17rem_minmax(0,1fr)]">
      <DataBrowser
        catalog={tab.catalog}
        database={tab.database}
        onContextChange={(context) => update(tab.id, context)}
        onInsert={(text) => editorRef.current?.insert(text)}
        onRunInNewTab={runInNewTab}
      />
      <div className="flex min-h-0 min-w-0 flex-col gap-2">
        <QueryTabStrip
          tabs={state.tabs}
          activeId={tab.id}
          onSelect={(id) => setState((current) => selectQueryTab(current, id))}
          onRename={(id, title) => update(id, { title })}
          onClose={(id) => setState((current) => closeQueryTab(current, id))}
          onNew={() =>
            setState((current) =>
              openQueryTab(current, {
                catalog: tab.catalog,
                database: tab.database,
                workGroup: tab.workGroup,
              }),
            )
          }
        />
        <QueryPane
          key={tab.id}
          tab={tab}
          onChange={(patch) => update(tab.id, patch)}
          completion={completion}
          engine={engine}
          editorRef={editorRef}
        />
      </div>
    </div>
  )
}
