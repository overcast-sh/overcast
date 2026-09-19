import { useEffect, useMemo, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { ListTree, Pencil, Play, RefreshCw, Search } from "lucide-react"
import type { ExecutionListItem } from "@aws-sdk/client-sfn"
import {
  sfnStateMachineQueryOptions,
  sfnStateMachinesQueryOptions,
  sfnExecutionsQueryOptions,
  sfnExecutionQueryOptions,
  sfnKeys,
  startExecutionMutationOptions,
  updateStateMachineMutationOptions,
} from "@/features/stepfunctions/data"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { useMediaQuery } from "@/hooks/use-media-query"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ResourceTable } from "@/components/ui/resource-table"
import { ResizableSplit } from "@/components/ui/resizable-split"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import { Badge } from "@/components/ui/badge"
import { DefinitionCard, Definition } from "@/components/ui/definition-card"
import { PageHeader, Spinner, EmptyState, SectionLabel } from "@/components/ui/primitives"
import { ArnText } from "@/components/ui/arn-link"
import { cn } from "@/lib/utils"
import { executionStatusVariant, formatTimestamp } from "@/features/stepfunctions/format"
import { parseDefinition } from "../asl"
import type { StateMachineTab } from "../views"
import { formatDuration } from "../execution-trace"
import { stateTypeTheme } from "../state-theme"
import { DefinitionEditorDialog } from "./definition-editor-dialog"
import { FlowDiagram } from "./flow-diagram"
import { JsonPane } from "./json-pane"
import { StartExecutionDialog } from "./start-execution-dialog"
import { StateInspector } from "./state-inspector"

type StatusFilter = "all" | "RUNNING" | "SUCCEEDED" | "FAILED" | "TIMED_OUT" | "ABORTED"

const STATUS_FILTERS: Array<{ id: StatusFilter; label: string }> = [
  { id: "all", label: "All" },
  { id: "RUNNING", label: "Running" },
  { id: "SUCCEEDED", label: "Succeeded" },
  { id: "FAILED", label: "Failed" },
  { id: "TIMED_OUT", label: "Timed out" },
  { id: "ABORTED", label: "Aborted" },
]

interface Props {
  /** State machine name, taken from the route. */
  name: string
  /**
   * The tab the URL asked for. Unset, the page picks: the diagram for a
   * machine that has never run — there is nothing else to look at yet — and
   * the executions otherwise.
   */
  tab: StateMachineTab | undefined
  onTabChange: (tab: StateMachineTab) => void
}

function executionDuration(e: ExecutionListItem, now: number): number | undefined {
  if (!e.startDate) return undefined
  return (e.stopDate?.getTime() ?? now) - e.startDate.getTime()
}

function median(values: number[]): number | undefined {
  if (values.length === 0) return undefined
  const sorted = [...values].sort((a, b) => a - b)
  const mid = Math.floor(sorted.length / 2)
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2
}

/**
 * A state machine: its executions — filterable by outcome, with the numbers
 * that say whether it is healthy — its definition as a flow diagram you can
 * click through state by state, and its configuration. Starting an execution
 * opens its live view.
 */
