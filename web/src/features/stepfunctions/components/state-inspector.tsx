/**
 * The side panel beside the flow diagram: everything about the selected state.
 *
 * Without an execution it explains the state as written — what it calls, where
 * it goes next, how it retries and what it catches. With one it adds what
 * happened each time the state ran: status, timing, attempts, the input it got
 * and the output it produced, the error it failed with, and its own slice of
 * the event history.
 */
import { useMemo, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ArrowRight, X } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { SectionLabel } from "@/components/ui/primitives"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import { formatPreciseTimeOfDay } from "@/lib/format"
import { cn } from "@/lib/utils"
import { parseTaskResource, type AslModel, type AslState } from "../asl"
import {
  describeIterationPath,
  formatDuration,
  matchesSelection,
  runDuration,
  type ExecutionTrace,
  type IterationSelection,
  type StateRun,
} from "../execution-trace"
import { sfnMapRunExecutionsQueryOptions } from "../data"
import { EventType } from "./event-type"
import { STATUS_THEME, stateTypeTheme } from "../state-theme"
import { JsonPane } from "./json-pane"

interface Props {
  model: AslModel
  stateName: string
  trace?: ExecutionTrace
  iterationSelection?: IterationSelection
  now: number
  onClose: () => void
  /** Moves the selection to another state, e.g. from a "Next" link. */
  onSelectState: (name: string) => void
  /** The state machine's name, for links to a distributed Map's child executions. */
  machineName?: string
}

export function StateInspector({
  model,
  stateName,
  trace,
  iterationSelection = {},
  now,
  onClose,
  onSelectState,
  machineName,
}: Props) {
  const state = model.states.get(stateName)
  const runs = useMemo(
    () =>
      (trace?.runsByName.get(stateName) ?? []).filter((r) =>
        matchesSelection(r.iterationPath, iterationSelection),
      ),
    [trace, stateName, iterationSelection],
  )
  const defaultRun = useMemo(
    () =>
      runs.find((r) => r.status === "running") ??
      runs.find((r) => r.status === "failed") ??
      runs.at(-1),
    [runs],
  )
  // The caller keys this component on the state name, so a different state
  // starts fresh on its own default run and tab.
  const [runKey, setRunKey] = useState<string | undefined>()
  const run = runs.find((r) => r.key === runKey) ?? defaultRun
  const [chosenTab, setTab] = useState("io")
  const tab = trace ? chosenTab : "definition"

  if (!state) return null
  const theme = stateTypeTheme(state.type)
  const Icon = theme.icon

  return (
    <aside className="flex h-full min-h-0 flex-col" aria-label={`State ${state.name}`}>
      <header className="flex items-start gap-2.5 border-b border-border px-4 py-3">
        <span
          className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md"
          style={{
            background: `color-mix(in oklab, ${theme.color} 15%, transparent)`,
            color: theme.color,
          }}
        >
          <Icon className="h-4 w-4" />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <h2 className="truncate text-sm font-semibold text-fg" title={state.name}>
            {state.name}
          </h2>
          <p className="truncate text-xs text-fg-muted">
            {theme.label} · {state.summary}
          </p>
        </div>
        {run && (
          <Badge variant={STATUS_THEME[run.status].badge}>{STATUS_THEME[run.status].label}</Badge>
        )}
        <Button size="icon-sm" variant="ghost" aria-label="Close state details" onClick={onClose}>
          <X className="h-3.5 w-3.5" />
        </Button>
      </header>

      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-3">
        {trace && runs.length === 0 && (
          <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-fg-muted">
            This state has not run
            {Object.values(iterationSelection).some((v) => v !== undefined)
              ? " in the selected iteration"
              : " in this execution"}
            .
          </p>
        )}

        {runs.length > 1 && <RunPicker runs={runs} selected={run} now={now} onSelect={setRunKey} />}

        {run && <RunSummary run={run} now={now} />}

        {run?.mapRunArn && machineName && (
          <MapRunChildren mapRunArn={run.mapRunArn} machineName={machineName} />
        )}

        <Tabs selectedKey={tab} onSelectionChange={setTab}>
          <TabList aria-label="State details">
            {trace && <Tab id="io">Input &amp; output</Tab>}
            {trace && <Tab id="events">Events</Tab>}
            <Tab id="definition">Definition</Tab>
          </TabList>
          {trace && (
            <TabPanel id="io" className="flex flex-col gap-3 pt-3">
              <JsonPane label="Input" value={run?.input} bodyClassName="max-h-72" />
              <JsonPane
                label="Output"
                value={run?.output}
                empty={
                  run?.status === "running"
                    ? "Still running…"
                    : run?.status === "failed"
                      ? "No output — the state failed."
                      : "—"
                }
                bodyClassName="max-h-72"
              />
            </TabPanel>
          )}
          {trace && (
            <TabPanel id="events" className="pt-3">
              <RunEvents run={run} trace={trace} />
            </TabPanel>
          )}
          <TabPanel id="definition" className="flex flex-col gap-3 pt-3">
            <StateDefinition model={model} state={state} onSelectState={onSelectState} />
          </TabPanel>
        </Tabs>
      </div>
    </aside>
  )
}

