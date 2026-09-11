import { useState, useCallback, useMemo } from "react"
import { Link } from "@tanstack/react-router"
import { useQuery } from "@tanstack/react-query"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Spinner } from "@/components/ui/primitives"
import { Input } from "@/components/ui/input"
import { JsonEditor } from "@/components/ui/json-editor"
import {
  testEventsQueryOptions,
  putTestEventMutationOptions,
  deleteTestEventMutationOptions,
  lambdaKeys,
} from "@/features/lambda/data"
import { eventTemplates, templateCategories } from "@/features/lambda/event-templates"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import {
  DEFAULT_TEST_EVENT,
  useLambdaInvoke,
  type LambdaInvoke,
} from "@/features/lambda/use-invoke"
import { decodeBase64Text } from "@/lib/base64"
import { summarisePlatformRecords } from "@/lib/log-format"
import { fieldLabel, sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { InvokeDebugHint } from "@/features/debugger/components/invoke-debug-hint"
import { useOptionalDebugSession } from "@/features/debugger/session/hooks"

export function TestTab({
  name,
  timeoutSeconds,
  invoke: lifted,
}: {
  name: string
  timeoutSeconds?: number
  /**
   * The invoke held by the page, so its event and result survive the tab
   * unmounting (a debug pause switches to the Code tab). A caller without
   * one — the invoke dialog — gets a tab-local invoke instead.
   */
  invoke?: LambdaInvoke
}) {
  const own = useLambdaInvoke(name)
  const invoke = lifted ?? own
  const { payload: eventPayload, setPayload: setEventPayload, result } = invoke
  const { isPending, progressStep, error: invokeError } = invoke

  // Event state
  const jsonError = useMemo(() => validateJson(eventPayload), [eventPayload])
  const [eventName, setEventName] = useState("")
  const [selectedSavedEvent, setSelectedSavedEvent] = useState<string | null>(null)

  // Saved events query
  const { data: savedEvents = [] } = useQuery(testEventsQueryOptions(name))

  // A console debug session waiting for a container is woken by the invoke
  // that starts one; absent on pages without the provider.
  const debugSession = useOptionalDebugSession()

  const { mutate: saveEvent, isPending: isSaving } = useResourceMutation({
    options: putTestEventMutationOptions(),
    invalidateKeys: [lambdaKeys.testEvents(name)],
    successTitle: "Saved",
    successDescription: () => `Test event "${eventName}" saved.`,
    errorTitle: "Save failed",
  })

  const { mutate: deleteEvt } = useResourceMutation({
    options: deleteTestEventMutationOptions(),
    invalidateKeys: [lambdaKeys.testEvents(name)],
    successTitle: "Deleted",
    successDescription: (vars) => `Test event "${vars.eventName}" deleted.`,
    onSuccess: (_, vars) => {
      if (selectedSavedEvent === vars.eventName) {
        setSelectedSavedEvent(null)
      }
    },
  })

  const handlePayloadChange = setEventPayload

  const handleInvoke = useCallback(() => {
    if (jsonError || isPending) return
    debugSession?.invokeStarted()
    void invoke.run()
  }, [jsonError, isPending, debugSession, invoke])

  const handleSave = useCallback(() => {
    if (!eventName.trim() || jsonError) return
    saveEvent({ functionName: name, eventName: eventName.trim(), body: eventPayload })
  }, [name, eventName, eventPayload, jsonError, saveEvent])

  const handleSelectSavedEvent = useCallback(
    (evtName: string) => {
      const evt = savedEvents.find((e) => e.name === evtName)
      if (evt) {
        setSelectedSavedEvent(evtName)
        setEventName(evtName)
        setEventPayload(evt.body)
      }
    },
    [savedEvents, setEventPayload],
  )

  const handleSelectTemplate = useCallback(
    (templateName: string) => {
      const tpl = eventTemplates.find((t) => t.name === templateName)
      if (tpl) {
        setEventPayload(tpl.body)
        setEventName("")
        setSelectedSavedEvent(null)
      }
    },
    [setEventPayload],
  )

  const handleNewEvent = useCallback(() => {
    setSelectedSavedEvent(null)
    setEventName("")
    setEventPayload(DEFAULT_TEST_EVENT)
  }, [setEventPayload])

  let parsedPayload: string | undefined
  if (result?.payload) {
    try {
      parsedPayload = JSON.stringify(JSON.parse(result.payload), null, 2)
    } catch {
      parsedPayload = result.payload
    }
  }

  // A log tail we cannot decode costs the log output and nothing else — the
  // status, the badge and the response payload are all still worth showing.
  // Under the JSON log format the START / END / REPORT lines arrive as
  // Telemetry-API-shaped records instead; each reads as the line it replaced.
  const decodedLog = result?.logResult ? decodeBase64Text(result.logResult) : null
  const logOutput = decodedLog === null ? null : summarisePlatformRecords(decodedLog)

  return (
    <div className="flex gap-6">
      {/* ── Left sidebar: templates + saved events ── */}
      <div className="flex w-56 shrink-0 flex-col gap-4">
        {/* Saved events */}
        <div className="flex flex-col gap-1">
          <div className="flex items-center justify-between">
            <span className={cn(sectionLabel, "text-fg-muted")}>Saved events</span>
            <button
              onClick={handleNewEvent}
              className="text-xs text-accent hover:underline"
              title="New blank event"
            >
              + New
            </button>
          </div>
          {savedEvents.length === 0 && (
            <p className="py-2 text-xs text-fg-muted italic">No saved events yet</p>
          )}
          {savedEvents.map((evt) => (
            <div key={evt.name} className="group flex items-center gap-1">
              <button
                onClick={() => handleSelectSavedEvent(evt.name)}
                className={cn(
                  "flex-1 truncate rounded px-2 py-1 text-left text-xs transition-colors",
                  selectedSavedEvent === evt.name
                    ? "bg-accent-muted font-medium text-accent"
                    : "text-fg hover:bg-bg-muted",
                )}
              >
                {evt.name}
              </button>
              <button
                onClick={() => deleteEvt({ functionName: name, eventName: evt.name })}
                className="hidden shrink-0 rounded p-0.5 text-fg-muted group-hover:block hover:text-danger"
                title="Delete"
              >
                <svg
                  className="h-3 w-3"
                  viewBox="0 0 12 12"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.5"
                >
                  <path d="M3 3l6 6M9 3l-6 6" />
                </svg>
              </button>
            </div>
          ))}
        </div>

        {/* Event templates */}
        <div className="flex flex-col gap-1">
          <span className={cn(sectionLabel, "text-fg-muted")}>Event templates</span>
          {templateCategories.map((cat) => (
            <div key={cat} className="flex flex-col">
              <span className="mt-1 text-xs font-medium text-fg-muted">{cat}</span>
              {eventTemplates
                .filter((t) => t.category === cat)
                .map((tpl) => (
                  <button
                    key={tpl.name}
                    onClick={() => handleSelectTemplate(tpl.name)}
                    className="truncate rounded px-2 py-1 text-left text-xs text-fg transition-colors hover:bg-bg-muted"
                  >
                    {tpl.name}
                  </button>
                ))}
            </div>
          ))}
        </div>
      </div>

      {/* ── Main content ── */}
      <div className="flex min-w-0 flex-1 flex-col gap-4">
        {/* What the debugger does to the timeout — nothing while it is off. */}
        <InvokeDebugHint service="lambda" resource={name} timeoutSeconds={timeoutSeconds} />

        {/* Event header: name + actions */}
        <div className="flex items-end gap-3">
          <div className="flex flex-1 flex-col gap-1">
            <label htmlFor="event-name" className={cn(fieldLabel, "text-fg-muted")}>
              Event name
            </label>
            <Input
              id="event-name"
              value={eventName}
              onChange={(e) => setEventName(e.target.value)}
              placeholder="my-test-event"
              className="max-w-xs"
            />
          </div>
          <div className="flex gap-2">
            <Button
              variant="secondary"
              size="md"
              onClick={handleSave}
              disabled={!eventName.trim() || !!jsonError || isSaving}
            >
              {isSaving ? <Spinner className="mr-2 h-3.5 w-3.5" /> : null}
              Save
            </Button>
            <Button onClick={handleInvoke} disabled={isPending || !!jsonError} size="md">
              {isPending ? <Spinner className="mr-2 h-3.5 w-3.5" /> : null}
              Test
            </Button>
          </div>
        </div>

        {/* Event JSON editor */}
        <JsonEditor
          value={eventPayload}
          onChange={handlePayloadChange}
          error={jsonError}
          placeholder='{ "key": "value" }'
          minHeight={200}
        />

        {/* Invocation status alert */}
        {isPending && (
          <div className="flex items-start gap-3 rounded-lg border border-border bg-bg-muted p-4">
            <Spinner className="mt-0.5 h-4 w-4 shrink-0 text-accent" />
            <div>
              <p className="text-sm font-medium text-fg">Executing function&hellip;</p>
              <p className="mt-0.5 text-xs text-fg-muted">
                {progressStep ?? "Waiting for the Lambda runtime to process the event."}
              </p>
            </div>
          </div>
        )}

        {!isPending && invokeError && (
          <div className="flex flex-col gap-2 rounded-lg border border-danger/30 bg-danger-muted p-4">
            <p className="font-mono text-sm font-medium text-danger">Invocation failed</p>
            <pre
              tabIndex={0}
              className="max-h-48 overflow-auto rounded-md border border-danger/20 bg-bg-elevated p-3 font-mono text-xs text-fg"
            >
              {invokeError}
            </pre>
          </div>
        )}

        {!isPending && result && (
          <div
            className={cn(
              "flex flex-col gap-3 rounded-lg border p-4",
              result.functionError
                ? "border-danger/30 bg-danger-muted"
                : "border-success/30 bg-success-muted",
            )}
          >
            <div className="flex items-center gap-2">
              <h3
                className={cn(
                  "font-mono text-sm font-medium",
                  result.functionError ? "text-danger" : "text-success",
                )}
              >
                {result.functionError ? "Execution failed" : "Execution succeeded"}
              </h3>
              <Badge variant={result.functionError ? "danger" : "success"}>
                {result.functionError
                  ? `Error: ${result.functionError}`
                  : `Status: ${result.statusCode}`}
              </Badge>
            </div>

            <div className="flex flex-col gap-1">
              <span className="font-mono text-xs font-medium text-fg-muted">Response</span>
              <pre
                tabIndex={0}
                className="max-h-64 overflow-auto rounded-md border border-border bg-bg-elevated p-3 font-mono text-xs text-fg"
              >
                {parsedPayload ?? "null"}
              </pre>
            </div>

            {result.logResult && (
              <div className="flex flex-col gap-1">
                <div className="flex items-center justify-between">
                  <span className="font-mono text-xs font-medium text-fg-muted">Log output</span>
                  {result.logGroupName && result.logStreamName && (
                    <Link
                      to="/cloudwatch/logs/stream"
                      search={{
                        groupName: result.logGroupName,
                        streamName: result.logStreamName,
                      }}
                      className="text-xs text-accent hover:underline"
                    >
                      View stream →
                    </Link>
                  )}
                </div>
                <pre
                  className={cn(
                    "max-h-48 overflow-auto rounded-md border border-border bg-bg-elevated p-3 font-mono text-xs",
                    logOutput === null ? "text-fg-muted italic" : "text-fg",
                  )}
                >
                  {logOutput ?? "Log output unavailable — the log tail was not valid base64."}
                </pre>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

/** The parse error for an event that is not JSON; an empty event is allowed. */
function validateJson(text: string): string | null {
  try {
    if (text.trim()) JSON.parse(text)
    return null
  } catch (e) {
    return (e as SyntaxError).message
  }
}
