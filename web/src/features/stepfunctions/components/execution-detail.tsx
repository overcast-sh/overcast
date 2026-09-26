import { useEffect, useMemo, useState, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { Crosshair, ListTree, Play, RefreshCw, RotateCcw, Square } from "lucide-react"
import {
  sfnExecutionDefinitionQueryOptions,
  sfnExecutionHistoryQueryOptions,
  sfnExecutionQueryOptions,
  sfnKeys,
  sfnStateMachinesQueryOptions,
  redriveExecutionMutationOptions,
  startExecutionMutationOptions,
  stopExecutionMutationOptions,
} from "@/features/stepfunctions/data"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { useMediaQuery } from "@/hooks/use-media-query"
import { useNow } from "@/hooks/use-now"
import { executionArnFor } from "@/services/api/stepfunctions"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { ResizableSplit } from "@/components/ui/resizable-split"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import { PageHeader, Spinner, EmptyState, SectionLabel } from "@/components/ui/primitives"
import { ArnLink, ArnText } from "@/components/ui/arn-link"
import { cn } from "@/lib/utils"
import { executionStatusVariant, formatTimestamp } from "@/features/stepfunctions/format"
import { parseDefinition } from "../asl"
import { lambdaTargetOfRun } from "../lambda-invocations"
import type { ExecutionTab } from "../views"
import {
  buildTrace,
  formatDuration,
  type IterationSelection,
  type StateRun,
} from "../execution-trace"
import { ExecutionEvents } from "./execution-events"
import { ExecutionTimeline } from "./execution-timeline"
import { FlowDiagram } from "./flow-diagram"
import { JsonPane } from "./json-pane"
import { StartExecutionDialog } from "./start-execution-dialog"
import { StateInspector } from "./state-inspector"
import { ErrorCause } from "./error-cause"

interface Props {
  /** State machine name, from the route. */
  name: string
  /** Execution name, from the route. */
  execution: string
  /**
   * The execution's full ARN, when it cannot be derived from the machine and
   * execution names — a distributed Map's child executions carry the map run's
   * label in theirs.
   */
  executionArn?: string
  /** Selected state, deep-linkable. */
  state?: string
  onStateChange: (state: string | undefined) => void
  tab: ExecutionTab
  onTabChange: (tab: ExecutionTab) => void
}

/**
 * One execution, live. The flow diagram lights up each state as the history
 * reaches it — running states pulse, the newest move animates along its edge,
 * Map iterations fill in as they finish — with the selected state's input,
 * output, error and events beside it. Below, the same history as a timeline,
 * a filterable event list, and the execution's own input and output.
 */
export function ExecutionDetail({
  name,
  execution,
  executionArn: explicitArn,
  state: selectedState,
  onStateChange,
  tab,
  onTabChange,
}: Props) {
  const navigate = useNavigate()
  const wide = useMediaQuery("(min-width: 1024px)")
  const { data: machines = [], isLoading: listLoading } = useQuery(sfnStateMachinesQueryOptions())
  const stateMachineArn = useMemo(
    () => machines.find((m) => m.name === name)?.stateMachineArn ?? "",
    [machines, name],
  )
  const executionArn =
    explicitArn ?? (stateMachineArn ? executionArnFor(stateMachineArn, execution) : "")

  const {
    data: detail,
    isLoading: detailLoading,
    isError: detailError,
    isFetching,
    refetch,
  } = useQuery({ ...sfnExecutionQueryOptions(executionArn), retry: false })
  const live = detail?.status === "RUNNING"
  const {
    data: events = [],
    isLoading: historyLoading,
    refetch: refetchHistory,
  } = useQuery(sfnExecutionHistoryQueryOptions(executionArn, live))
  const { data: definition } = useQuery(sfnExecutionDefinitionQueryOptions(executionArn))

  // One last history read when the execution finishes, so the final events —
  // which land between the last live poll and the status flip — are shown.
  useEffect(() => {
    if (detail?.status && detail.status !== "RUNNING") void refetchHistory()
  }, [detail?.status, refetchHistory])

  const parsed = useMemo(() => parseDefinition(definition?.definition), [definition?.definition])
  const model = parsed.model
  const trace = useMemo(() => buildTrace(events, model), [events, model])
  const [iterations, setIterations] = useState<IterationSelection>({})
  const now = useNow(live, 250)
  const clock = live ? now : (trace.end ?? detail?.stopDate?.getTime() ?? now)

  const [showStop, setShowStop] = useState(false)
  const [showRerun, setShowRerun] = useState(false)
  const [showRedrive, setShowRedrive] = useState(false)

  const stopMut = useResourceMutation({
    options: stopExecutionMutationOptions(),
    invalidateKeys: [sfnKeys.execution(executionArn), sfnKeys.executions(stateMachineArn)],
    successTitle: "Execution stopped",
    onSuccess: () => setShowStop(false),
  })
  const redriveMut = useResourceMutation({
    options: redriveExecutionMutationOptions(),
    invalidateKeys: [sfnKeys.execution(executionArn), sfnKeys.executions(stateMachineArn)],
    successTitle: "Execution redriven",
    successDescription: () => "It resumes from the state that stopped it.",
    onSuccess: () => {
      setShowRedrive(false)
      void refetchHistory()
    },
  })
  const startMut = useResourceMutation({
    options: startExecutionMutationOptions(),
    invalidateKeys: [sfnKeys.executions(stateMachineArn)],
    successTitle: "Execution started",
    onSuccess: (data) => {
      setShowRerun(false)
      const newName = data.executionArn?.split(":").pop()
      if (newName)
        void navigate({
          to: "/stepfunctions/execution/$name/$execution",
          params: { name, execution: newName },
        })
    },
  })

  // Selecting a run from the timeline or event list also narrows the diagram
  // to the Map iterations it ran in, so the diagram shows that exact run.
  const selectRun = (run: StateRun) => {
    onStateChange(run.name)
    if (run.iterationPath.length) {
      setIterations((prev) => {
        const next = { ...prev }
        for (const frame of run.iterationPath) next[frame.map] = frame.index
        return next
      })
    }
  }

  const failedRun = trace.failedRunKey ? trace.runsByKey.get(trace.failedRunKey) : undefined
  const runningNames = [
    ...new Set(trace.runs.filter((r) => r.status === "running").map((r) => r.name)),
  ]

  if ((listLoading && !explicitArn) || (executionArn && detailLoading)) {
    return (
      <div className="flex justify-center py-16">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (!executionArn || detailError || !detail) {
    return (
      <EmptyState
        icon={<ListTree className="h-6 w-6" />}
        title="Execution not found"
        description={`No execution named "${execution}" on state machine "${name}".`}
        action={
          <Button size="sm" asChild>
            <Link to="/stepfunctions/$name" params={{ name }}>
              Back to {name}
            </Link>
          </Button>
        }
      />
    )
  }

  const duration = detail.startDate ? clock - detail.startDate.getTime() : undefined
  // Redrive resumes a Standard execution that failed, timed out or was
  // aborted; the API refuses anything else, so the button is not offered.
  const canRedrive =
    ["FAILED", "TIMED_OUT", "ABORTED"].includes(detail.status ?? "") &&
    definition?.stateMachineArn !== undefined &&
    machines.find((m) => m.stateMachineArn === stateMachineArn)?.type !== "EXPRESS"
  const diagram = model ? (
    <FlowDiagram
      model={model}
      trace={trace}
      live={live}
      selectedState={selectedState}
      onSelectState={onStateChange}
      iterationSelection={iterations}
      onIterationSelectionChange={setIterations}
      now={clock}
      exportName={`${name}-${execution}`}
      exportTitle={`${name} / ${execution}`}
    />
  ) : (
    <div className="flex h-full items-center justify-center p-6 text-center text-sm text-fg-muted">
      {definition ? parsed.error : "Loading the definition…"}
    </div>
  )

  const side =
    selectedState && model?.states.has(selectedState) ? (
      <StateInspector
        key={selectedState}
        model={model}
        stateName={selectedState}
        trace={trace}
        iterationSelection={iterations}
        now={clock}
        onClose={() => onStateChange(undefined)}
        onSelectState={onStateChange}
        machineName={name}
      />
    ) : (
      <ExecutionOverview
        input={detail.input}
        output={detail.output}
        live={live}
        runningNames={runningNames}
        onSelectState={onStateChange}
      />
    )

  return (
    <div className="flex w-full flex-col gap-4">
      <PageHeader
        title={execution}
        meta={<ArnText arn={executionArn} />}
        description={
          stateMachineArn ? (
            <ArnLink arn={stateMachineArn} label={name} className="text-sm" />
          ) : (
            <Link
              className="text-accent hover:underline"
              to="/stepfunctions/$name"
              params={{ name }}
            >
              {name}
            </Link>
          )
        }
        actions={
          <>
            <Button size="sm" variant="ghost" onClick={() => void refetch()} disabled={isFetching}>
              <RefreshCw className={cn("mr-1.5 h-3.5 w-3.5", isFetching && "animate-spin")} />
              Refresh
            </Button>
            <Button size="sm" variant="secondary" onClick={() => setShowRerun(true)}>
              <Play className="mr-1.5 h-3.5 w-3.5" />
              Run again
            </Button>
            {canRedrive && (
              <Button size="sm" variant="secondary" onClick={() => setShowRedrive(true)}>
                <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
                Redrive
              </Button>
            )}
            {live && (
              <Button size="sm" variant="danger" onClick={() => setShowStop(true)}>
                <Square className="mr-1.5 h-3.5 w-3.5" />
                Stop
              </Button>
            )}
          </>
        }
      />

      <div className="grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-border bg-border sm:grid-cols-3 lg:grid-cols-5">
        <Stat label="Status">
          <span className="flex items-center gap-2">
            <Badge variant={executionStatusVariant(detail.status)}>{detail.status}</Badge>
            {live && (
              <span className="flex items-center gap-1 text-2xs text-accent">
                <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-accent" /> live
              </span>
            )}
          </span>
        </Stat>
        <Stat label="Duration">
          <span className="tabular-nums">{formatDuration(duration)}</span>
        </Stat>
        <Stat label="Started">{formatTimestamp(detail.startDate)}</Stat>
        <Stat label="States run">
          <span className="tabular-nums">
            {trace.runs.length}
            <span className="text-fg-subtle"> · {events.length} events</span>
          </span>
        </Stat>
        <Stat label={live ? "Now running" : failedRun ? "Failed in" : "Stopped"}>
          {live ? (
            <StateLinks names={runningNames} onSelect={onStateChange} />
          ) : failedRun ? (
            <StateLinks
              names={[failedRun.name]}
              onSelect={() => selectRun(failedRun)}
              tone="danger"
            />
          ) : (
            formatTimestamp(detail.stopDate)
          )}
        </Stat>
      </div>

      {detail.error && (
        <div className="flex flex-wrap items-start gap-3">
          <ErrorCause
            error={detail.error}
            cause={detail.cause}
            className="min-w-48 flex-1 px-4 py-3 text-sm"
          />
          {failedRun && (
            <Button size="sm" variant="danger-ghost" onClick={() => selectRun(failedRun)}>
              <Crosshair className="mr-1.5 h-3.5 w-3.5" />
              {lambdaTargetOfRun(failedRun)
                ? `Logs for ${failedRun.name}`
                : `Show ${failedRun.name}`}
            </Button>
          )}
        </div>
      )}

      <div className="overflow-hidden rounded-lg border border-border bg-bg-elevated">
        {wide ? (
          <ResizableSplit
            direction="horizontal"
            sized="second"
            defaultSize={400}
            minSize={300}
            maxSize={720}
            storageKey="overcast.sfn.inspectorWidth"
            label="Resize the state details panel"
            className="h-[640px]"
            first={diagram}
            second={side}
            secondClassName="border-l border-border"
          />
        ) : (
          <div className="flex flex-col">
            <div className="h-[480px]">{diagram}</div>
            <div className="max-h-[560px] border-t border-border">{side}</div>
          </div>
        )}
      </div>

      <Tabs selectedKey={tab} onSelectionChange={(key) => onTabChange(key as ExecutionTab)}>
        <TabList aria-label="Execution views">
          <Tab id="timeline">Timeline</Tab>
          <Tab id="events">Event history</Tab>
          <Tab id="io">Input &amp; output</Tab>
          <Tab id="definition">Definition</Tab>
        </TabList>
        <TabPanel id="timeline" className="pt-4">
          <ExecutionTimeline
            model={model}
            trace={trace}
            now={clock}
            selectedState={selectedState}
            onSelectRun={selectRun}
          />
        </TabPanel>
        <TabPanel id="events" className="pt-4">
          <ExecutionEvents
            events={events}
            trace={trace}
            isLoading={historyLoading}
            onSelectRun={selectRun}
          />
        </TabPanel>
        <TabPanel id="io" className="grid gap-4 pt-4 md:grid-cols-2">
          <JsonPane label="Input" value={detail.input} bodyClassName="max-h-[28rem]" />
          <JsonPane
            label="Output"
            value={detail.output}
            empty={
              live
                ? "Still running…"
                : detail.status === "SUCCEEDED"
                  ? "—"
                  : "No output — the execution did not succeed."
            }
            bodyClassName="max-h-[28rem]"
          />
        </TabPanel>
        <TabPanel id="definition" className="pt-4">
          <JsonPane
            label="Definition used by this execution"
            value={definition?.definition}
            bodyClassName="max-h-[36rem]"
          />
        </TabPanel>
      </Tabs>

      <ConfirmDialog
        open={showStop}
        onOpenChange={setShowStop}
        title="Stop execution"
        description={
          <>
            Stop <span className="font-mono font-semibold">{execution}</span>? It ends as ABORTED
            and cannot be resumed.
          </>
        }
        confirmLabel="Stop execution"
        variant="danger"
        isPending={stopMut.isPending}
        onConfirm={() => stopMut.mutate({ executionArn })}
      />

      <ConfirmDialog
        open={showRedrive}
        onOpenChange={setShowRedrive}
        title="Redrive execution"
        description={
          <>
            Resume <span className="font-mono font-semibold">{execution}</span> from the state that
            stopped it? States that already succeeded are not run again.
          </>
        }
        confirmLabel="Redrive"
        isPending={redriveMut.isPending}
        onConfirm={() => redriveMut.mutate(executionArn)}
      />

      <StartExecutionDialog
        open={showRerun}
        onOpenChange={setShowRerun}
        initialInput={detail.input}
        pending={startMut.isPending}
        onStart={({ input, name: newName }) =>
          startMut.mutate({ stateMachineArn, input, name: newName })
        }
      />
    </div>
  )
}

function Stat({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1 bg-bg-elevated px-4 py-3 max-lg:last:col-span-2">
      <SectionLabel>{label}</SectionLabel>
      <div className="min-w-0 truncate font-mono text-xs text-fg">{children}</div>
    </div>
  )
}

function StateLinks({
  names,
  onSelect,
  tone,
}: {
  names: string[]
  onSelect: (name: string) => void
  tone?: "danger"
}) {
  if (names.length === 0) return <span className="text-fg-subtle">—</span>
  return (
    <span className="flex min-w-0 flex-wrap gap-x-2">
      {names.slice(0, 3).map((n) => (
        <button
          key={n}
          type="button"
          onClick={() => onSelect(n)}
          className={cn(
            "truncate hover:underline",
            tone === "danger" ? "text-danger" : "text-accent",
          )}
        >
          {n}
        </button>
      ))}
      {names.length > 3 && <span className="text-fg-subtle">+{names.length - 3}</span>}
    </span>
  )
}

function ExecutionOverview({
  input,
  output,
  live,
  runningNames,
  onSelectState,
}: {
  input?: string
  output?: string
  live: boolean
  runningNames: string[]
  onSelectState: (name: string) => void
}) {
  return (
    <div className="flex h-full min-h-0 flex-col gap-4 overflow-y-auto px-4 py-3">
      <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-fg-muted">
        Select a state in the diagram to see what it received, what it returned, how long it took
        and every event it logged.
        {live && runningNames.length > 0 && (
          <>
            {" "}
            Running now:{" "}
            {runningNames.map((n, i) => (
              <span key={n}>
                {i > 0 && ", "}
                <button
                  type="button"
                  className="text-accent hover:underline"
                  onClick={() => onSelectState(n)}
                >
                  {n}
                </button>
              </span>
            ))}
            .
          </>
        )}
      </p>
      <JsonPane label="Execution input" value={input} bodyClassName="max-h-64" />
      <JsonPane
        label="Execution output"
        value={output}
        empty={live ? "Still running…" : "—"}
        bodyClassName="max-h-64"
      />
    </div>
  )
}