/** A distributed Map's work happens in child executions; list them, each linking to its own live view. */
function MapRunChildren({ mapRunArn, machineName }: { mapRunArn: string; machineName: string }) {
  const { data: children = [], isLoading } = useQuery(sfnMapRunExecutionsQueryOptions(mapRunArn))
  const counts = children.reduce<Record<string, number>>((acc, c) => {
    acc[c.status ?? "?"] = (acc[c.status ?? "?"] ?? 0) + 1
    return acc
  }, {})
  return (
    <div className="flex flex-col gap-1.5">
      <SectionLabel>
        Child executions
        {children.length > 0 &&
          ` · ${Object.entries(counts)
            .map(([status, n]) => `${n} ${status.toLowerCase()}`)
            .join(", ")}`}
      </SectionLabel>
      {isLoading ? (
        <p className="text-xs text-fg-muted">Loading…</p>
      ) : children.length === 0 ? (
        <p className="text-xs text-fg-muted">No child executions yet.</p>
      ) : (
        <ul className="flex max-h-48 flex-col gap-px overflow-y-auto rounded-md border border-border bg-bg-muted p-1">
          {children.map((child) => (
            <li key={child.executionArn}>
              <Link
                to="/stepfunctions/execution/$name/$execution"
                params={{ name: machineName, execution: child.name ?? "" }}
                search={{ arn: child.executionArn }}
                className="flex items-center gap-2 rounded px-2 py-1 font-mono text-2xs text-fg-muted hover:bg-bg-elevated hover:text-accent"
              >
                <span
                  className="h-2 w-2 shrink-0 rounded-full"
                  style={{ background: executionStatusColor(child.status) }}
                />
                <span className="min-w-0 flex-1 truncate">{child.name}</span>
                <span className="shrink-0">{child.status}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function executionStatusColor(status: string | undefined): string {
  if (status === "SUCCEEDED") return STATUS_THEME.succeeded.color
  if (status === "RUNNING") return STATUS_THEME.running.color
  if (status === "FAILED" || status === "TIMED_OUT") return STATUS_THEME.failed.color
  return STATUS_THEME.aborted.color
}

function RunPicker({
  runs,
  selected,
  now,
  onSelect,
}: {
  runs: StateRun[]
  selected: StateRun | undefined
  now: number
  onSelect: (key: string) => void
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <SectionLabel>{runs.length} runs</SectionLabel>
      <ul className="flex max-h-36 flex-col gap-px overflow-y-auto rounded-md border border-border bg-bg-muted p-1">
        {runs.map((r, i) => (
          <li key={r.key}>
            <button
              type="button"
              onClick={() => onSelect(r.key)}
              aria-current={selected?.key === r.key}
              className={cn(
                "flex w-full items-center gap-2 rounded px-2 py-1 text-left font-mono text-2xs transition-colors",
                selected?.key === r.key
                  ? "bg-bg-elevated text-fg shadow-xs"
                  : "text-fg-muted hover:bg-bg-elevated/60",
              )}
            >
              <span
                className="h-2 w-2 shrink-0 rounded-full"
                style={{ background: STATUS_THEME[r.status].color }}
              />
              <span className="w-8 shrink-0 text-fg-subtle">#{i + 1}</span>
              <span className="min-w-0 flex-1 truncate">
                {describeIterationPath(r.iterationPath) || "top level"}
              </span>
              <span className="shrink-0 tabular-nums">{formatDuration(runDuration(r, now))}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

function RunSummary({ run, now }: { run: StateRun; now: number }) {
  return (
    <div className="flex flex-col gap-3">
      <DefinitionList layout="inline">
        <Definition label="Started" value={formatPreciseTimeOfDay(run.start)} />
        <Definition
          label="Duration"
          value={
            formatDuration(runDuration(run, now)) + (run.status === "running" ? " so far" : "")
          }
        />
        {run.attempts > 0 && <Definition label="Attempts" value={run.attempts} />}
        {run.iterationPath.length > 0 && (
          <Definition label="Iteration" value={describeIterationPath(run.iterationPath)} />
        )}
        {run.itemCount !== undefined && (
          <Definition
            label="Items"
            value={`${run.itemCount} · ${[...(run.iterations?.values() ?? [])].filter((i) => i.status === "succeeded").length} succeeded`}
          />
        )}
      </DefinitionList>
      {run.error && (
        <div
          className={cn(
            "rounded-md border px-3 py-2 text-xs",
            run.status === "caught"
              ? "border-warning/30 bg-warning-muted text-warning"
              : "border-danger/30 bg-danger-muted text-danger",
          )}
        >
          <p className="font-mono font-semibold wrap-anywhere">{run.error}</p>
          {run.cause && (
            <p className="mt-1 wrap-anywhere whitespace-pre-wrap text-fg-muted">
              {prettyCause(run.cause)}
            </p>
          )}
          {run.status === "caught" && (
            <p className="mt-1 text-fg-muted">Caught — the execution moved on through a Catch.</p>
          )}
        </div>
      )}
      {run.retriedErrors.length > 0 && (
        <div className="rounded-md border border-warning/30 bg-warning-muted px-3 py-2 text-xs text-warning">
          <p className="font-semibold">
            Retried {run.retriedErrors.length} time{run.retriedErrors.length === 1 ? "" : "s"}
          </p>
          <ul className="mt-1 flex flex-col gap-0.5 font-mono text-2xs text-fg-muted">
            {run.retriedErrors.map((e, i) => (
              <li key={i} className="truncate" title={e.cause}>
                {i + 1}. {e.error}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

/** A Lambda's error cause is itself JSON; show it indented rather than as one long line. */
function prettyCause(cause: string): string {
  try {
    const parsed: unknown = JSON.parse(cause)
    if (parsed && typeof parsed === "object") {
      const { errorMessage, errorType } = parsed as { errorMessage?: string; errorType?: string }
      if (errorMessage) return errorType ? `${errorType}: ${errorMessage}` : errorMessage
      return JSON.stringify(parsed, null, 2)
    }
  } catch {
    // Not JSON: show as written.
  }
  return cause
}

function RunEvents({ run, trace }: { run: StateRun | undefined; trace: ExecutionTrace }) {
  if (!run) return <p className="text-xs text-fg-muted">No run selected.</p>
  return (
    <ol className="flex flex-col">
      {run.eventIds.map((id) => {
        const event = trace.eventsById.get(id)
        const at = event?.timestamp instanceof Date ? event.timestamp.getTime() : run.start
        return (
          <li
            key={id}
            className="flex items-center gap-2 border-b border-border/60 py-1.5 last:border-0"
          >
            <span className="w-8 shrink-0 font-mono text-2xs text-fg-subtle tabular-nums">
              #{id}
            </span>
            <EventType type={event?.type} />
            <span className="ml-auto font-mono text-2xs text-fg-subtle tabular-nums">
              +{formatDuration(at - run.start)}
            </span>
          </li>
        )
      })}
    </ol>
  )
}

const DEFINITION_FIELDS_SHOWN_ELSEWHERE = new Set([
  "Type",
  "Comment",
  "Next",
  "End",
  "Branches",
  "ItemProcessor",
  "Iterator",
  "Choices",
  "Default",
])

function StateDefinition({
  model,
  state,
  onSelectState,
}: {
  model: AslModel
  state: AslState
  onSelectState: (name: string) => void
}) {
  const raw = state.raw
  const resource = typeof raw.Resource === "string" ? raw.Resource : undefined
  const retry = Array.isArray(raw.Retry) ? (raw.Retry as Array<Record<string, unknown>>) : []
  const nextTargets = state.transitions.filter((t) => t.kind !== "catch")
  const catches = state.transitions.filter((t) => t.kind === "catch")
  // Show the state without its nested branches — those are states of their own.
  const shown = Object.fromEntries(
    Object.entries(raw).map(([k, v]) =>
      k === "Branches" && Array.isArray(v)
        ? [
            k,
            v.map(
              (_, i) =>
                `… branch ${i + 1} (${model.scopes.get(`${state.name}#${i}`)?.states.length ?? 0} states)`,
            ),
          ]
        : k === "ItemProcessor" || k === "Iterator"
          ? [k, `… ${model.scopes.get(`${state.name}#items`)?.states.length ?? 0} states`]
          : [k, v],
    ),
  )
  const hasExtraFields = Object.keys(raw).some((k) => !DEFINITION_FIELDS_SHOWN_ELSEWHERE.has(k))

  return (
    <>
      {state.comment && <p className="text-xs text-fg-muted italic">{state.comment}</p>}
      <DefinitionList layout="inline">
        {resource && (
          <Definition
            label="Resource"
            value={
              <span title={resource}>
                {(() => {
                  const r = parseTaskResource(resource)
                  return r.service
                    ? `${r.service} · ${r.action}${r.pattern ? ` (${r.pattern === "sync" ? "run a job, wait" : "wait for callback"})` : ""}`
                    : resource
                })()}
              </span>
            }
            copyable={resource}
          />
        )}
        {state.terminal && (
          <Definition
            label="Ends"
            value={
              state.type === "Fail"
                ? "Fails the execution"
                : state.type === "Succeed"
                  ? "Succeeds"
                  : "End of branch"
            }
          />
        )}
        {retry.length > 0 && (
          <Definition
            label="Retry"
            value={
              <ul className="flex flex-col gap-0.5">
                {retry.map((r, i) => (
                  <li key={i}>
                    {(Array.isArray(r.ErrorEquals) ? r.ErrorEquals.join(", ") : "?") +
                      ` — ${String(r.MaxAttempts ?? 3)}× every ${String(r.IntervalSeconds ?? 1)}s` +
                      (r.BackoffRate !== undefined ? ` ×${String(r.BackoffRate)}` : "")}
                  </li>
                ))}
              </ul>
            }
          />
        )}
      </DefinitionList>

      {(nextTargets.length > 0 || catches.length > 0) && (
        <div className="flex flex-col gap-1.5">
          <SectionLabel>Goes to</SectionLabel>
          <ul className="flex flex-col gap-1">
            {[...nextTargets, ...catches].map((t, i) => (
              <li key={i}>
                <button
                  type="button"
                  onClick={() => onSelectState(t.to)}
                  className="flex w-full items-center gap-2 rounded-md border border-border px-2 py-1.5 text-left text-xs transition-colors hover:bg-bg-muted"
                >
                  <ArrowRight
                    className={cn(
                      "h-3.5 w-3.5 shrink-0",
                      t.kind === "catch" ? "text-danger" : "text-fg-subtle",
                    )}
                  />
                  <span className="font-medium text-fg">{t.to}</span>
                  {t.label && (
                    <span
                      className={cn(
                        "ml-auto truncate font-mono text-2xs",
                        t.kind === "catch" ? "text-danger" : "text-fg-muted",
                      )}
                      title={t.label}
                    >
                      {t.kind === "choice" ? `if ${t.label}` : t.label}
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      {hasExtraFields || state.type !== "Pass" ? (
        <JsonPane label="ASL" value={JSON.stringify(shown)} bodyClassName="max-h-96" />
      ) : null}
    </>
  )
}
