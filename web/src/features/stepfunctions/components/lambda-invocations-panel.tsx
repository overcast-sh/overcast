/**
 * The "Lambda" tab of the state inspector: for a Task that called a Lambda
 * function, which invocation each attempt made and everything that invocation
 * wrote — its log lines, its REPORT (duration, memory, cold start), its log
 * stream — with the attempt's error beside them and the event it was handed
 * one click from being replayed in the function's Test tab.
 *
 * The attempt-to-invocation match comes from `lambda-invocations.ts`, which
 * says how sure it is; this panel says so too, rather than presenting a guess
 * as the answer.
 */
import { useMemo, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { ExternalLink, FileText, FlaskConical, RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { CopyButton } from "@/components/ui/copy-button"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { ResourceLink } from "@/components/ui/arn-link"
import { SectionLabel } from "@/components/ui/primitives"
import { LogViewer } from "@/components/logs/log-viewer"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { formatCount, formatDuration, formatQuantity } from "@/lib/format"
import { cn } from "@/lib/utils"
import {
  lambdaFunctionQueryOptions,
  lambdaKeys,
  putTestEventMutationOptions,
} from "@/features/lambda/data"
import { endpointStore } from "@/services/endpoint-store"
import { sfnTaskLogsQueryOptions, TASK_LOG_LIMIT } from "../data"
import type { StateRun, TaskAttempt } from "../execution-trace"
import {
  attemptWindow,
  defaultLogGroup,
  groupInvocations,
  invocationPayloadOf,
  matchAttempt,
  neverRan,
  testEventName,
  type AttemptMatch,
  type InvocationLog,
  type LambdaTarget,
} from "../lambda-invocations"
import { STATUS_THEME } from "../state-theme"
import { ErrorCause } from "./error-cause"

interface Props {
  run: StateRun
  target: LambdaTarget
  /** The state's name, used to name a replayed test event. */
  stateName: string
  now: number
}

export function LambdaInvocationsPanel({ run, target, stateName, now }: Props) {
  const attempts = run.taskAttempts
  const [chosen, setChosen] = useState<number | undefined>()
  const index = chosen ?? attempts.length - 1
  const attempt = attempts.at(index)

  // The function's region is the one its ARN names, or the one the execution
  // recorded calling it in — never simply the console's, which is only
  // whatever default the environment started it with.
  const consoleRegion = endpointStore.get().region
  const region = target.region || consoleRegion
  const fn = useQuery(lambdaFunctionQueryOptions(target.functionName, region))
  const logGroup = fn.data?.LoggingConfig?.LogGroup || defaultLogGroup(target.functionName)

  // One read covers every attempt: from just before the first was scheduled
  // to just after the last settled — or up to now while one is running.
  const live = run.status === "running" && attempts.some((a) => a.status === "running")
  const window = useMemo(() => {
    const first = attempts.at(0)
    const last = attempts.at(-1)
    if (!first || !last) return { startMs: 0, endMs: 0 }
    const startMs = attemptWindow(first, 0).startMs
    return live
      ? { startMs }
      : { startMs, endMs: attemptWindow(last, run.end ?? last.scheduledAt).endMs }
  }, [attempts, live, run.end])
  // Wait for the function's config: a custom LoggingConfig.LogGroup would
  // otherwise be read as the default first, and reported missing.
  const logs = useQuery({
    ...sfnTaskLogsQueryOptions(logGroup, window, region),
    enabled: !fn.isLoading,
  })
  const functionGone = fn.error?.name === "ResourceNotFoundException"
  const invocations = useMemo(() => groupInvocations(logs.data?.events ?? []), [logs.data])

  if (!attempt) return null
  const attemptEnd = attempt.endAt ?? run.end ?? now

  return (
    <div className="flex flex-col gap-3">
      <DefinitionList layout="inline">
        <Definition
          label="Function"
          value={
            <span className="flex flex-wrap items-center gap-x-1.5">
              <ResourceLink service="lambda" resourceId={target.functionName} region={region} />
              {target.qualifier && <span className="text-fg-muted">: {target.qualifier}</span>}
            </span>
          }
          copyable={target.functionName}
        />
        <Definition
          label="Log group"
          value={<ResourceLink service="logs" resourceId={logGroup} region={region} />}
          copyable={logGroup}
        />
        {region !== consoleRegion && <Definition label="Region" value={region} />}
        {target.via === "lambda:invoke" && target.invocationType === "Event" && (
          <Definition label="Invocation" value="Asynchronous (Event)" />
        )}
      </DefinitionList>

      {fn.isError && (
        <Notice tone="warning">
          {functionGone
            ? `${target.functionName} no longer exists in ${region}, so its logs are read from the default log group.`
            : `Could not read ${target.functionName}'s configuration (${fn.error.message}), so its logs are read from the default log group.`}
        </Notice>
      )}

      {attempts.length > 1 && (
        <AttemptPicker attempts={attempts} selected={index} now={now} onSelect={setChosen} />
      )}

      <AttemptDetail
        key={attempt.scheduledEventId}
        attempt={attempt}
        attemptNumber={index + 1}
        // The run's own error is already shown above the tabs; repeat an
        // attempt's only when it is a different one — a failure a Retry absorbed.
        showError={attempt.error !== run.error || attempt.cause !== run.cause}
        target={target}
        stateName={stateName}
        logGroup={logGroup}
        region={region}
        attemptEnd={attemptEnd}
        invocations={invocations}
        logsState={{
          loading: logs.isLoading,
          fetching: logs.isFetching,
          error: logs.error,
          truncated: Boolean(logs.data?.nextToken),
          allEvents: logs.data?.events ?? [],
          refetch: () => void logs.refetch(),
        }}
      />
    </div>
  )
}

// ─── Attempts ────────────────────────────────────────────────────────────────

const ATTEMPT_THEME: Record<TaskAttempt["status"], { label: string; color: string }> = {
  running: { label: "Running", color: STATUS_THEME.running.color },
  succeeded: { label: "Succeeded", color: STATUS_THEME.succeeded.color },
  failed: { label: "Failed", color: STATUS_THEME.failed.color },
  timedOut: { label: "Timed out", color: STATUS_THEME.failed.color },
  aborted: { label: "Aborted", color: STATUS_THEME.aborted.color },
}

function AttemptPicker({
  attempts,
  selected,
  now,
  onSelect,
}: {
  attempts: TaskAttempt[]
  selected: number
  now: number
  onSelect: (index: number) => void
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <SectionLabel>{attempts.length} attempts</SectionLabel>
      <ul className="flex max-h-36 flex-col gap-px overflow-y-auto rounded-md border border-border bg-bg-muted p-1">
        {attempts.map((a, i) => (
          <li key={a.scheduledEventId}>
            <button
              type="button"
              onClick={() => onSelect(i)}
              aria-current={selected === i}
              className={cn(
                "flex w-full items-center gap-2 rounded px-2 py-1 text-left font-mono text-2xs transition-colors",
                selected === i
                  ? "bg-bg-elevated text-fg shadow-xs"
                  : "text-fg-muted hover:bg-bg-elevated/60",
              )}
            >
              <span
                className="h-2 w-2 shrink-0 rounded-full"
                style={{ background: ATTEMPT_THEME[a.status].color }}
              />
              <span className="w-16 shrink-0 text-fg-subtle">Attempt {i + 1}</span>
              <span className="min-w-0 flex-1 truncate">
                {a.error ?? ATTEMPT_THEME[a.status].label}
              </span>
              <span className="shrink-0 tabular-nums">
                {formatDuration((a.endAt ?? now) - a.scheduledAt)}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

interface LogsState {
  loading: boolean
  fetching: boolean
  error: Error | null
  truncated: boolean
  allEvents: Array<{ timestamp?: number; message?: string; logStreamName?: string }>
  refetch: () => void
}

function AttemptDetail({
  attempt,
  attemptNumber,
  showError,
  target,
  stateName,
  logGroup,
  region,
  attemptEnd,
  invocations,
  logsState,
}: {
  attempt: TaskAttempt
  attemptNumber: number
  showError: boolean
  target: LambdaTarget
  stateName: string
  logGroup: string
  region: string
  attemptEnd: number
  invocations: InvocationLog[]
  logsState: LogsState
}) {
  const match = useMemo(
    () => matchAttempt(attempt, invocations, attemptEnd),
    [attempt, invocations, attemptEnd],
  )
  const [picked, setPicked] = useState<string | undefined>()
  const invocation =
    match.candidates.find((c) => c.requestId === picked) ?? match.invocation ?? undefined
  const [showWindow, setShowWindow] = useState(false)
  const payload = invocationPayloadOf(attempt, target)

  const failed = attempt.status === "failed" || attempt.status === "timedOut"

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        <span className="flex items-center gap-1.5 font-medium text-fg">
          <span
            className="h-2 w-2 rounded-full"
            style={{ background: ATTEMPT_THEME[attempt.status].color }}
          />
          Attempt {attemptNumber} · {ATTEMPT_THEME[attempt.status].label}
        </span>
        <span className="font-mono text-fg-muted tabular-nums">
          {formatDuration(attemptEnd - attempt.scheduledAt)}
        </span>
        <span className="ml-auto flex items-center gap-1">
          <CopyButton value={payload} noun="event payload" />
          <ReplayButton
            functionName={target.functionName}
            region={region}
            stateName={stateName}
            attemptNumber={attemptNumber}
            payload={payload}
          />
        </span>
      </div>

      {failed && showError && <ErrorCause error={attempt.error} cause={attempt.cause} />}

      {invocation && (
        <InvocationFacts
          invocation={invocation}
          match={match}
          logGroup={logGroup}
          region={region}
        />
      )}

      {match.quality === "best-guess" && (
        <CandidatePicker
          candidates={match.candidates}
          selected={invocation?.requestId}
          onSelect={setPicked}
        />
      )}

      <div className="flex flex-col gap-1.5">
        <div className="flex items-center gap-2">
          <SectionLabel>
            {showWindow || !invocation ? "Logs during this attempt" : "Invocation logs"}
          </SectionLabel>
          <span className="ml-auto flex items-center gap-1">
            {invocation && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setShowWindow((v) => !v)}
                aria-pressed={showWindow}
              >
                {showWindow ? "This invocation only" : "Everything in this window"}
              </Button>
            )}
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label="Refresh logs"
              onClick={logsState.refetch}
              disabled={logsState.fetching}
            >
              <RefreshCw className={cn("h-3 w-3", logsState.fetching && "animate-spin")} />
            </Button>
          </span>
        </div>
        <LogsBody
          attempt={attempt}
          target={target}
          logGroup={logGroup}
          invocation={showWindow ? undefined : invocation}
          logsState={logsState}
        />
      </div>
    </div>
  )
}

const MATCH_NOTES: Record<AttemptMatch["quality"], string | undefined> = {
  "request-id": "Matched by the request id Lambda returned.",
  "error-message": "The invocation that logged this attempt's error.",
  "only-one": "The only invocation of this function during the attempt.",
  "best-guess":
    "Several invocations of this function ran during this attempt — this is the closest by timing and outcome.",
  none: undefined,
}

function InvocationFacts({
  invocation,
  match,
  logGroup,
  region,
}: {
  invocation: InvocationLog
  match: AttemptMatch
  logGroup: string
  region: string
}) {
  const r = invocation.report
  const memoryTight =
    r?.maxMemoryUsedMb !== undefined &&
    r.memorySizeMb !== undefined &&
    r.maxMemoryUsedMb >= r.memorySizeMb * 0.9
  return (
    <div className="flex flex-col gap-2 rounded-md border border-border bg-bg-muted/50 px-3 py-2">
      <DefinitionList layout="inline">
        <Definition label="Request ID" value={invocation.requestId} copyable />
        {invocation.logStreamName && (
          <Definition
            label="Log stream"
            value={
              <Link
                to="/cloudwatch/logs/stream"
                search={{
                  groupName: logGroup,
                  streamName: invocation.logStreamName,
                  region,
                  ...(invocation.start !== undefined ? { anchorTs: invocation.start } : {}),
                }}
                className="inline-flex items-center gap-1 text-accent hover:underline"
                title="Open the stream at this invocation"
              >
                <span className="wrap-anywhere">{invocation.logStreamName}</span>
                <ExternalLink className="h-3 w-3 shrink-0" />
              </Link>
            }
            copyable={invocation.logStreamName}
          />
        )}
        {r?.durationMs !== undefined && (
          <Definition
            label="Duration"
            value={`${formatDuration(r.durationMs)}${r.billedDurationMs !== undefined ? ` · billed ${formatDuration(r.billedDurationMs)}` : ""}`}
          />
        )}
        {r?.maxMemoryUsedMb !== undefined && (
          <Definition
            label="Memory"
            value={
              <span className={cn(memoryTight && "text-warning")}>
                {r.maxMemoryUsedMb} of {r.memorySizeMb ?? "?"} MB used
                {memoryTight && " — close to the limit"}
              </span>
            }
          />
        )}
        <Definition
          label="Start"
          value={
            r?.initDurationMs !== undefined
              ? `Cold — init ${formatDuration(r.initDurationMs)}`
              : r
                ? "Warm"
                : "—"
          }
        />
        {r?.status && (
          <Definition
            label="Ended by"
            value={
              <span className="text-danger">{r.status === "timeout" ? "Timeout" : r.status}</span>
            }
          />
        )}
        {!r && (
          <Definition label="Report" value="No REPORT line yet — still running, or it crashed." />
        )}
      </DefinitionList>
      {MATCH_NOTES[match.quality] && (
        <p className="text-2xs text-fg-subtle">{MATCH_NOTES[match.quality]}</p>
      )}
    </div>
  )
}

function CandidatePicker({
  candidates,
  selected,
  onSelect,
}: {
  candidates: InvocationLog[]
  selected: string | undefined
  onSelect: (requestId: string) => void
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <SectionLabel>{candidates.length} invocations overlapped this attempt</SectionLabel>
      <ul className="flex max-h-32 flex-col gap-px overflow-y-auto rounded-md border border-border bg-bg-muted p-1">
        {candidates.map((c) => {
          const errored = c.report?.status !== undefined
          return (
            <li key={c.requestId}>
              <button
                type="button"
                onClick={() => onSelect(c.requestId)}
                aria-current={selected === c.requestId}
                className={cn(
                  "flex w-full items-center gap-2 rounded px-2 py-1 text-left font-mono text-2xs transition-colors",
                  selected === c.requestId
                    ? "bg-bg-elevated text-fg shadow-xs"
                    : "text-fg-muted hover:bg-bg-elevated/60",
                )}
              >
                <span className="min-w-0 flex-1 truncate">{c.requestId}</span>
                {errored && <span className="shrink-0 text-danger">{c.report?.status}</span>}
                <span className="shrink-0 tabular-nums">
                  {formatDuration(c.report?.durationMs)}
                </span>
              </button>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

function LogsBody({
  attempt,
  target,
  logGroup,
  invocation,
  logsState,
}: {
  attempt: TaskAttempt
  target: LambdaTarget
  logGroup: string
  invocation: InvocationLog | undefined
  logsState: LogsState
}) {
  if (neverRan(attempt.error)) {
    return (
      <Notice>
        Lambda turned this call away before the function ran, so the attempt wrote no logs.
      </Notice>
    )
  }
  if (isMissingGroup(logsState.error)) {
    return (
      <Notice>
        There is no log group <span className="font-mono">{logGroup}</span> yet —{" "}
        {target.functionName} has never written a log line here.
      </Notice>
    )
  }

  const windowEvents = invocation ? invocation.lines : windowSlice(logsState.allEvents, attempt)
  const empty = logsState.loading
    ? undefined
    : attempt.status === "running"
      ? "Waiting for the invocation to write its first line…"
      : target.invocationType === "Event"
        ? "An asynchronous invoke returns as soon as Lambda queues the event, so the function's logs come after this attempt ended. Open the log group to find them."
        : "No log lines from this function during the attempt. CloudWatch Logs can lag a moment behind — refresh to check again."

  return (
    <div className="flex flex-col gap-1">
      <div className="flex h-72 flex-col rounded-md border border-border bg-bg-muted p-2">
        <LogViewer
          events={windowEvents}
          loading={logsState.loading}
          error={logsState.error ? logsState.error.message : null}
          emptyMessage={empty}
          defaultMode="plain"
          follow={attempt.status === "running"}
          className="min-h-0 flex-1"
        />
      </div>
      <p className="flex items-center gap-2 text-2xs text-fg-subtle">
        <FileText className="h-3 w-3" />
        {formatQuantity(windowEvents.length, "line")}
        {logsState.truncated &&
          ` · the window held more than ${formatCount(TASK_LOG_LIMIT)} events; open the log group for the rest`}
      </p>
    </div>
  )
}

/** The events of the whole read that fall inside one attempt's own window. */
function windowSlice(events: LogsState["allEvents"], attempt: TaskAttempt): LogsState["allEvents"] {
  const { startMs, endMs } = attemptWindow(attempt, Number.POSITIVE_INFINITY)
  return events.filter((e) => (e.timestamp ?? 0) >= startMs && (e.timestamp ?? 0) <= endMs)
}

function isMissingGroup(error: Error | null): boolean {
  return error?.name === "ResourceNotFoundException"
}

function Notice({
  children,
  tone = "muted",
}: {
  children: React.ReactNode
  tone?: "muted" | "warning"
}) {
  return (
    <p
      className={cn(
        "rounded-md border px-3 py-2 text-xs",
        tone === "warning"
          ? "border-warning/30 bg-warning-muted text-warning"
          : "border-dashed border-border text-fg-muted",
      )}
    >
      {children}
    </p>
  )
}

/**
 * Saves the attempt's event as a test event on the function and opens its
 * Test tab — the shortest path from "this failed" to stepping through it.
 */
function ReplayButton({
  functionName,
  region,
  stateName,
  attemptNumber,
  payload,
}: {
  functionName: string
  /** The function's region, which the Task named and the console may not be showing. */
  region: string
  stateName: string
  attemptNumber: number
  payload: string
}) {
  const navigate = useNavigate()
  const eventName = testEventName(stateName, attemptNumber)
  const save = useResourceMutation({
    options: putTestEventMutationOptions(),
    invalidateKeys: [lambdaKeys.testEvents(functionName)],
    successTitle: "Saved as a test event",
    successDescription: () => `"${eventName}" is ready in ${functionName}'s Test tab.`,
    errorTitle: "Could not save the test event",
    onSuccess: () =>
      void navigate({
        to: "/lambda/$name",
        params: { name: functionName },
        search: { region },
        hash: "test",
      }),
  })
  return (
    <Button
      size="sm"
      variant="ghost"
      busy={save.isPending}
      title="Save this attempt's event as a test event and open the function's Test tab"
      onClick={() => save.mutate({ functionName, eventName, body: payload, region })}
    >
      <FlaskConical className="mr-1.5 h-3.5 w-3.5" />
      Replay in Lambda
    </Button>
  )
}
