/**
 * The compute debugger's one console component (docs/plans/compute-debugger.md
 * § 7): a dumb renderer of a single `DebuggerTarget` descriptor. Every word
 * that depends on a protocol — the editor bodies, the tag commands, the
 * reason a target is off — is rendered by the server and shown verbatim, so
 * nothing here knows what "inspector" or "jdwp" means. Phase 2 (§ 11) grows
 * an in-console session next to this panel, not inside it.
 *
 * `DebugPanel` takes the descriptor; `DebugTargetPanel` fetches it for a
 * `service/resource[/container]` and owns the loading, "off" and error
 * states around it. The editor tab strip is `EditorTabs`; the two hooks the
 * live line needs are in `../hooks`.
 */
import { useId } from "react"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { Code, SectionLabel } from "@/components/ui/primitives"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { SkeletonRows } from "@/components/ui/skeleton"
import { formatAge, formatDate } from "@/lib/format"
import { cn } from "@/lib/utils"
import type { DebuggerSetup, DebuggerTarget } from "@/types"
import { useDebugTarget, useNow, useReplacedContainer } from "../hooks"
import { ATTACHED_STATES, listenAddress, shortContainerId } from "../target"
import { DebugStateBadge } from "./debug-state-badge"
import { EditorTabs } from "./editor-tabs"

// ─── Blocks ───────────────────────────────────────────────────────────────

function StateLine({ target }: { target: DebuggerTarget }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <DebugStateBadge state={target.state} />
      {target.reason && (
        <span className={cn("text-sm", target.state === "error" ? "text-danger" : "text-fg-muted")}>
          {target.reason}
        </span>
      )}
    </div>
  )
}

/**
 * How to turn the debugger on, as three things to paste. The strings come
 * from the server with the real ARN filled in; the console never spells a
 * tag itself.
 */
function SetupBlock({ setup, service }: { setup: DebuggerSetup; service: string }) {
  const noun = service === "ecs" ? "task definition" : "function"
  // One block per untagged container on an ECS task page, so the heading id
  // is minted per instance rather than shared.
  const headingId = useId()
  return (
    <Card>
      <CardContent role="region" aria-labelledby={headingId} className="flex flex-col gap-3">
        <SectionLabel as="h3" id={headingId}>
          Turn it on
        </SectionLabel>
        <p className="text-sm text-fg-muted">
          Start Overcast with the flag and tag the {noun}. The next cold start listens for your
          editor.
        </p>
        <DefinitionList layout="inline">
          <Definition label="Flag" value={setup.flag} copyable />
          <Definition label="CLI tag" value={setup.tagCli} copyable />
          <Definition label="CDK line" value={setup.tagCdk} copyable />
        </DefinitionList>
      </CardContent>
    </Card>
  )
}

function LocalRootHint() {
  return (
    <>
      Unknown — tag the resource with{" "}
      <Code className="whitespace-nowrap">overcast:source-path</Code> (or{" "}
      <Code className="whitespace-nowrap">overcast:hot-reload-path</Code>) and the editor snippets
      below fill it in.
    </>
  )
}

/** What an editor attaches to, as a definition grid — one resource's fields, not a list. */
function ResolvedTarget({ target }: { target: DebuggerTarget }) {
  return (
    <DefinitionList columns={3} className="gap-y-2">
      <Definition
        label="Protocol"
        value={
          target.protocol ? (
            <span className="inline-flex items-center gap-1.5">
              {target.protocol}
              {target.protocolSource && (
                <Badge variant="outline" title="Where the protocol came from">
                  {target.protocolSource}
                </Badge>
              )}
            </span>
          ) : null
        }
      />
      <Definition label="Listen" value={listenAddress(target)} copyable />
      <Definition
        label="Container"
        value={target.containerId ? shortContainerId(target.containerId) : null}
        copyable={target.containerId || undefined}
      />
      <Definition label="Remote root" value={target.remoteRoot} />
      <Definition
        label="Local root"
        value={target.localRoot || <LocalRootHint />}
        variant={target.localRoot ? undefined : "prose"}
      />
      <Definition label="Timeout policy" value={target.timeoutPolicy} />
    </DefinitionList>
  )
}

