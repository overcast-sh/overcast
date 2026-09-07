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
 * states around it.
 */
import { useEffect, useState } from "react"
import { CopyButton } from "@/components/ui/copy-button"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { Code, CodeBlock, SectionLabel } from "@/components/ui/primitives"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { SkeletonRows } from "@/components/ui/skeleton"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import { formatAge, formatDate } from "@/lib/format"
import { cn } from "@/lib/utils"
import type { DebuggerEditor, DebuggerSetup, DebuggerTarget } from "@/types"
import { useDebugTarget } from "../hooks"
import { ATTACHED_STATES, listenAddress, shortContainerId } from "../target"
import { DebugStateBadge } from "./debug-state-badge"

// ─── Container replacement ────────────────────────────────────────────────

/**
 * Remembers the container an attached editor was talking to, so the panel
 * can say when it has been swapped out from under it — hot reload recycling
 * the container, a redeploy — before the editor notices.
 *
 * The remembered pair is adopted on every *new* attach session
 * (`attachedSince` changes when the client count goes 0 → 1), which is what
 * lets the notice clear on its own: an editor that reconnected to the new
 * container starts a new session, one that is still holding the old splice
 * does not. Returns the previous container id while a replacement is
 * pending, null otherwise.
 */
function useReplacedContainer(target: DebuggerTarget): string | null {
  const attachedNow = ATTACHED_STATES.has(target.state) && target.containerId !== ""
  const [session, setSession] = useState<{ containerId: string; since: string } | null>(null)

  // State adjusted from a prop during render — the React-documented form,
  // guarded so it settles in one extra render rather than an effect's two.
  if (attachedNow && (session === null || session.since !== target.attachedSince)) {
    setSession({ containerId: target.containerId, since: target.attachedSince })
    return null
  }

  if (session === null || target.containerId === "" || target.containerId === session.containerId) {
    return null
  }
  return session.containerId
}

/** A once-a-second clock for the relative times on the live line. */
function useNow(intervalMs = 1_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

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
  return (
    <Card>
      <CardContent
        role="region"
        aria-labelledby="debugger-setup-heading"
        className="flex flex-col gap-3"
      >
        <SectionLabel as="h3" id="debugger-setup-heading">
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

/** One line on what is happening now, ticking while a client is attached. */
function LiveLine({
  target,
  replacedFrom,
}: {
  target: DebuggerTarget
  replacedFrom: string | null
}) {
  const now = useNow()
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
          Attached since{" "}
          <time dateTime={target.attachedSince} title={formatDate(target.attachedSince)}>
            {formatAge(now - Date.parse(target.attachedSince))}
          </time>
          . The timeout clock is suspended.
        </>
      )
      break
    case "paused":
      line = (
        <>
          Paused since{" "}
          <time dateTime={target.pausedSince} title={formatDate(target.pausedSince)}>
            {formatAge(now - Date.parse(target.pausedSince))}
          </time>
          {target.attachedSince && (
            <>
              {" "}
              (attached{" "}
              <time dateTime={target.attachedSince} title={formatDate(target.attachedSince)}>
                {formatAge(now - Date.parse(target.attachedSince))}
              </time>
              )
            </>
          )}
          .
        </>
      )
      break
    default:
      line = null
  }
  return (
    <div className="flex flex-col gap-2" aria-live="polite">
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

// ─── Editors ──────────────────────────────────────────────────────────────

const STEP_PREFIX = /^\d+\.\s+/

/**
 * A steps body is lines: numbered ones are the steps, anything else is a
 * sentence before or between them. Consecutive numbered lines become one
 * ordered list, so a list restarts its numbering exactly where the server's
 * text does.
 */
function StepsBody({ body }: { body: string }) {
  const blocks: Array<{ kind: "p"; text: string } | { kind: "ol"; items: string[] }> = []
  for (const line of body.split("\n")) {
    if (line.trim() === "") continue
    if (STEP_PREFIX.test(line)) {
      const item = line.replace(STEP_PREFIX, "")
      const last = blocks.at(-1)
      if (last?.kind === "ol") last.items.push(item)
      else blocks.push({ kind: "ol", items: [item] })
    } else {
      blocks.push({ kind: "p", text: line })
    }
  }
  return (
    <div className="flex flex-col gap-2 text-sm text-fg">
      {blocks.map((block, i) =>
        block.kind === "p" ? (
          <p key={i} className="text-fg-muted">
            {block.text}
          </p>
        ) : (
          <ol key={i} className="list-decimal space-y-1 pl-6 font-mono text-xs">
            {block.items.map((item, j) => (
              <li key={j}>{item}</li>
            ))}
          </ol>
        ),
      )}
    </div>
  )
}

function EditorBody({ editor }: { editor: DebuggerEditor }) {
  if (editor.kind === "steps") return <StepsBody body={editor.body} />
  // json and shell are both a block to paste; an unknown kind is shown the
  // same way rather than hidden, so a new server kind degrades to readable.
  return <CodeBlock className="whitespace-pre">{editor.body}</CodeBlock>
}

/** The editor tab strip: one tab per server-rendered editor, each with a copy button. */
function EditorTabs({ editors }: { editors: DebuggerEditor[] }) {
  const [selected, setSelected] = useState(editors[0].id)
  // The server may drop an editor between polls (a protocol change); fall
  // back to the first rather than rendering no panel.
  const selectedKey = editors.some((e) => e.id === selected) ? selected : editors[0].id
  return (
    <Tabs selectedKey={selectedKey} onSelectionChange={setSelected}>
      <TabList aria-label="Editor" className="gap-4">
        {editors.map((editor) => (
          <Tab key={editor.id} id={editor.id}>
            {editor.label}
          </Tab>
        ))}
      </TabList>
      {editors.map((editor) => (
        <TabPanel key={editor.id} id={editor.id} className="flex flex-col gap-2 pt-3">
          <div className="flex items-center justify-between gap-3">
            <span className="text-xs text-fg-muted">
              {editor.verified ? (
                <>Paste into your editor's attach configuration.</>
              ) : (
                <>Unverified — this configuration has not been tried end to end.</>
              )}
            </span>
            <CopyButton value={editor.body} noun={`${editor.label} configuration`} />
          </div>
          <EditorBody editor={editor} />
        </TabPanel>
      ))}
    </Tabs>
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
