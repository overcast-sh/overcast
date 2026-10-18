import { useState, type FormEvent } from "react"
import { Send, Snowflake } from "lucide-react"
import { Advisory } from "@/components/ui/advisory"
import { Badge, type BadgeProps } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { FormField, FormRow } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { Select } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { topologyKey } from "@/features/map/use-topology"
import { formatBytes } from "@/lib/format"
import type { EndpointResponse } from "@/services/api/lambda-web"
import { invokeEndpointMutationOptions } from "../data"
import {
  describeBody,
  describeErrorType,
  invokePath,
  methodHasBody,
  TEST_METHODS,
} from "../endpoint-access"
import type { EnvEntry } from "../models"
import { KeyValueFields } from "./key-value-fields"

interface TestRequestPanelProps {
  /** The endpoint's domain name: it names the endpoint on the invoke path. */
  domainName: string
  /** The Region to fall back to when the domain name carries none. */
  region: string
  /** `IamAuth` endpoints want a SigV4 signature, which a browser cannot make. */
  authType: string | undefined
  /**
   * How many environments the endpoint has right now, when known. A request
   * that finds none is expected to be a cold start.
   */
  environments: number | undefined
}

/** A response, and whether the request likely started an environment. */
interface Outcome {
  response: EndpointResponse
  /** The endpoint showed 0 environments when the request was sent. */
  likelyColdStart: boolean
}

function statusVariant(status: number): NonNullable<BadgeProps["variant"]> {
  if (status < 300) return "success"
  if (status < 400) return "info"
  if (status < 500) return "warning"
  return "danger"
}

/**
 * Sends one request to an endpoint, from the browser, and shows what came back.
 *
 * The request goes through the invoke path (`/_overcast/lambdaweb/invoke/…`)
 * because a browser cannot set `Host`: the server forwards the endpoint's own
 * domain to the application as `Host` itself. An `IamAuth` endpoint is out of
 * reach — it wants a SigV4 signature — so the form is off, with the reason.
 */