/** A relative time that ticks, with the absolute one on hover. */
function Since({ at, now }: { at: string; now: number }) {
  return (
    <time dateTime={at} title={formatDate(at)}>
      {formatAge(now - Date.parse(at))}
    </time>
  )
}

/** One line on what is happening now, ticking while a client is attached. */
function LiveLine({
  target,
  replacedFrom,
}: {
  target: DebuggerTarget
  replacedFrom: string | null
}) {
  // The clock only runs while there is an age to show; every other state
  // re-renders when its data does.
  const now = useNow(ATTACHED_STATES.has(target.state))
  const listen = listenAddress(target)
  let line: React.ReactNode
  switch (target.state) {
    case "unbound":
      line = <>Listening on {listen} — no container is bound yet.</>
      break
    case "listening":
      line = <>Listening on {listen} — no debugger attached.</>
      break
    case "attached":
      line = (
        <>
          Attached since <Since at={target.attachedSince} now={now} />. The timeout clock is
          suspended.
        </>
      )
      break
    case "paused":
      line = (
        <>
          Paused since <Since at={target.pausedSince} now={now} />
          {target.attachedSince && (
            <>
              {" "}
              (attached <Since at={target.attachedSince} now={now} />)
            </>
          )}
          .
        </>
      )
      break
    default:
      line = null
  }
  // The replacement notice is the one thing worth announcing; the ticking
  // age is not, so no live region wraps it.
  return (
    <div className="flex flex-col gap-2">
      {line && <p className="text-sm text-fg-muted">{line}</p>}
      {replacedFrom && (
        <p
          role="status"
          className="rounded-md border border-warning/30 bg-warning-muted px-3 py-2 text-sm text-warning"
        >
          Container replaced — your editor should reconnect.{" "}
          <span className="font-mono text-xs">(was {shortContainerId(replacedFrom)})</span>
        </p>
      )}
    </div>
  )
}

// ─── Panel ────────────────────────────────────────────────────────────────

/** Renders one descriptor. Key it on `target.id` when the resource can change under it. */
export function DebugPanel({ target }: { target: DebuggerTarget }) {
  const replacedFrom = useReplacedContainer(target)
  return (
    <div className="flex flex-col gap-4">
      <StateLine target={target} />
      {!target.enabled && <SetupBlock setup={target.setup} service={target.service} />}
      {target.enabled && <ResolvedTarget target={target} />}
      {target.enabled && <LiveLine target={target} replacedFrom={replacedFrom} />}
      {target.editors.length > 0 && <EditorTabs editors={target.editors} />}
    </div>
  )
}

/**
 * Fetches and renders the target for one resource. A resource the server has
 * no entry for at all — a service without a describer yet, a task that is
 * gone — reads as "off", not as a failure: the feature being absent is the
 * ordinary case on a default configuration.
 */
export function DebugTargetPanel({
  service,
  resource,
  container,
}: {
  service: string
  resource: string
  container?: string
}) {
  const { data, isPending, error } = useDebugTarget(service, resource, container)

  if (isPending) {
    return <SkeletonRows rows={3} noun="debugger state" />
  }
  if (error) {
    return (
      <p className="text-sm text-fg-muted">
        Debugger state unavailable — {error instanceof Error ? error.message : String(error)}
      </p>
    )
  }
  if (data === null) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <DebugStateBadge state="inert" />
        <span className="text-sm text-fg-muted">
          No debug target for this {service === "ecs" ? "container" : "resource"}.
        </span>
      </div>
    )
  }
  return <DebugPanel key={data.id} target={data} />
}
