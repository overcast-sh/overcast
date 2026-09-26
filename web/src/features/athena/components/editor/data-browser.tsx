import { useState } from "react"
import { Link } from "@tanstack/react-router"
import { useQuery } from "@tanstack/react-query"
import { Database, LibraryBig } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { FieldLabel, QueryListState } from "@/components/ui/primitives"
import { Select } from "@/components/ui/select"
import { dataCatalogsQueryOptions, databasesQueryOptions, tablesQueryOptions } from "../../data"
import { DEFAULT_CATALOG } from "../../query-tabs"
import { TableEntry } from "./table-entry"

/**
 * The editor's left rail: the catalog and database the query runs in (its
 * `QueryExecutionContext`), and that database's tables, filterable, each
 * one click from being written into the query.
 */
export interface DataBrowserProps {
  catalog: string
  database: string
  /** Why the catalog's databases could not be listed, if they could not. */
  databasesError: Error | null
  onContextChange: (context: { catalog: string; database: string }) => void
  onInsert: (text: string) => void
  onRunInNewTab: (sql: string, title: string) => void
}

export function DataBrowser({
  catalog,
  database,
  databasesError,
  onContextChange,
  onInsert,
  onRunInNewTab,
}: DataBrowserProps) {
  const [filter, setFilter] = useState("")
  const catalogs = useQuery(dataCatalogsQueryOptions())
  const databases = useQuery(databasesQueryOptions(catalog))
  const tables = useQuery(tablesQueryOptions(catalog, database))
  const needle = filter.trim().toLowerCase()
  const shown = (tables.data ?? []).filter((t) => (t.Name ?? "").toLowerCase().includes(needle))
  const catalogNames = catalogs.data?.map((c) => c.CatalogName ?? "") ?? [catalog]
  const databaseNames = databases.data?.map((d) => d.Name ?? "") ?? [database]

  return (
    <aside aria-label="Data browser" className="flex min-h-0 flex-col gap-3">
      <label className="flex flex-col gap-1">
        <FieldLabel>Catalog</FieldLabel>
        <Select
          value={catalog}
          // The database belongs to the catalog: a new catalog starts on its first one.
          onChange={(e) => onContextChange({ catalog: e.target.value, database: "" })}
        >
          {withCurrent(catalogNames, catalog).map((name) => (
            <option key={name}>{name}</option>
          ))}
        </Select>
      </label>
      <label className="flex flex-col gap-1">
        <FieldLabel>Database</FieldLabel>
        <Select
          value={database}
          onChange={(e) => onContextChange({ catalog, database: e.target.value })}
        >
          {withCurrent(databaseNames, database).map((name) => (
            <option key={name}>{name}</option>
          ))}
        </Select>
      </label>
      <Input
        type="search"
        aria-label="Filter tables"
        placeholder="Filter tables…"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
      />
      <div className="-mx-1 min-h-0 flex-1 overflow-y-auto px-1">
        {shown.length > 0 ? (
          <ul aria-label={`Tables in ${database}`} className="flex flex-col">
            {shown.map((table) => (
              <TableEntry
                key={table.Name}
                table={table}
                database={database}
                inGlue={catalog === DEFAULT_CATALOG}
                onInsert={onInsert}
                onRunInNewTab={onRunInNewTab}
              />
            ))}
          </ul>
        ) : (
          <QueryListState
            isLoading={tables.isLoading}
            isEmpty
            error={databasesError ?? tables.error}
            loadingCount={4}
            loadingNoun="tables"
            loadingClassName="-mx-4"
            emptyClassName="py-8"
            emptyIcon={<Database className="size-8" />}
            emptyTitle="No tables"
            emptyDescription={`${database} has no tables yet.`}
            emptyAction={
              <Button asChild variant="outline" size="sm">
                <Link to="/glue">
                  <LibraryBig aria-hidden className="size-3.5" />
                  Create a table from S3 data
                </Link>
              </Button>
            }
            isFiltered={needle !== ""}
            onClearFilter={() => setFilter("")}
            filteredEmptyTitle="No matching tables"
          />
        )}
      </div>
    </aside>
  )
}

/** The listed names, with the current one kept even when the list does not have it (yet). */
function withCurrent(names: readonly string[], current: string): string[] {
  return names.includes(current) ? [...names] : [current, ...names]
}