export function TestRequestPanel({
  domainName,
  region,
  authType,
  environments,
}: TestRequestPanelProps) {
  const [method, setMethod] = useState<string>("GET")
  const [target, setTarget] = useState("/")
  const [headers, setHeaders] = useState<EnvEntry[]>([])
  const [body, setBody] = useState("")
  const [outcome, setOutcome] = useState<Outcome>()
  // A request can start an environment; the map's count is re-read on the event, and here too.
  const invoke = useResourceMutation({
    options: invokeEndpointMutationOptions(),
    invalidateKeys: [topologyKey],
    errorToast: false,
  })
  const path = target.startsWith("/") ? target : `/${target}`

  if (authType === "IamAuth") {
    return (
      <section aria-label="Test request" className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">Test request</h3>
        <Advisory tone="info" title="IAM auth endpoints cannot be tested from the console">
          They only accept requests signed with SigV4, and a browser cannot sign one. Use the{" "}
          <span className="font-mono">curl --aws-sigv4</span> example above.
        </Advisory>
      </section>
    )
  }

  const send = (event: FormEvent) => {
    event.preventDefault()
    const likelyColdStart = environments === 0
    invoke.mutate(
      {
        invokePath: invokePath(domainName, region),
        method,
        target: path,
        headers: Object.fromEntries(
          headers.filter((h) => h.key.trim() !== "").map((h) => [h.key.trim(), h.value]),
        ),
        body: methodHasBody(method) ? body : undefined,
      },
      { onSuccess: (response) => setOutcome({ response, likelyColdStart }) },
    )
  }

  return (
    <section aria-label="Test request" className="flex flex-col gap-3">
      <h3 className="text-sm font-medium">Test request</h3>
      <form onSubmit={send} className="flex flex-col gap-3">
        <FormRow>
          <FormField label="Method" className="w-32 shrink-0">
            <Select value={method} onChange={(event) => setMethod(event.target.value)}>
              {TEST_METHODS.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </Select>
          </FormField>
          <FormField label="Path and query">
            <Input
              value={target}
              placeholder="/hello?name=overcast"
              className="font-mono"
              onChange={(event) => setTarget(event.target.value)}
            />
          </FormField>
        </FormRow>
        <FormField
          label="Headers"
          hint="A browser sets Host, Origin and a few others itself; the endpoint's own domain is forwarded as Host."
        >
          <KeyValueFields
            entries={headers}
            onChange={setHeaders}
            noun="request header"
            addLabel="Add header"
          />
        </FormField>
        {methodHasBody(method) && (
          <FormField label="Body">
            <Textarea
              value={body}
              rows={4}
              placeholder={`{"hello":"world"}`}
              onChange={(event) => setBody(event.target.value)}
            />
          </FormField>
        )}
        <div className="flex items-center gap-3">
          <Button type="submit" size="sm" busy={invoke.isPending}>
            <Send className="size-3.5" />
            {invoke.isPending ? "Sending" : "Send request"}
          </Button>
          {environments === 0 && (
            <span className="inline-flex items-center gap-1 text-xs text-fg-muted">
              <Snowflake aria-hidden className="size-3" />
              Scaled to zero: this request starts an environment.
            </span>
          )}
        </div>
      </form>
      {invoke.error && (
        <p role="alert" className="text-xs text-danger">
          The request did not reach the emulator: {invoke.error.message}
        </p>
      )}
      {outcome && <TestResponse outcome={outcome} />}
    </section>
  )
}

function TestResponse({ outcome: { response, likelyColdStart } }: { outcome: Outcome }) {
  const header = (name: string) => response.headers.find(([n]) => n === name)?.[1]
  const requestId = header("lambda-web-request-id")
  const logStream = header("lambda-web-logstream")
  const errorType = header("lambda-web-error-type")
  const contentType = header("content-type")
  const body = describeBody(response.body, contentType ?? null)
  return (
    <div
      role="group"
      aria-label="Response"
      className="flex flex-col gap-3 rounded-md border border-border p-3"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <Badge variant={statusVariant(response.status)}>
          {response.status} {response.statusText}
        </Badge>
        <span className="font-mono text-xs tabular-nums">{Math.round(response.durationMs)} ms</span>
        {likelyColdStart && (
          <span
            className="inline-flex items-center gap-1 text-xs text-accent"
            title="The endpoint had no environments when the request was sent, so this one waited for a container to start."
          >
            <Snowflake aria-hidden className="size-3" />
            Likely a cold start
          </span>
        )}
      </div>
      {errorType && (
        <p className="text-xs">
          <span className="font-mono text-danger">{errorType}</span>
          {describeErrorType(errorType) && (
            <span className="text-fg-muted"> — {describeErrorType(errorType)}</span>
          )}
        </p>
      )}
      <DefinitionList columns={1} layout="inline">
        <Definition label="lambda-web-request-id" value={requestId} />
        <Definition label="lambda-web-logstream" value={logStream} />
        <Definition label="content-type" value={contentType} />
      </DefinitionList>
      {requestId === undefined && (
        <p className="text-xs text-fg-muted">
          This browser cannot read the <span className="font-mono">lambda-web-*</span> response
          headers across origins, so they are not shown. The status, body and timing are real.
        </p>
      )}
      <div className="flex flex-col gap-1">
        <span className="text-xs text-fg-subtle">
          Body ·{" "}
          {body.kind === "empty"
            ? "empty"
            : `${body.kind === "binary" ? "binary, " : ""}${formatBytes(body.size)}`}
        </span>
        {(body.kind === "json" || body.kind === "text") && (
          <pre className="max-h-64 overflow-auto rounded-md bg-bg-muted p-2 font-mono text-xs break-all whitespace-pre-wrap">
            {body.text}
          </pre>
        )}
      </div>
    </div>
  )
}
