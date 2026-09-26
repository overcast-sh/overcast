import { useEffect, useEffectEvent, useRef } from "react"
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
  addQueryTab,
  closeQueryTab,
  createQueryTab,
  initialQueryTabs,
  openQueryTab,
  queryContext,
  QUERY_TABS_STORAGE_KEY,
  restoreQueryTabs,
  selectQueryTab,
  updateQueryTab,
  type QueryTab,
  type QueryTabs,
} from "../../query-tabs"
import type { CompletionContext } from "../../sql-completion"
import { DataBrowser } from "./data-browser"
import { EngineStatusChip } from "./engine-status"
import { QueryPane } from "./query-pane"
import { QueryTabStrip } from "./query-tab-strip"
import type { SqlEditorHandle } from "./sql-editor"
import { disposeSqlModel } from "./sql-model"

/** SQL another page asked the editor to open (`athenaEditorLink`). */
export interface EditorLink {
  catalog?: string
  database?: string
  workGroup?: string
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
  const stored = activeQueryTab(state)
  const editorRef = useRef<SqlEditorHandle | null>(null)

  // ─── A deep link, opened once ───────────────────────────────────────────
  // Keyed by value: the route hands a new object each render, and React runs
  // this effect twice in development, and neither may open a second tab.
  const linkKey = link
    ? JSON.stringify([link.catalog, link.database, link.workGroup, link.sql, link.executionId])
    : null
  const opened = useRef<string | null>(null)
  const openLink = useEffectEvent((opening: EditorLink) => {
    setState((current) => {
      const from = activeQueryTab(current)
      return openQueryTab(current, {
        sql: opening.sql,
        catalog: opening.catalog ?? from.catalog,
        database: opening.database ?? from.database,
        workGroup: opening.workGroup ?? from.workGroup,
        executionId: opening.executionId,
      })
    })
    onLinkOpened()
  })
  useEffect(() => {
    if (!link || linkKey === null) {
      // The same link may come back (Back, then Forward): it opens again then.
      opened.current = null
      return
    }
    if (opened.current === linkKey) return
    opened.current = linkKey
    openLink(link)
  }, [link, linkKey])

  // ─── The loaded catalog, for the rail and for completion ────────────────
  const engine = useQuery(engineStatusQueryOptions()).data
  const catalogs = useQuery(dataCatalogsQueryOptions()).data
  const databases = useQuery(databasesQueryOptions(stored.catalog))
  const databaseNames = databases.data?.map((d) => d.Name ?? "") ?? []
  // A tab's database may not exist — not yet, not any more, or not in a
  // catalog just picked: the catalog's first one stands in until the reader
  // picks another.
  const tab =
    databaseNames.length > 0 && !databaseNames.includes(stored.database)
      ? { ...stored, database: databaseNames[0] }
      : stored
  const tables = useQuery(tablesQueryOptions(tab.catalog, tab.database)).data
  const completion: CompletionContext = {
    catalogs: catalogs?.map((c) => c.CatalogName ?? "") ?? [],
    databases: databaseNames,
    database: tab.database,
    tables: tables ?? [],
  }

  // ─── Preview and Show DDL: a new tab, run at once ───────────────────────
  // The tab opens first, so its SQL survives a refused start; the run then
  // lands on that tab by id, whatever the reader did meanwhile.
  const start = useResourceMutation({
    options: startQueryMutationOptions(),
    invalidateKeys: [athenaKeys.executionList()],
    errorTitle: "Could not run the query",
  })
  const runInNewTab = (sql: string, title: string) => {
    const draft = createQueryTab(state.tabs, { ...queryContext(tab), title, sql })
    setState((current) => addQueryTab(current, draft))
    void start.mutateAsync(startQueryInput(draft)).then(
      (executionId) => update(draft.id, { executionId }),
      () => undefined, // Reported by the mutation's toast; the tab keeps the SQL.
    )
  }

  const update = (id: string, patch: Partial<QueryTab>) =>
    setState((current) => updateQueryTab(current, id, patch))

  return (
    <div className="grid min-h-0 flex-1 grid-cols-[13rem_minmax(0,1fr)] gap-4 xl:grid-cols-[17rem_minmax(0,1fr)]">
      <DataBrowser
        catalog={tab.catalog}
        database={tab.database}
        databasesError={databases.error}
        onContextChange={(context) => update(tab.id, context)}
        onInsert={(text) => editorRef.current?.insert(text)}
        onRunInNewTab={runInNewTab}
      />
      <div className="flex min-h-0 min-w-0 flex-col gap-2">
        <div className="flex items-end gap-3 border-b border-border">
          <QueryTabStrip
            tabs={state.tabs}
            activeId={tab.id}
            onSelect={(id) => setState((current) => selectQueryTab(current, id))}
            onRename={(id, title) => update(id, { title })}
            onClose={(id) => {
              setState((current) => closeQueryTab(current, id))
              disposeSqlModel(id)
            }}
            onNew={() => setState((current) => openQueryTab(current, queryContext(tab)))}
          />
          {engine && (
            <div className="mb-1.5 shrink-0">
              <EngineStatusChip status={engine} />
            </div>
          )}
        </div>
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
