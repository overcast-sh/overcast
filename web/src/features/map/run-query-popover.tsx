/**
 * RunQueryPopover — *Run query* on a workgroup node: a compact editor holding
 * the workgroup's last SQL, run in its last query context, with the outcome
 * and a row count underneath. Anything more — a longer query, the result
 * grid, a different context — is *Open in editor* away.
 *
 * `⌘⏎` / `Ctrl+⏎` runs; `esc` closes (the popover's own contract).
 */

import { useState } from "react"
import * as PopoverPrimitive from "@radix-ui/react-popover"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Play } from "lucide-react"
import type { QueryExecutionContext } from "@aws-sdk/client-athena"
import { Advisory } from "@/components/ui/advisory"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Textarea } from "@/components/ui/textarea"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import {
  executionQueryOptions,
  firstResultPageQueryOptions,
  startQueryMutationOptions,
} from "@/features/athena/data"
import { isFinished } from "@/features/athena/execution-state"
import { athenaEditorLink } from "@/features/athena/links"
import type { TopologyQueryRun } from "@/types"
import { athenaExecutionRoute } from "./node-route"
import { resultCount } from "./run-query"

interface RunQueryPopoverProps {
  workGroup: string
  /** The workgroup's latest execution, whose SQL and context the editor starts from. */
  lastRun?: TopologyQueryRun
  engineState?: string
}

export function RunQueryPopover({ workGroup, lastRun, engineState }: RunQueryPopoverProps) {
  const [open, setOpen] = useState(false)
  return (
    <PopoverPrimitive.Root open={open} onOpenChange={setOpen}>
      <PopoverPrimitive.Trigger asChild>
        <button
          type="button"
          title="Run query"
          aria-label={`Run a query in ${workGroup}`}
          className="flex h-6 w-6 items-center justify-center rounded text-fg-muted transition-colors hover:bg-cat-8/15 hover:text-cat-8"
        >
          <Play className="h-3.5 w-3.5" />
        </button>
      </PopoverPrimitive.Trigger>
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          align="start"
          side="bottom"
          sideOffset={8}
          collisionPadding={12}
          onClick={(e) => e.stopPropagation()}
          className="z-50 flex w-[min(30rem,92vw)] flex-col gap-3 rounded-card border border-border bg-bg-elevated p-3 shadow-xl"
        >
          {open && (
            <RunQueryForm workGroup={workGroup} lastRunId={lastRun?.id} engineState={engineState} />
          )}
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}

function RunQueryForm({
  workGroup,
  lastRunId,
  engineState,
}: {
  workGroup: string
  lastRunId?: string
  engineState?: string
}) {
  const last = useQuery(executionQueryOptions(lastRunId ?? ""))
  if (lastRunId && last.isPending) return <Skeleton className="h-36 w-full" />
  return (
    <QueryEditor
      workGroup={workGroup}
      initialSql={last.data?.Query ?? ""}
      context={last.data?.QueryExecutionContext}
      engineState={engineState}
    />
  )
}

function QueryEditor({
  workGroup,
  initialSql,
  context,
  engineState,
}: {
  workGroup: string
  initialSql: string
  context?: QueryExecutionContext
  engineState?: string
}) {
  const [sql, setSql] = useState(initialSql)
  const start = useResourceMutation({
    options: startQueryMutationOptions(),
    errorTitle: "Could not start the query",
  })
  const runId = start.data ?? ""
  const execution = useQuery(executionQueryOptions(runId))
  const state = execution.data?.Status?.State
  const running = start.isPending || (runId !== "" && !isFinished(state) && !execution.isError)
  const database = context?.Database

  const run = () => {
    if (!sql.trim() || running) return
    start.mutate({ QueryString: sql, WorkGroup: workGroup, QueryExecutionContext: context })
  }

  return (
    <>
      <div className="flex items-baseline justify-between gap-3">
        <p className="text-sm font-semibold">Run in {workGroup}</p>
        <p className="truncate font-mono text-2xs text-fg-subtle">
          {[context?.Catalog, database].filter(Boolean).join(" / ") || "default database"}
        </p>
      </div>
      {engineState === "off" && (
        <Advisory
          title="The query engine is off"
          docsPath="services/athena/limitations.md#the-engine"
        >
          Queries succeed with no rows; DDL still updates the Glue Data Catalog.
        </Advisory>
      )}
      <Textarea
        aria-label="SQL"
        autoFocus
        value={sql}
        onChange={(e) => setSql(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault()
            run()
          }
        }}
        placeholder="SELECT …"
        rows={5}
        spellCheck={false}
        className="resize-y"
      />
      <div className="flex items-center gap-2">
        <Button size="sm" onClick={run} busy={running} busyLabel="Running" disabled={!sql.trim()}>
          <Play className="h-3.5 w-3.5" />
          Run
        </Button>
        <kbd className="rounded border border-border px-1.5 py-0.5 font-mono text-2xs text-fg-subtle">
          ⌘⏎
        </kbd>
        <Link
          {...athenaEditorLink({ catalog: context?.Catalog, database, sql })}
          className="ml-auto font-mono text-2xs text-fg-muted uppercase hover:text-fg hover:underline"
        >
          Open in editor
        </Link>
      </div>
      {execution.error && (
        <p role="alert" className="text-xs text-danger">
          {execution.error.message}
        </p>
      )}
      {runId && isFinished(state) && (
        <RunOutcome
          id={runId}
          state={state}
          reason={execution.data?.Status?.StateChangeReason}
          dml={execution.data?.StatementType === "DML"}
        />
      )}
    </>
  )
}

/** A finished run: its state, and the rows its result holds or why it did not succeed. */
function RunOutcome({
  id,
  state,
  reason,
  dml,
}: {
  id: string
  state?: string
  reason?: string
  /** A SELECT, whose result's first row is its column names. */
  dml: boolean
}) {
  const route = athenaExecutionRoute(id)
  const succeeded = state === "SUCCEEDED"
  const page = useQuery({ ...firstResultPageQueryOptions(id), enabled: succeeded })
  return (
    <div className="flex items-center gap-2 border-t border-border pt-2 text-xs">
      {!succeeded ? (
        <span role="alert" className="min-w-0 truncate text-danger" title={reason}>
          {state?.toLowerCase()}
          {reason ? `: ${reason}` : ""}
        </span>
      ) : page.data ? (
        <span className="text-fg-muted">
          {resultCount(page.data.ResultSet?.Rows?.length ?? 0, dml, !!page.data.NextToken)}
        </span>
      ) : (
        <Skeleton className="h-3 w-16" />
      )}
      <Link
        to={route.to}
        search={route.search}
        className="ml-auto shrink-0 font-mono text-2xs text-fg-muted uppercase hover:text-fg hover:underline"
      >
        View in history
      </Link>
    </div>
  )
}
