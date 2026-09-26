import { useState, type RefObject } from "react"
import { useQuery } from "@tanstack/react-query"
import { ResizableSplit } from "@/components/ui/resizable-split"
import { EmptyState } from "@/components/ui/primitives"
import type { AthenaEngineStatus } from "@/types"
import { engineStatusQueryOptions, preparedStatementQueryOptions } from "../../data"
import { isInert } from "../../engine-chip"
import { queryRun } from "../../query-input"
import type { QueryTab } from "../../query-tabs"
import type { CompletionContext } from "../../sql-completion"
import { SQL_KEYWORDS } from "../../sql-keywords"
import { errorPosition, executedStatement, formatSql, placeholderCount } from "../../sql-text"
import { useQueryRun } from "../../use-query-run"
import { SaveQueryDialog } from "../save-query-dialog"
import { EditorToolbar } from "./editor-toolbar"
import { InertEngineAdvisory } from "./engine-status"
import { ParametersStrip } from "./parameters-strip"
import { QueryError } from "./query-error"
import { QueryResult } from "./query-result"
import { RunStatus } from "./run-status"
import { SqlEditor, type SqlEditorHandle, type SqlError } from "./sql-editor"

/**
 * One query tab: its toolbar, the editor, the parameters its SQL asks for,
 * and its last run — state, statistics, error or result. Mounted per tab
 * (keyed by its id), so a run in one tab never shows in another.
 */
export interface QueryPaneProps {
  tab: QueryTab
  onChange: (patch: Partial<QueryTab>) => void
  completion: CompletionContext
  engine: AthenaEngineStatus | undefined
  /** The editor, which the data browser inserts names into. */
  editorRef: RefObject<SqlEditorHandle | null>
}

/**
 * How many values an `EXECUTE name` needs: the `?`s of the prepared
 * statement it runs, which are not in the tab's SQL. Undefined for any
 * other SQL, whose own `?`s count.
 */
function useExecuteCount(tab: QueryTab): number | undefined {
  const name = executedStatement(tab.sql) ?? ""
  const { data } = useQuery(preparedStatementQueryOptions(tab.workGroup, name))
  return name && data?.QueryStatement !== undefined
    ? placeholderCount(data.QueryStatement)
    : undefined
}

export function QueryPane({ tab, onChange, completion, engine, editorRef }: QueryPaneProps) {
  const [saving, setSaving] = useState(false)
  const run = useQueryRun(tab, (executionId) => onChange({ executionId }))
  const executeCount = useExecuteCount(tab)
  // While this tab's query waits, the engine may be starting for it: poll it closely.
  useQuery({ ...engineStatusQueryOptions(true), enabled: run.running })
  const { execution } = run
  const failure = execution?.Status?.State === "FAILED" ? execution.Status : undefined
  const failureMessage = failure?.AthenaError?.ErrorMessage ?? failure?.StateChangeReason ?? ""
  const position = failure ? errorPosition(failureMessage) : null
  const sqlError: SqlError | undefined =
    position && execution?.QueryExecutionId
      ? { ...position, message: failureMessage, executionId: execution.QueryExecutionId }
      : undefined

  const outcome = run.error ? (
    <QueryError announce sql={tab.sql} message={run.error.message} />
  ) : failure ? (
    <QueryError
      announce
      sql={execution?.Query ?? tab.sql}
      error={failure.AthenaError}
      message={failureMessage}
    />
  ) : execution?.Status?.State === "SUCCEEDED" ? (
    <QueryResult execution={execution} inert={isInert(engine)} />
  ) : execution ? null : (
    <EmptyState
      className="py-10"
      title="Run a query to see its result"
      description="Results, statistics and errors show here."
    />
  )

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <EditorToolbar
        tab={tab}
        request={queryRun(tab, { executeCount })}
        onWorkGroupChange={(workGroup) => onChange({ workGroup })}
        running={run.running}
        stopping={run.stopping}
        onRun={() => run.run(queryRun(tab, { executeCount }))}
        onRunSelection={() => editorRef.current?.runSelection()}
        onStop={run.stop}
        onFormat={() => editorRef.current?.replaceAll(formatSql(tab.sql, SQL_KEYWORDS))}
        onSave={() => setSaving(true)}
      />
      {engine && isInert(engine) && <InertEngineAdvisory status={engine} />}
      <ParametersStrip
        count={executeCount ?? placeholderCount(tab.sql)}
        values={tab.parameters}
        onChange={(parameters) => onChange({ parameters })}
      />
      <ResizableSplit
        direction="vertical"
        sized="first"
        defaultSize={200}
        minSize={96}
        maxSize={640}
        storageKey="overcast:athena:editor-height"
        label="Resize the editor"
        className="min-h-0 flex-1"
        firstClassName="overflow-hidden rounded-md border border-border"
        secondClassName="flex min-h-0 flex-col gap-2 pt-2"
        first={
          <SqlEditor
            ref={editorRef}
            path={tab.id}
            defaultValue={tab.sql}
            onChange={(sql) => onChange({ sql })}
            onRun={(selection) =>
              run.run(
                selection
                  ? queryRun(tab, { selection: selection.sql, offset: selection.offset })
                  : queryRun(tab, { executeCount }),
              )
            }
            onStop={run.stop}
            running={run.running}
            completion={completion}
            error={sqlError}
          />
        }
        second={
          <>
            {execution && <RunStatus execution={execution} engine={engine} />}
            {outcome}
          </>
        }
      />
      {saving && <SaveQueryDialog tab={tab} open onOpenChange={setSaving} />}
    </div>
  )
}
