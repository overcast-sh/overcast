import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import type { QueryExecution } from "@aws-sdk/client-athena"
import { FileText, History, RotateCcw, SquareCode } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { CopyButton } from "@/components/ui/copy-button"
import { HighlightedCode } from "@/components/ui/highlighted-code"
import { RefreshAction, ResourceListFilter, RowAction } from "@/components/ui/resource-list-page"
import { ResourceListSection } from "@/components/ui/resource-list-section"
import { ResourceTable, type ResourceTableSort } from "@/components/ui/resource-table"
import { useNow } from "@/hooks/use-now"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { formatBytes, formatDate, formatDuration } from "@/lib/format"
import { athenaKeys, executionsQueryOptions, startQueryMutationOptions } from "../data"
import { elapsedMs, isFinished, stateBadge } from "../execution-state"
import { athenaEditorLink } from "../links"
import { QueryError } from "./editor/query-error"

/** The first line of the SQL, for the table: the whole of it is one expand away. */
function snippet(sql: string | undefined): string {
  const line = (sql ?? "").trim().split("\n")[0] ?? ""
  return line.length > 120 ? `${line.slice(0, 120)}…` : line
}

function matches(execution: QueryExecution, needle: string): boolean {
  return [
    execution.QueryExecutionId,
    execution.Query,
    execution.WorkGroup,
    execution.Status?.State,
  ].some((field) => field?.toLowerCase().includes(needle))
}

/** An execution's query, opened in a new editor tab with its context. */
function editorLinkFor(execution: QueryExecution) {
  return athenaEditorLink({
    catalog: execution.QueryExecutionContext?.Catalog,
    database: execution.QueryExecutionContext?.Database,
    workGroup: execution.WorkGroup,
    sql: execution.Query ?? "",
  })
}

export interface HistoryTabProps {
  filter: string
  onFilterChange: (value: string) => void
  sort?: ResourceTableSort
  onSortChange: (sort: ResourceTableSort | undefined) => void
  /** The execution a `?execution=` link opened on, expanded. */
  execution?: string
}

/**
 * Every workgroup's recent executions, newest first: the SQL, where it ran,
 * how long and how much it read. Expand one for its full SQL and error;
 * open it in the editor, run it again, or open its result.
 */
export function HistoryTab({
  filter,
  onFilterChange,
  sort,
  onSortChange,
  execution,
}: HistoryTabProps) {
  const { data, isLoading, isFetching, error, refetch } = useQuery(executionsQueryOptions())
  const needle = filter.trim().toLowerCase()
  const shown = needle ? (data ?? []).filter((e) => matches(e, needle)) : data
  const rerun = useResourceMutation({
    options: startQueryMutationOptions(),
    invalidateKeys: [athenaKeys.executionList()],
    successTitle: "Query started",
    successDescription: (input) => snippet(input.QueryString),
    errorTitle: "Could not run the query",
  })
  const now = useNow((data ?? []).some((e) => !isFinished(e.Status?.State)))

  return (
    <ResourceListSection
      className="pt-4"
      actions={
        <>
          <ResourceListFilter
            value={filter}
            onChange={onFilterChange}
            placeholder="Filter by SQL, workgroup, state or id…"
            className="flex-1"
          />
          <RefreshAction isFetching={isFetching} onClick={() => void refetch()} />
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: shown, isLoading, error }}
        noun="executions"
        emptyIcon={History}
        emptyTitle="No queries yet"
        emptyDescription="Queries run from the editor, the CLI or an SDK show here."
        emptyAction={
          <Link
            to="/athena"
            search={{ tab: "editor" }}
            className="text-xs text-accent hover:underline"
          >
            Open the editor
          </Link>
        }
        isFiltered={needle !== ""}
        onClearFilter={() => onFilterChange("")}
        filteredEmptyTitle="No matching queries"
        rowKey={(e) => e.QueryExecutionId ?? ""}
        sort={sort}
        onSortChange={onSortChange}
        defaultSort={{ id: "submitted", desc: true }}
        defaultExpanded={(e) => e.QueryExecutionId === execution}
        expandedContent={(e) => (
          <div className="flex flex-col gap-2 py-1">
            <p className="flex items-center gap-1 font-mono text-2xs text-fg-subtle">
              {e.QueryExecutionId}
              <CopyButton value={e.QueryExecutionId ?? ""} noun="execution id" tone="inline" />
            </p>
            <HighlightedCode
              text={e.Query ?? ""}
              language="sql"
              className="max-h-64 overflow-auto rounded-md border border-border bg-bg-muted p-2 text-xs"
            />
            {e.Status?.State === "FAILED" && (
              <QueryError
                sql={e.Query ?? ""}
                error={e.Status.AthenaError}
                message={e.Status.AthenaError?.ErrorMessage ?? e.Status.StateChangeReason ?? ""}
              />
            )}
          </div>
        )}
        columns={[
          {
            id: "state",
            header: "State",
            sortValue: (e) => e.Status?.State,
            cell: (e) => <Badge variant={stateBadge(e.Status?.State)}>{e.Status?.State}</Badge>,
          },
          {
            id: "sql",
            header: "SQL",
            // A table cell ignores max-width, so the snippet's own box truncates.
            cell: (e) => (
              <span className="block max-w-40 truncate xl:max-w-72">{snippet(e.Query)}</span>
            ),
          },
          {
            id: "workgroup",
            header: "Workgroup",
            sortValue: (e) => e.WorkGroup,
            cell: (e) => e.WorkGroup,
          },
          {
            id: "submitted",
            header: "Submitted",
            cellClassName: "whitespace-nowrap text-fg-muted",
            sortValue: (e) => e.Status?.SubmissionDateTime,
            cell: (e) => formatDate(e.Status?.SubmissionDateTime),
          },
          {
            id: "duration",
            header: "Duration",
            headerClassName: "text-right",
            cellClassName: "text-right tabular-nums",
            sortValue: (e) => elapsedMs(e, now),
            cell: (e) =>
              formatDuration(elapsedMs(e, now) ?? undefined) +
              (isFinished(e.Status?.State) ? "" : "…"),
          },
          {
            id: "scanned",
            header: "Scanned",
            headerClassName: "text-right",
            cellClassName: "text-right tabular-nums",
            sortValue: (e) => e.Statistics?.DataScannedInBytes,
            cell: (e) => formatBytes(e.Statistics?.DataScannedInBytes ?? 0),
          },
          {
            id: "type",
            header: "Type",
            sortValue: (e) => e.StatementType,
            cell: (e) => e.StatementType,
          },
        ]}
        rowActions={(e) => (
          <>
            <RowAction asChild label="Open in editor">
              <Link {...editorLinkFor(e)}>
                <SquareCode aria-hidden className="size-3.5" />
              </Link>
            </RowAction>
            <RowAction
              label="Run again"
              onClick={() =>
                rerun.mutate({
                  QueryString: e.Query,
                  WorkGroup: e.WorkGroup,
                  QueryExecutionContext: e.QueryExecutionContext,
                  ExecutionParameters: e.ExecutionParameters,
                })
              }
            >
              <RotateCcw aria-hidden className="size-3.5" />
            </RowAction>
            {e.Status?.State === "SUCCEEDED" && (
              <RowAction asChild label="Open result">
                <Link
                  to="/athena"
                  search={{ ...editorLinkFor(e).search, execution: e.QueryExecutionId }}
                >
                  <FileText aria-hidden className="size-3.5" />
                </Link>
              </RowAction>
            )}
          </>
        )}
      />
    </ResourceListSection>
  )
}