export function StateMachineDetail({ name, tab: requestedTab, onTabChange }: Props) {
  const navigate = useNavigate()
  const wide = useMediaQuery("(min-width: 1024px)")
  const [showStart, setShowStart] = useState(false)
  const [showEdit, setShowEdit] = useState(false)
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all")
  const [search, setSearch] = useState("")
  const [selectedState, setSelectedState] = useState<string>()

  // Executions and the definition are both addressed by ARN; the route carries
  // the name, so resolve it through the list first.
  const { data: machines = [], isLoading: listLoading } = useQuery(sfnStateMachinesQueryOptions())
  const arn = useMemo(
    () => machines.find((m) => m.name === name)?.stateMachineArn ?? "",
    [machines, name],
  )

  const { data: machine, refetch: refetchMachine } = useQuery(sfnStateMachineQueryOptions(arn))
  const {
    data: executions = [],
    isLoading: execsLoading,
    isFetching,
    refetch,
    error: execsError,
  } = useQuery(sfnExecutionsQueryOptions(arn))

  const tab: StateMachineTab =
    requestedTab ??
    (!execsLoading && !execsError && executions.length === 0 ? "diagram" : "executions")
  const anyRunning = executions.some((e) => e.status === "RUNNING")
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!anyRunning) return
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [anyRunning])

  const latest = useMemo(
    () =>
      [...executions]
        .sort((a, b) => (b.startDate?.getTime() ?? 0) - (a.startDate?.getTime() ?? 0))
        .at(0),
    [executions],
  )
  // The last run's input, fetched on demand for the start dialog's "Last input".
  const latestArn = latest?.executionArn ?? ""
  const { data: latestDetail } = useQuery({
    ...sfnExecutionQueryOptions(latestArn),
    enabled: showStart && latestArn !== "",
  })

  const parsed = useMemo(() => parseDefinition(machine?.definition), [machine?.definition])

  const stats = useMemo(() => {
    const count = (s: string) => executions.filter((e) => e.status === s).length
    const finished = executions.filter((e) => e.status !== "RUNNING")
    const succeeded = count("SUCCEEDED")
    return {
      total: executions.length,
      running: count("RUNNING"),
      succeeded,
      failed: count("FAILED") + count("TIMED_OUT"),
      aborted: count("ABORTED"),
      successRate: finished.length ? Math.round((succeeded / finished.length) * 100) : undefined,
      medianMs: median(
        executions
          .filter((e) => e.status === "SUCCEEDED")
          .map((e) => executionDuration(e, now))
          .filter((d): d is number => d !== undefined),
      ),
      byStatus: Object.fromEntries(
        STATUS_FILTERS.map((f) => [f.id, f.id === "all" ? executions.length : count(f.id)]),
      ) as Record<StatusFilter, number>,
    }
  }, [executions, now])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return executions.filter(
      (e) =>
        (statusFilter === "all" || e.status === statusFilter) &&
        (!q || (e.name ?? "").toLowerCase().includes(q)),
    )
  }, [executions, statusFilter, search])

  const startMut = useResourceMutation({
    options: startExecutionMutationOptions(),
    invalidateKeys: [sfnKeys.executions(arn)],
    successTitle: "Execution started",
    onSuccess: (data) => {
      setShowStart(false)
      const execution = data.executionArn?.split(":").pop()
      if (execution)
        void navigate({
          to: "/stepfunctions/execution/$name/$execution",
          params: { name, execution },
        })
    },
  })

  const updateMut = useResourceMutation({
    options: updateStateMachineMutationOptions(),
    invalidateKeys: [sfnKeys.stateMachine(arn)],
    successTitle: "Definition saved",
    onSuccess: () => {
      setShowEdit(false)
      void refetchMachine()
    },
  })

  if (listLoading) {
    return (
      <div className="flex justify-center py-16">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (!arn) {
    return (
      <EmptyState
        icon={<ListTree className="h-6 w-6" />}
        title="State machine not found"
        description={`No state machine named "${name}".`}
        action={
          <Button size="sm" asChild>
            <Link to="/stepfunctions">Back to state machines</Link>
          </Button>
        }
      />
    )
  }

  const stateCount = parsed.model?.states.size ?? 0
  const typeCounts = parsed.model
    ? [...parsed.model.states.values()].reduce<Record<string, number>>((acc, s) => {
        acc[s.type] = (acc[s.type] ?? 0) + 1
        return acc
      }, {})
    : {}

  const diagram = parsed.model ? (
    <FlowDiagram
      model={parsed.model}
      selectedState={selectedState}
      onSelectState={setSelectedState}
      exportName={name}
      exportTitle={name}
    />
  ) : (
    <div className="flex h-full items-center justify-center p-6 text-center text-sm text-fg-muted">
      {machine ? parsed.error : "Loading the definition…"}
    </div>
  )
  const side =
    parsed.model && selectedState && parsed.model.states.has(selectedState) ? (
      <StateInspector
        key={selectedState}
        model={parsed.model}
        stateName={selectedState}
        now={now}
        onClose={() => setSelectedState(undefined)}
        onSelectState={setSelectedState}
      />
    ) : (
      <div className="flex h-full flex-col gap-4 overflow-y-auto px-4 py-3">
        <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-fg-muted">
          Select a state to see what it does, where it goes next, how it retries and what it
          catches.
        </p>
        {!execsLoading && executions.length === 0 && (
          <div className="flex flex-col gap-2 rounded-md border border-accent/30 bg-accent-muted/40 px-3 py-2.5 text-xs text-fg">
            <p>
              This state machine has not run yet. Start an execution and this diagram lights up as
              it runs.
            </p>
            <Button size="sm" className="self-start" onClick={() => setShowStart(true)}>
              <Play className="mr-1.5 h-3.5 w-3.5" /> Start execution
            </Button>
          </div>
        )}
        {parsed.model?.comment && <p className="text-sm text-fg">{parsed.model.comment}</p>}
        <div className="flex flex-col gap-1.5">
          <SectionLabel>{stateCount} states</SectionLabel>
          <ul className="flex flex-wrap gap-1.5">
            {Object.entries(typeCounts).map(([type, n]) => {
              const theme = stateTypeTheme(type)
              const Icon = theme.icon
              return (
                <li
                  key={type}
                  className="flex items-center gap-1.5 rounded-md border border-border px-2 py-1 text-xs text-fg-muted"
                >
                  <Icon className="h-3.5 w-3.5" style={{ color: theme.color }} />
                  {theme.label}
                  <span className="font-mono text-fg-subtle">{n}</span>
                </li>
              )
            })}
          </ul>
        </div>
        {parsed.model && parsed.model.issues.length > 0 && (
          <div className="rounded-md border border-warning/30 bg-warning-muted px-3 py-2 text-xs text-warning">
            <p className="font-semibold">Definition issues</p>
            <ul className="mt-1 list-disc pl-4">
              {parsed.model.issues.map((i) => (
                <li key={i}>{i}</li>
              ))}
            </ul>
          </div>
        )}
      </div>
    )

  return (
    <div className="flex w-full flex-col gap-4">
      <PageHeader
        title={name}
        meta={<ArnText arn={arn} />}
        description={
          <span className="flex items-center gap-2">
            {machine?.type && <Badge>{machine.type}</Badge>}
            <span>
              {stateCount} states · created {formatTimestamp(machine?.creationDate)}
            </span>
          </span>
        }
        actions={
          <>
            <Button size="sm" variant="ghost" onClick={() => void refetch()} disabled={isFetching}>
              <RefreshCw className={cn("mr-1.5 h-3.5 w-3.5", isFetching && "animate-spin")} />
              Refresh
            </Button>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => setShowEdit(true)}
              disabled={!machine}
            >
              <Pencil className="mr-1.5 h-3.5 w-3.5" />
              Edit definition
            </Button>
            <Button size="sm" onClick={() => setShowStart(true)}>
              <Play className="mr-1.5 h-3.5 w-3.5" />
              Start execution
            </Button>
          </>
        }
      />

      <div className="grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-border bg-border sm:grid-cols-3 lg:grid-cols-6">
        <Stat label="Executions" value={String(stats.total)} />
        <Stat
          label="Running"
          value={String(stats.running)}
          tone={stats.running ? "accent" : undefined}
        />
        <Stat
          label="Succeeded"
          value={String(stats.succeeded)}
          tone={stats.succeeded ? "success" : undefined}
        />
        <Stat
          label="Failed"
          value={String(stats.failed + stats.aborted)}
          tone={stats.failed ? "danger" : undefined}
        />
        <Stat
          label="Success rate"
          value={stats.successRate === undefined ? "—" : `${stats.successRate}%`}
        />
        <Stat label="Median duration" value={formatDuration(stats.medianMs)} />
      </div>

      <Tabs selectedKey={tab} onSelectionChange={(key) => onTabChange(key as StateMachineTab)}>
        <TabList aria-label="State machine views">
          <Tab id="executions">Executions</Tab>
          <Tab id="diagram">Diagram</Tab>
          <Tab id="details">Details</Tab>
        </TabList>

        <TabPanel id="executions" className="flex flex-col gap-3 pt-4">
          <div className="flex flex-wrap items-center gap-2">
            <div role="radiogroup" aria-label="Filter by status" className="flex flex-wrap gap-1">
              {STATUS_FILTERS.map((f) => (
                <button
                  key={f.id}
                  type="button"
                  role="radio"
                  aria-checked={statusFilter === f.id}
                  onClick={() => setStatusFilter(f.id)}
                  disabled={f.id !== "all" && stats.byStatus[f.id] === 0 && statusFilter !== f.id}
                  className={cn(
                    "flex h-7 items-center gap-1.5 rounded-full border px-3 text-2xs font-medium transition-colors disabled:opacity-40",
                    statusFilter === f.id
                      ? "border-accent/40 bg-accent-muted text-accent"
                      : "border-border text-fg-muted hover:bg-bg-muted hover:text-fg",
                  )}
                >
                  {f.label}
                  <span className="font-mono tabular-nums opacity-70">{stats.byStatus[f.id]}</span>
                </button>
              ))}
            </div>
            <div className="relative ml-auto w-full max-w-64">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-fg-subtle" />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Filter by name…"
                aria-label="Filter executions by name"
                className="h-7 pl-8 text-xs"
              />
            </div>
          </div>
          <ResourceTable
            variant="embedded"
            query={{ data: filtered, isLoading: execsLoading, error: execsError }}
            noun="executions"
            emptyIcon={Play}
            emptyTitle="No executions"
            emptyDescription="Start an execution to run this state machine and watch it move through the diagram live."
            emptyAction={
              <div className="flex gap-2">
                <Button size="sm" variant="secondary" onClick={() => onTabChange("diagram")}>
                  View diagram
                </Button>
                <Button size="sm" onClick={() => setShowStart(true)}>
                  <Play className="mr-1.5 h-3.5 w-3.5" /> Start execution
                </Button>
              </div>
            }
            isFiltered={statusFilter !== "all" || search !== ""}
            onClearFilter={() => {
              setStatusFilter("all")
              setSearch("")
            }}
            rowKey={(execution) => execution.executionArn ?? ""}
            onRowClick={(execution) =>
              void navigate({
                to: "/stepfunctions/execution/$name/$execution",
                params: { name, execution: execution.name ?? "" },
              })
            }
            // Most recent run first: a reader arriving here is looking for the
            // last one, and the list refreshes while runs are in flight, so the
            // order must not depend on the emulator's storage order.
            defaultSort={{ id: "started", desc: true }}
            columns={[
              {
                id: "name",
                header: "Name",
                sortValue: (execution) => execution.name,
                cell: (execution) => (
                  <Link
                    className="font-medium text-accent hover:underline"
                    to="/stepfunctions/execution/$name/$execution"
                    params={{ name, execution: execution.name ?? "" }}
                    onClick={(e) => e.stopPropagation()}
                  >
                    {execution.name}
                  </Link>
                ),
              },
              {
                id: "status",
                header: "Status",
                sortValue: (execution) => execution.status,
                cell: (execution) => (
                  <Badge variant={executionStatusVariant(execution.status)}>
                    {execution.status}
                  </Badge>
                ),
              },
              {
                id: "started",
                header: "Started",
                cellClassName: "text-fg-muted",
                sortValue: (execution) => execution.startDate,
                cell: (execution) => formatTimestamp(execution.startDate),
              },
              {
                id: "duration",
                header: "Duration",
                cellClassName: "text-fg-muted tabular-nums",
                sortValue: (execution) => executionDuration(execution, now),
                cell: (execution) =>
                  formatDuration(executionDuration(execution, now)) +
                  (execution.status === "RUNNING" ? "…" : ""),
              },
              {
                id: "stopped",
                header: "Stopped",
                cellClassName: "text-fg-muted",
                sortValue: (execution) => execution.stopDate,
                cell: (execution) => formatTimestamp(execution.stopDate),
              },
            ]}
          />
        </TabPanel>

        <TabPanel id="diagram" className="pt-4">
          <div className="overflow-hidden rounded-lg border border-border bg-bg-elevated">
            {wide ? (
              <ResizableSplit
                direction="horizontal"
                sized="second"
                defaultSize={380}
                minSize={300}
                maxSize={680}
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
                <div className="max-h-[480px] border-t border-border">{side}</div>
              </div>
            )}
          </div>
        </TabPanel>

        <TabPanel id="details" className="flex flex-col gap-4 pt-4">
          <DefinitionCard title="Configuration">
            <Definition label="Type" value={machine?.type} />
            <Definition label="Status" value={machine?.status} />
            <Definition label="Created" value={formatTimestamp(machine?.creationDate)} />
            <Definition label="Role" value={machine?.roleArn} copyable />
            <Definition label="Logging" value={machine?.loggingConfiguration?.level} />
            <Definition
              label="Tracing"
              value={machine?.tracingConfiguration?.enabled ? "Enabled" : "Disabled"}
            />
            <Definition label="Revision" value={machine?.revisionId} copyable />
            <Definition label="Query language" value={parsed.model?.queryLanguage} />
          </DefinitionCard>
          <JsonPane label="Definition" value={machine?.definition} bodyClassName="max-h-[40rem]" />
        </TabPanel>
      </Tabs>

      <StartExecutionDialog
        open={showStart}
        onOpenChange={setShowStart}
        lastInput={latestDetail?.input}
        pending={startMut.isPending}
        onStart={({ input, name: executionName }) =>
          startMut.mutate({ stateMachineArn: arn, input, name: executionName })
        }
      />

      <DefinitionEditorDialog
        open={showEdit}
        onOpenChange={setShowEdit}
        mode="edit"
        name={name}
        initialDefinition={machine?.definition}
        pending={updateMut.isPending}
        onSubmit={(result) => updateMut.mutate({ arn, definition: result.definition })}
      />
    </div>
  )
}

function Stat({
  label,
  value,
  tone,
}: {
  label: string
  value: string
  tone?: "accent" | "success" | "danger"
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1 bg-bg-elevated px-4 py-3">
      <SectionLabel>{label}</SectionLabel>
      <span
        className={cn(
          "font-mono text-lg font-semibold tabular-nums",
          tone === "accent"
            ? "text-accent"
            : tone === "success"
              ? "text-success"
              : tone === "danger"
                ? "text-danger"
                : "text-fg",
        )}
      >
        {value}
      </span>
    </div>
  )
}
