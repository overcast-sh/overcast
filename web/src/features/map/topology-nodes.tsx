/**
 * topology-nodes — custom React Flow node for a single AWS resource.
 *
 * Renders service icon + resource label + a small event-count badge that
 * pulses when events flow through the node.
 */

import { memo, useEffect, useRef, useState, useMemo, useCallback, type ReactNode } from "react"
import * as RadixDialog from "@radix-ui/react-dialog"
import { Handle, Position, type NodeProps, useNodeId } from "@xyflow/react"
import { useNavigate } from "@tanstack/react-router"
import { useQuery, queryOptions } from "@tanstack/react-query"
import { useVirtualizer } from "@tanstack/react-virtual"
import { Box, Zap, Send, Play, Clock, Filter, Search, X } from "lucide-react"
import { SendMessageDialog } from "@/features/sqs/components/send-message"
import { PublishMessageDialog } from "@/features/sns/components/publish-dialog"
import { LambdaInvokeDialog } from "@/features/lambda/components/lambda-invoke-dialog"
import { LambdaInvocationsDrawer, type Invocation } from "./lambda-invocations-drawer"
import type { LogStreamTarget } from "./log-stream-peek"
import { logsStreamsQueryOptions } from "@/features/cloudwatch/logs/data"
import type { LogStream, StreamEvent, TopologyECSResourceType } from "@/types"
import { fieldLabel, sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { SERVICES } from "@/lib/service-registry"
import { sqs } from "@/services/api"
import type { LambdaInstance, SQSMessage } from "@/types"
import { useGhostTracker } from "@/hooks/use-ghost-tracker"
import { useLambdaInstances } from "@/hooks/use-lambda-instances"
import { useEventStream } from "@/hooks/use-event-stream"
import { EventType } from "@/services/event-types"
import { LambdaInstanceCard, LAMBDA_GHOST_TTL } from "./lambda-instance-node"
import { LAMBDA_GROUP_HEADER_H, LAMBDA_GROUP_MAX_VISIBLE } from "./map-layout"
import { useSqsEventMessages } from "./use-sqs-event-messages"
import {
  computeSqsVisualMessages,
  createSqsVisualMessagesState,
  isInflight,
  sqsVisualMessagesStateEqual,
  type DisplayMessage,
} from "./sqs-visual-messages"
import { CopyButton } from "@/components/ui/copy-button"
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useEndpoint } from "@/hooks/use-endpoint"
import { endpointStore } from "@/services/endpoint-store"
import { SERVICE_THEME, FALLBACK_COLOR, toSweep } from "./map-theme"
import "./map-animations.css"
import type { FileRoutesByTo } from "@/routeTree.gen"
import { Tooltip } from "@/components/ui/tooltip"
import { TriggerEventViewer } from "./trigger-event-viewer"

interface NodeRoute {
  to: keyof FileRoutesByTo
  params?: Record<string, string>
  search?: Record<string, string>
}

function routeHref(route: NodeRoute, search?: Record<string, string | undefined>): string {
  let href = route.to as string
  for (const [key, value] of Object.entries(route.params ?? {})) {
    href = href.replace(`$${key}`, encodeURIComponent(value))
  }
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries({ ...(route.search ?? {}), ...(search ?? {}) })) {
    if (value) params.set(key, value)
  }
  const query = params.toString()
  return query ? `${href}?${query}` : href
}

function openRouteInNewTab(route: NodeRoute, search?: Record<string, string | undefined>) {
  window.open(routeHref(route, search), "_blank", "noopener,noreferrer")
}

/**
 * Returns the deepest available route for a given service+resource name,
 * or null if there is no per-resource page.
 */
interface NodeRouteInput {
  service: string
  label: string
  nodeId?: string
  protocolType?: string
  ecsResourceType?: ServiceNodeData["ecsResourceType"]
  clusterName?: string
  taskId?: string
  scope?: string
}

function nodeRoute({
  service,
  label,
  nodeId,
  protocolType,
  ecsResourceType,
  clusterName,
  taskId,
  scope,
}: NodeRouteInput): NodeRoute | null {
  switch (service) {
    case "s3":
      return { to: "/s3/$bucket", params: { bucket: label } }
    case "sqs":
      return { to: "/sqs/$queue", params: { queue: label } }
    case "dynamodb":
      return { to: "/dynamodb/$tableName", params: { tableName: label } }
    case "sns":
      return { to: "/sns/$topic", params: { topic: label } }
    case "lambda":
      return { to: "/lambda/$name", params: { name: label } }
    case "logs":
      return { to: "/cloudwatch/logs/group" as const, search: { groupName: label } }
    case "ecs":
      if (ecsResourceType === "task" && clusterName && taskId) {
        return {
          to: "/ecs/$cluster/tasks/$taskId",
          params: { cluster: clusterName, taskId },
        }
      }
      if (ecsResourceType === "service" && clusterName) {
        return {
          to: "/ecs/$cluster",
          params: { cluster: clusterName },
          search: { tab: "services", service: label },
        }
      }
      return { to: "/ecs/$cluster", params: { cluster: clusterName ?? label } }
    case "ecr":
      return { to: "/ecr/$repositoryName", params: { repositoryName: label } }
    case "waf": {
      const webAclId = nodeId?.split("::")[2]
      return webAclId && scope
        ? {
            to: "/waf/$scope/$webAclId/$name",
            params: { scope, webAclId, name: label },
          }
        : { to: "/waf" }
    }
    case "ec2":
      return { to: "/ec2/$instanceId", params: { instanceId: label } }
    case "rds":
      return { to: "/rds/$instance", params: { instance: label } }
    case "apigateway": {
      // Node ID format: "region::apigateway::apiId" — extract the API ID.
      const apiId = nodeId?.split("::")[2]
      if (!apiId) return { to: "/apigateway" }
      if (protocolType === "REST") {
        return { to: "/apigateway/rest/$apiId", params: { apiId } }
      }
      return { to: "/apigateway/http/$apiId", params: { apiId } }
    }
    case "appsync": {
      // Node ID format: "region::appsync::apiId" — extract the API ID.
      const apiId = nodeId?.split("::")[2]
      return apiId ? { to: "/appsync/$apiId", params: { apiId } } : { to: "/appsync" }
    }
    case "cognito": {
      // Node ID format: "region::cognito::poolId" — extract the pool ID.
      const poolId = nodeId?.split("::")[2]
      return poolId ? { to: "/cognito/$poolId", params: { poolId } } : { to: "/cognito" }
    }
    case "msk":
      return { to: "/msk" }
    default:
      return null
  }
}

export interface ServiceNodeData extends Record<string, unknown> {
  service: string
  label: string
  /** AWS region this resource belongs to */
  region?: string
  streamEnabled?: boolean
  /** Total events routed through this node since mount — drives badge + ring pulse */
  eventCount?: number
  /** Data-write events (PutObject, PutItem, etc.) — drives the sweep flash */
  writeCount?: number
  /**
   * Draining write-burst count (S3/DynamoDB only) — shown as a badge and drains
   * 1 per ~2 s so rapid writes remain visible after the sweep flash fades.
   */
  writeBurstCount?: number
  /** True only on the first render after a new node appears — triggers pop-in animation */
  isNew?: boolean
  /** SQS only — visible message count */
  approximateNumberOfMessages?: number
  /** SQS only — in-flight message count */
  approximateNumberOfMessagesNotVisible?: number
  /** CloudWatch Logs only — callback fired when user clicks a stream row */
  onPeekStream?: (target: LogStreamTarget) => void
  /** Resource status string (RDS: "available", "stopped", etc.) */
  status?: string
  /** API Gateway only — REST or HTTP */
  protocolType?: string
  /** API Gateway only — number of routes/resources */
  routeCount?: number
  /** API Gateway only — number of deployed stages */
  stageCount?: number
  /** AppSync only — authentication type */
  authenticationType?: string
  /** AppSync only — number of data sources */
  dataSourceCount?: number
  /** AppSync only — number of resolvers */
  resolverCount?: number
  /** ECS only — distinguishes cluster, service, and task map nodes. */
  ecsResourceType?: TopologyECSResourceType
  /** ECS service/task owner used for navigation. */
  clusterName?: string
  /** ECS task UUID used for task detail navigation. */
  taskId?: string
  /** ECS service configured task count. */
  desiredCount?: number
  /** ECS service currently running task count. */
  runningCount?: number
  /** ECR only — full push-ready repository URI for copy-to-clipboard action. */
  repositoryUri?: string
  /** WAF only — REGIONAL or CLOUDFRONT. */
  scope?: string
  /** WAF only — number of stored rules. */
  ruleCount?: number
  /** ESM filter node only. */
  esmId?: string
  functionName?: string
  eventSource?: string
  sourceType?: string
  filterPatterns?: string[]
  hasTarget?: boolean
  hasSource?: boolean
}

// How long the "pulse" ring stays visible after an event (ms)
const PULSE_TTL = 1500

// How long the inbound sweep flash lasts (ms) — 2 s so rapid writes aren’t invisible
const FLASH_TTL = 2_000

/** Height (px) of the scrollable message list section inside an SQS node. */
const SQS_NODE_LIST_H = 128
/** Estimated row height (px) for the message virtualizer. */
const SQS_MSG_ROW_H = 28
/**
 * Total node height (px) when the message list is visible.
 * Exported so map-page.tsx can pass this as a size override to the layout.
 */
export const SQS_NODE_EXPANDED_H = 202
/**
 * Height (px) of an SQS node with no messages: header row plus the stats bar.
 * The layout reserves exactly this so the handles sit on the card's midline.
 */
export const SQS_NODE_IDLE_H = 66
/** Height (px) of a node whose second line is a status pill (RDS). */
export const STATUS_NODE_H = 58

/** Height (px) of the scrollable stream list inside a CloudWatch Logs node. */
const LOGS_NODE_LIST_H = 112
/** Row height (px) for each stream row. */
const LOGS_STREAM_ROW_H = 28
/**
 * Total height (px) for expanded CloudWatch Logs nodes.
 * Exported so map-page.tsx can pass this as a size override to the layout.
 */
export const LOGS_NODE_EXPANDED_H = 175

/** How recently a stream must have been written to show the active dot (ms). */
const LOGS_ACTIVITY_TTL = 30_000

// How long the message-movement dot stays visible (ms)
const MSG_ANIM_TTL = 900

/**
 * SqsStatsBar — small stats row shown inside SQS queue nodes.
 *
 * Renders "N visible" and "M in-flight" pills and animates a dot
 * when messages move between states:
 *   visible → in-flight  : dot slides →  (message received)
 *   in-flight → visible  : dot slides ←  (visibility timeout elapsed)
 *   in-flight disappears : dot fades     (message deleted)
 */
const SqsStatsBar = memo(function SqsStatsBar({
  visible,
  inFlight,
}: {
  visible: number
  inFlight: number
}) {
  const prevVisible = useRef(visible)
  const prevInFlight = useRef(inFlight)

  // "direction" drives the CSS animation:
  //   "right"  = message moved to in-flight
  //   "left"   = message returned to visible
  //   "delete" = message was deleted
  //   null     = idle
  const [direction, setDirection] = useState<"right" | "left" | "delete" | null>(null)

  useEffect(() => {
    const dv = visible - prevVisible.current
    const df = inFlight - prevInFlight.current
    prevVisible.current = visible
    prevInFlight.current = inFlight

    if (df === 0) return

    let next: "right" | "left" | "delete" | null = null
    if (df > 0) {
      // in-flight increased → received
      next = "right"
    } else if (df < 0 && dv > 0) {
      // in-flight decreased, visible increased → VT expired, returned
      next = "left"
    } else if (df < 0) {
      // in-flight decreased, visible unchanged/decreased → deleted
      next = "delete"
    }

    if (!next) return
    // Deliberate set-state-in-effect: the animation is a reaction to a prop
    // delta — detected against the previous render's counts held in the refs
    // above — not derived state, and the timeout below resets it.
    setDirection(next)
    const t = setTimeout(() => setDirection(null), MSG_ANIM_TTL)
    return () => clearTimeout(t)
  }, [visible, inFlight])

  const dotStyle: React.CSSProperties | undefined = direction
    ? {
        position: "absolute" as const,
        top: "50%",
        marginTop: "-3px",
        width: 6,
        height: 6,
        borderRadius: "50%",
        background: direction === "delete" ? "#f87171" : "#facc15",
        animation: `${
          direction === "right"
            ? "overcastMsgRight"
            : direction === "left"
              ? "overcastMsgLeft"
              : "overcastMsgFade"
        } ${MSG_ANIM_TTL}ms ease-in-out forwards`,
      }
    : undefined

  return (
    <div className="mt-2 flex items-center gap-2 font-mono text-xs font-semibold tabular-nums">
      {/* Visible pill */}
      <span
        className={cn(
          "flex items-center gap-0.5 rounded px-1.5 py-0.5",
          visible > 0 ? "bg-success-muted text-success" : "bg-fg-muted/15 text-fg-muted",
        )}
        title="Visible messages"
      >
        &#8595;{visible}
      </span>

      {/* Animation lane */}
      <div className="relative flex h-3 flex-1 items-center overflow-visible">
        <div className="h-px w-full bg-fg-muted/20" />
        {dotStyle && <span aria-hidden style={dotStyle} />}
      </div>

      {/* In-flight pill */}
      <span
        className={cn(
          "flex items-center gap-0.5 rounded px-1.5 py-0.5",
          inFlight > 0 ? "bg-warning-muted text-warning" : "bg-fg-muted/15 text-fg-muted",
        )}
        title="In-flight messages (received, not yet deleted)"
      >
        &#8599;{inFlight}
      </span>
    </div>
  )
})

// ─── SqsMessageList ──────────────────────────────────────────────────────────

/**
 * Virtualized list of messages stored in an SQS queue, shown inside the node.
 * Uses the peek endpoint — zero state mutation, no visibility timeout applied.
 */

/** How long a deleted message lingers as a ghost before being removed (ms). */
const GHOST_TTL = 60_000
/** Ghost rows start fading this many ms before expiry. */
const GHOST_FADE_START = 30_000
function useSqsVisualMessages(
  queueName: string,
  liveMessages: SQSMessage[],
  sqsEvents: Array<{
    type: string
    time: string
    payload: unknown
  }>,
) {
  const [nowMs, setNowMs] = useState(() => Date.now())
  const ghostSource = [...liveMessages]
  const ghosts = useGhostTracker({
    items: ghostSource,
    getKey: (m) => m.messageId,
    ttl: GHOST_TTL,
  })
  const [visualState, setVisualState] = useState(createSqsVisualMessagesState)
  const visualResult = useMemo(
    () =>
      computeSqsVisualMessages({
        queueName,
        liveMessages,
        ghosts,
        sqsEvents,
        nowMs,
        state: visualState,
      }),
    [queueName, liveMessages, ghosts, sqsEvents, nowMs, visualState],
  )

  if (!sqsVisualMessagesStateEqual(visualState, visualResult.state)) {
    setVisualState(visualResult.state)
  }

  const messages = visualResult.messages

  useEffect(() => {
    if (!visualResult.needsClock) return
    const id = setInterval(() => setNowMs(Date.now()), 250)
    return () => clearInterval(id)
  }, [visualResult.needsClock])

  const visibleCount = useMemo(
    () => messages.filter((m) => !m.isGhost && m.visualPhase === "visible").length,
    [messages],
  )
  const inFlightCount = useMemo(
    () =>
      messages.filter((m) => m.visualPhase === "inflight" || m.visualPhase === "delayed").length,
    [messages],
  )

  return { messages, visibleCount, inFlightCount }
}

const SqsMessageList = memo(function SqsMessageList({
  messages,
  onSelect,
}: {
  messages: DisplayMessage[]
  onSelect: (msg: SQSMessage) => void
}) {
  "use no memo"
  // Track when each inflight window started, keyed by "messageId:visibleAfter".
  // Used to compute the countdown bar width without requiring extra server data.
  const inflightStart = useRef<Map<string, number>>(new Map())

  const containerRef = useRef<HTMLDivElement>(null)

  const virtualizer = useVirtualizer({
    count: messages.length,
    getScrollElement: () => containerRef.current,
    estimateSize: () => SQS_MSG_ROW_H,
    overscan: 3,
  })

  // Prevent ReactFlow panning when the user scrolls the list.
  const stopWheel = useCallback((e: React.WheelEvent) => e.stopPropagation(), [])
  // Prevent the node-level navigation click when clicking inside the list.
  const stopClick = useCallback((e: React.MouseEvent) => e.stopPropagation(), [])

  if (messages.length === 0) return null

  return (
    <div
      ref={containerRef}
      onWheel={stopWheel}
      onClick={stopClick}
      className="map-detail mt-2 overflow-y-auto rounded border border-border/40"
      style={{ height: SQS_NODE_LIST_H }}
    >
      <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
        {virtualizer.getVirtualItems().map((row) => {
          const { msg, isGhost, deletedAt, visualPhase } = messages[row.index]
          const isDone = visualPhase === "done"
          // Ghosts fade out linearly over GHOST_FADE_START ms before expiry.
          const ghostAge = isDone && deletedAt ? Date.now() - deletedAt : 0
          const ghostOpacity = isDone
            ? Math.max(
                0.15,
                1 - Math.max(0, ghostAge - (GHOST_TTL - GHOST_FADE_START)) / GHOST_FADE_START,
              )
            : 1
          return (
            <button
              key={msg.messageId}
              type="button"
              onClick={() => !isGhost && onSelect(msg)}
              style={{
                position: "absolute",
                top: row.start,
                left: 0,
                right: 0,
                height: row.size,
                opacity: ghostOpacity,
              }}
              className={cn(
                "flex w-full items-center gap-1.5 overflow-hidden px-2 text-left text-xs transition-colors",
                isDone ? "cursor-default" : "hover:bg-bg-muted/60",
                row.index % 2 === 0 ? "bg-bg/30" : "bg-transparent",
              )}
            >
              {/* Receive count badge — shown first so it's immediately visible */}
              <span
                className={cn(
                  "shrink-0 rounded bg-fg-muted/20 px-1 py-px font-mono text-2xs font-bold text-fg-muted tabular-nums",
                  isDone && "line-through",
                )}
                title={`Received ${msg.approximateReceiveCount} time(s)`}
              >
                &times;{msg.approximateReceiveCount}
              </span>
              {/* Status badge */}
              <span
                className={cn(
                  "inline-flex shrink-0 items-center rounded px-1 py-px font-mono text-2xs leading-none font-bold uppercase",
                  visualPhase === "done"
                    ? "bg-danger-muted text-danger"
                    : visualPhase === "delayed"
                      ? "bg-accent-muted text-accent"
                      : visualPhase === "inflight"
                        ? "bg-warning-muted text-warning"
                        : "bg-success-muted text-success",
                )}
              >
                {visualPhase === "done"
                  ? "done"
                  : visualPhase === "delayed"
                    ? "delay"
                    : visualPhase === "inflight"
                      ? "flight"
                      : "vis"}
              </span>
              <span className={cn("truncate font-mono text-fg-subtle", isDone && "line-through")}>
                {msg.messageId}
              </span>
              {/* Countdown bar — warning for in-flight, accent for delayed (live messages only) */}
              {visualPhase !== "done" &&
                visualPhase !== "visible" &&
                msg.visibleAfter > 0 &&
                (() => {
                  const key = `${msg.messageId}:${msg.visibleAfter}`
                  if (!inflightStart.current.has(key)) {
                    inflightStart.current.set(key, Date.now())
                  }
                  const start = inflightStart.current.get(key)!
                  const total = msg.visibleAfter - start
                  const remaining = msg.visibleAfter - Date.now()
                  const pct = total > 0 ? Math.max(0, Math.min(1, remaining / total)) : 0
                  return (
                    <div
                      className={cn(
                        "pointer-events-none absolute inset-x-0 bottom-0 h-px origin-left",
                        visualPhase === "delayed" ? "bg-accent" : "bg-warning",
                      )}
                      style={{
                        transform: `scaleX(${pct})`,
                        transition: "transform 1s linear",
                      }}
                    />
                  )
                })()}
            </button>
          )
        })}
      </div>
    </div>
  )
})

// ─── LogStreamList ───────────────────────────────────────────────────────────

/**
 * Scrollable list of log streams for a CloudWatch Logs group node.
 * Streams are sorted most-recently-active first; an emerald dot marks
 * streams that had events written within the last 30 s.
 */
const LogStreamList = memo(function LogStreamList({
  groupName,
  onSelect,
}: {
  groupName: string
  onSelect: (stream: LogStream) => void
}) {
  "use no memo"
  const { data: streams = [] } = useQuery({
    ...logsStreamsQueryOptions(groupName),
    refetchInterval: 10_000,
    staleTime: 0,
  })

  const sorted = useMemo(
    () => [...streams].sort((a, b) => (b.lastIngestionTime ?? 0) - (a.lastIngestionTime ?? 0)),
    [streams],
  )

  const containerRef = useRef<HTMLDivElement>(null)
  const virtualizer = useVirtualizer({
    count: sorted.length,
    getScrollElement: () => containerRef.current,
    estimateSize: () => LOGS_STREAM_ROW_H,
    overscan: 3,
  })

  const stopWheel = useCallback((e: React.WheelEvent) => e.stopPropagation(), [])
  const stopClick = useCallback((e: React.MouseEvent) => e.stopPropagation(), [])

  // The layout reserves the list's height whether or not streams exist yet
  // (the topology does not know), so an empty group shows the space it will
  // fill rather than a short card floating in a tall slot.
  if (sorted.length === 0) {
    return (
      <div
        className="map-detail mt-2 flex items-center justify-center rounded border border-dashed border-border/40 text-2xs text-fg-subtle"
        style={{ height: LOGS_NODE_LIST_H }}
      >
        no streams yet
      </div>
    )
  }

  const now = Date.now()

  return (
    <div
      ref={containerRef}
      onWheel={stopWheel}
      onClick={stopClick}
      className="map-detail mt-2 overflow-y-auto rounded border border-border/40"
      style={{ height: LOGS_NODE_LIST_H }}
    >
      <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
        {virtualizer.getVirtualItems().map((row) => {
          const stream = sorted[row.index]
          const isActive = (stream.lastIngestionTime ?? 0) > now - LOGS_ACTIVITY_TTL
          return (
            <button
              key={stream.logStreamName}
              type="button"
              data-peek-trigger
              onClick={() => onSelect(stream)}
              style={{
                position: "absolute",
                top: row.start,
                left: 0,
                right: 0,
                height: row.size,
              }}
              className={cn(
                "flex w-full items-center gap-2 overflow-hidden px-2 text-left text-xs transition-colors hover:bg-bg-muted/60",
                row.index % 2 === 0 ? "bg-bg/30" : "bg-transparent",
              )}
            >
              <span
                className={cn(
                  "h-1.5 w-1.5 shrink-0 rounded-full transition-colors",
                  isActive ? "bg-success" : "bg-fg-muted/30",
                )}
                title={isActive ? "Recently active" : "No recent activity"}
              />
              <span className="truncate font-mono text-fg-subtle">
                {stream.logStreamName ?? ""}
              </span>
            </button>
          )
        })}
      </div>
    </div>
  )
})

// ─── RdsStatusBadge ──────────────────────────────────────────────────────────

/** Small coloured pill showing an RDS instance status. */
const RdsStatusBadge = memo(function RdsStatusBadge({ status }: { status: string }) {
  const colourClass =
    status === "available"
      ? "bg-success-muted text-success"
      : status === "stopped"
        ? "bg-fg-muted/20 text-fg-muted"
        : status === "stopping" || status === "starting"
          ? "bg-warning-muted text-warning"
          : status === "deleting" || status === "failed"
            ? "bg-danger-muted text-danger"
            : "bg-accent-muted text-accent"
  return (
    <span
      className={cn(
        "mt-0.5 inline-flex items-center rounded px-1.5 py-0.5 font-mono text-2xs font-semibold uppercase",
        colourClass,
      )}
    >
      {status}
    </span>
  )
})

// ─── SqsMessageModal ─────────────────────────────────────────────────────────

/** Inspect a peeked SQS message — body, attributes, system attributes. Read-only. */
function SqsMessageModal({
  msg,
  queueName,
  open,
  onClose,
}: {
  msg: SQSMessage | null
  queueName: string
  open: boolean
  onClose: () => void
}) {
  const messageId = msg?.messageId
  // Fetch the full message (with body) on-demand when the modal opens.
  const { data: fullMsg, isLoading } = useQuery(
    queryOptions({
      queryKey: ["sqs", "message-detail", queueName, messageId],
      queryFn: async () => {
        if (!messageId) return null
        const messages = await sqs.receiveMessages(queueName)
        return messages.find((m) => m.messageId === messageId) ?? null
      },
      enabled: open && !!messageId,
      staleTime: 0,
      gcTime: 30_000,
    }),
  )
  // Show the event-driven stub while loading, enriched message once loaded.
  // Show the event-driven stub while loading, enriched message once loaded.
  const displayMsg = fullMsg ?? msg
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose()
      }}
    >
      <DialogContent
        className="flex max-h-[80vh] max-w-xl flex-col overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        <DialogHeader>
          <DialogTitle>SQS Message</DialogTitle>
          <p className="font-mono text-xs break-all text-fg-muted">{displayMsg?.messageId}</p>
        </DialogHeader>
        {displayMsg && (
          <>
            <div className="mb-3 flex flex-wrap gap-2 text-xs">
              <span
                className={cn(
                  "rounded px-1.5 py-0.5 font-mono text-2xs font-bold uppercase",
                  isInflight(displayMsg)
                    ? "bg-warning-muted text-warning"
                    : "bg-success-muted text-success",
                )}
              >
                {isInflight(displayMsg) ? "In-flight" : "Visible"}
              </span>
              <span className="rounded bg-fg-muted/15 px-1.5 py-0.5 text-fg-muted">
                received &times;{displayMsg.approximateReceiveCount}
              </span>
              {isInflight(displayMsg) && displayMsg.visibleAfter > 0 && (
                <span className="rounded bg-fg-muted/15 px-1.5 py-0.5 text-fg-muted">
                  visible {new Date(displayMsg.visibleAfter).toLocaleTimeString()}
                </span>
              )}
            </div>
            <div className="flex-1 space-y-4 overflow-y-auto pr-1">
              {isLoading ? (
                <p className="py-4 text-center text-xs text-fg-muted">Loading message…</p>
              ) : (
                <>
                  <section>
                    <p className={cn(sectionLabel, "mb-1 text-fg-muted")}>Body</p>
                    <pre className="max-h-48 overflow-x-auto rounded bg-bg p-2 text-xs break-all whitespace-pre-wrap text-fg">
                      {displayMsg.body || "(empty)"}
                    </pre>
                  </section>

                  {Object.keys(displayMsg.messageAttributes).length > 0 && (
                    <section>
                      <p className={cn(sectionLabel, "mb-1 text-fg-muted")}>Message Attributes</p>
                      <table className="w-full text-xs">
                        <tbody>
                          {Object.entries(displayMsg.messageAttributes).map(([k, v]) => (
                            <tr key={k} className="border-b border-border/30 last:border-0">
                              <td className="w-2/5 py-1 pr-3 align-top font-mono break-all text-fg-muted">
                                {k}
                              </td>
                              <td className="py-1 font-mono break-all text-fg">{v.stringValue}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </section>
                  )}

                  {Object.keys(displayMsg.attributes).length > 0 && (
                    <section>
                      <p className={cn(sectionLabel, "mb-1 text-fg-muted")}>System Attributes</p>
                      <table className="w-full text-xs">
                        <tbody>
                          {Object.entries(displayMsg.attributes).map(([k, v]) => (
                            <tr key={k} className="border-b border-border/30 last:border-0">
                              <td className="w-2/5 py-1 pr-3 align-top font-mono break-all text-fg-muted">
                                {k}
                              </td>
                              <td className="py-1 font-mono break-all text-fg">{v}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </section>
                  )}
                </>
              )}
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

// ─── ServiceNode ─────────────────────────────────────────────────────────────

function areServiceNodePropsEqual(prev: NodeProps, next: NodeProps): boolean {
  if (prev.selected !== next.selected) return false
  const pd = prev.data as ServiceNodeData
  const nd = next.data as ServiceNodeData
  return (
    pd.service === nd.service &&
    pd.label === nd.label &&
    pd.region === nd.region &&
    pd.streamEnabled === nd.streamEnabled &&
    pd.eventCount === nd.eventCount &&
    pd.writeCount === nd.writeCount &&
    pd.writeBurstCount === nd.writeBurstCount &&
    pd.isNew === nd.isNew &&
    pd.approximateNumberOfMessages === nd.approximateNumberOfMessages &&
    pd.approximateNumberOfMessagesNotVisible === nd.approximateNumberOfMessagesNotVisible &&
    pd.status === nd.status &&
    pd.protocolType === nd.protocolType &&
    pd.routeCount === nd.routeCount &&
    pd.stageCount === nd.stageCount &&
    pd.authenticationType === nd.authenticationType &&
    pd.dataSourceCount === nd.dataSourceCount &&
    pd.resolverCount === nd.resolverCount &&
    pd.repositoryUri === nd.repositoryUri &&
    pd.scope === nd.scope &&
    pd.ruleCount === nd.ruleCount &&
    pd.esmId === nd.esmId &&
    pd.functionName === nd.functionName &&
    pd.eventSource === nd.eventSource &&
    pd.sourceType === nd.sourceType &&
    JSON.stringify(pd.filterPatterns ?? []) === JSON.stringify(nd.filterPatterns ?? []) &&
    pd.hasTarget === nd.hasTarget &&
    pd.hasSource === nd.hasSource
  )
}

type ESMDecisionEvent = StreamEvent & {
  payload?: {
    esmId?: string
    matched?: boolean
    eventName?: string
    record?: unknown
    filterPatterns?: string[]
  }
}

type ESMFilterMode = "all" | "matched" | "filtered"

function recordSearchText(event: ESMDecisionEvent): string {
  return JSON.stringify(event.payload?.record ?? event.payload ?? {}, null, 2)
}

function isRecordObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function stringifyRecordSection(value: unknown): string {
  return JSON.stringify(value ?? {}, null, 2)
}

function prettyFilterPatternArray(patterns: string[]): string {
  const parsed = patterns.map((pattern) => {
    try {
      return JSON.parse(pattern) as unknown
    } catch {
      return pattern
    }
  })
  return JSON.stringify(parsed, null, 2)
}

function HighlightedText({ text, query }: { text: string; query: string }) {
  const q = query.trim()
  if (!q) return <>{text}</>

  const lower = text.toLowerCase()
  const needle = q.toLowerCase()
  const parts: ReactNode[] = []
  let idx = 0
  let match = lower.indexOf(needle, idx)
  while (match >= 0) {
    if (match > idx) parts.push(text.slice(idx, match))
    parts.push(
      <mark key={`${match}:${needle}`} className="rounded bg-warning/25 px-0.5 text-fg">
        {text.slice(match, match + q.length)}
      </mark>,
    )
    idx = match + q.length
    match = lower.indexOf(needle, idx)
  }
  if (idx < text.length) parts.push(text.slice(idx))
  return <>{parts}</>
}

function ESMRecordDetails({ event, query }: { event: ESMDecisionEvent; query: string }) {
  const record = event.payload?.record
  const q = query.trim()
  if (!isRecordObject(record)) {
    return <TriggerEventViewer event={record ?? event.payload} />
  }

  if (q) {
    return (
      <pre tabIndex={0} className="max-h-80 overflow-auto rounded border border-border bg-bg p-3 text-xs leading-relaxed whitespace-pre-wrap text-fg">
        <HighlightedText text={recordSearchText(event)} query={q} />
      </pre>
    )
  }

  const dynamodb = isRecordObject(record.dynamodb) ? record.dynamodb : undefined
  const sections = [
    ["Keys", dynamodb?.Keys],
    ["New image", dynamodb?.NewImage],
    ["Old image", dynamodb?.OldImage],
  ].filter((section): section is [string, unknown] => section[1] !== undefined)

  if (sections.length === 0) {
    return <TriggerEventViewer event={record} />
  }

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-2 text-xs text-fg-muted @md:grid-cols-4">
        {typeof record.eventID === "string" && (
          <div className="rounded bg-bg px-2 py-1">
            <span className="block font-mono text-2xs font-semibold text-fg-subtle uppercase">
              Event ID
            </span>
            <span className="break-all text-fg">{record.eventID}</span>
          </div>
        )}
        {typeof record.eventVersion === "string" && (
          <div className="rounded bg-bg px-2 py-1">
            <span className="block font-mono text-2xs font-semibold text-fg-subtle uppercase">
              Version
            </span>
            <span className="text-fg">{record.eventVersion}</span>
          </div>
        )}
        {typeof record.awsRegion === "string" && (
          <div className="rounded bg-bg px-2 py-1">
            <span className="block font-mono text-2xs font-semibold text-fg-subtle uppercase">
              Region
            </span>
            <span className="text-fg">{record.awsRegion}</span>
          </div>
        )}
        {typeof dynamodb?.SequenceNumber === "string" && (
          <div className="rounded bg-bg px-2 py-1">
            <span className="block font-mono text-2xs font-semibold text-fg-subtle uppercase">
              Sequence
            </span>
            <span className="break-all text-fg">{dynamodb.SequenceNumber}</span>
          </div>
        )}
      </div>
      <div className="grid gap-3 @lg:grid-cols-2">
        {sections.map(([label, value]) => (
          <div key={label} className="min-w-0 rounded border border-border bg-bg">
            <div className={cn(sectionLabel, "border-b border-border px-3 py-2 text-fg-muted")}>
              {label}
            </div>
            <pre tabIndex={0} className="max-h-56 overflow-auto p-3 text-xs leading-relaxed whitespace-pre-wrap text-fg">
              {stringifyRecordSection(value)}
            </pre>
          </div>
        ))}
      </div>
    </div>
  )
}

function ESMFilterPanel({ esmId, patterns }: { esmId: string; patterns: string[] }) {
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<ESMFilterMode>("all")
  const [query, setQuery] = useState("")
  const { events } = useEventStream({ source: "lambda" })
  const decisions = useMemo(
    () =>
      events
        .filter(
          (e): e is ESMDecisionEvent =>
            (e.type === EventType.lambda.ESMRecordFiltered ||
              e.type === EventType.lambda.ESMRecordMatched) &&
            (e.payload as { esmId?: string } | undefined)?.esmId === esmId,
        )
        .map((event, index) => ({ event, receipt: index + 1 })),
    [events, esmId],
  )
  const visibleDecisions = useMemo(
    () =>
      decisions.filter(({ event }) => {
        const didMatch = event.payload?.matched === true
        if (mode === "matched" && !didMatch) return false
        if (mode === "filtered" && didMatch) return false

        const q = query.trim().toLowerCase()
        if (!q) return true
        return `${event.payload?.eventName ?? ""}\n${recordSearchText(event)}`
          .toLowerCase()
          .includes(q)
      }),
    [decisions, mode, query],
  )
  const matched = decisions.filter(({ event }) => event.payload?.matched === true).length
  const filtered = decisions.filter(({ event }) => event.payload?.matched === false).length
  const total = matched + filtered
  const filterPatternText = patterns.length > 0 ? prettyFilterPatternArray(patterns) : "[]"

  return (
    <>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          setOpen(true)
        }}
        className="group flex h-full w-full items-center justify-center rounded-full border border-cat-3/35 bg-cat-3/15 text-cat-3 shadow-lg shadow-cat-3/20 transition-all hover:scale-105 hover:border-cat-3/70 hover:bg-cat-3/25"
        title="Peek DynamoDB stream filter decisions"
      >
        <Filter className="h-7 w-7 transition-transform group-hover:rotate-12" />
        {total > 0 && (
          <span className="absolute -top-1 -right-1 flex h-5 min-w-5 items-center justify-center rounded-full border border-cat-3/40 bg-bg-elevated px-1 font-mono text-2xs font-black text-cat-3 tabular-nums shadow">
            {total > 99 ? "99+" : total}
          </span>
        )}
      </button>

      <RadixDialog.Root open={open} onOpenChange={setOpen}>
        <RadixDialog.Portal>
          <RadixDialog.Overlay className="fixed inset-0 z-60 bg-black/20 backdrop-blur-[1px]" />
          <RadixDialog.Content
            aria-describedby={undefined}
            onClick={(e) => e.stopPropagation()}
            className={cn(
              "fixed inset-y-0 right-0 z-70 flex w-[min(760px,100vw)] flex-col border-l border-border bg-bg-elevated shadow-2xl",
              "transition-transform duration-300 data-[state=closed]:translate-x-full data-[state=open]:translate-x-0",
            )}
          >
            <div className="flex shrink-0 items-start justify-between gap-4 border-b border-border bg-gradient-to-br from-cat-3/12 via-bg-elevated to-bg-elevated px-5 py-4">
              <div className="min-w-0">
                <RadixDialog.Title className="flex items-center gap-2 text-base font-semibold text-fg">
                  <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-cat-3/15 text-cat-3">
                    <Filter className="h-4 w-4" />
                  </span>
                  Event Source Mapping Filter
                </RadixDialog.Title>
                <p className="mt-1 text-xs text-fg-muted">
                  DynamoDB stream records evaluated by this Lambda trigger, in receipt order.
                </p>
              </div>
              <RadixDialog.Close asChild>
                <button
                  type="button"
                  className="rounded p-1.5 text-fg-muted transition-colors hover:bg-fg-muted/15 hover:text-fg"
                  aria-label="Close"
                >
                  <X className="h-4 w-4" />
                </button>
              </RadixDialog.Close>
            </div>

            <div className="grid shrink-0 grid-cols-3 gap-2 border-b border-border px-5 py-3">
              <div className="rounded-lg border border-border bg-bg px-3 py-2">
                <div className={cn(fieldLabel, "text-fg-subtle")}>Received</div>
                <div className="font-mono text-xl font-semibold text-fg tabular-nums">{total}</div>
              </div>
              <div className="rounded-lg border border-success/20 bg-success/8 px-3 py-2">
                <div className={cn(fieldLabel, "text-success")}>Filtered in</div>
                <div className="font-mono text-xl font-semibold text-success tabular-nums">
                  {matched}
                </div>
              </div>
              <div className="rounded-lg border border-danger/20 bg-danger/8 px-3 py-2">
                <div className={cn(fieldLabel, "text-danger")}>Filtered out</div>
                <div className="font-mono text-xl font-semibold text-danger tabular-nums">
                  {filtered}
                </div>
              </div>
            </div>

            <div className="shrink-0 border-b border-border px-5 py-4">
              <div className="mb-2 flex items-center justify-between gap-3">
                <h3 className="font-mono text-sm font-semibold text-fg">Filter pattern</h3>
                {patterns.length > 1 && (
                  <span className="text-xs text-fg-muted">{patterns.length} patterns</span>
                )}
              </div>
              <pre tabIndex={0} className="max-h-32 overflow-auto rounded-lg border border-cat-3/15 bg-cat-3/6 p-3 text-xs leading-relaxed whitespace-pre-wrap text-fg">
                {filterPatternText}
              </pre>
            </div>

            <div className="shrink-0 border-b border-border px-5 py-4">
              <div className="mb-3 flex flex-col gap-3 @md:flex-row @md:items-center @md:justify-between">
                <div>
                  <h3 className="font-mono text-sm font-semibold text-fg">Record history</h3>
                  <p className="text-xs text-fg-subtle">
                    Showing {visibleDecisions.length} matching records from the local event cache.
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-1 rounded-lg border border-border bg-bg p-1 text-xs">
                  {(["all", "matched", "filtered"] as const).map((value) => (
                    <button
                      key={value}
                      type="button"
                      onClick={() => setMode(value)}
                      className={cn(
                        "rounded-md px-3 py-1.5 font-medium transition-colors",
                        mode === value
                          ? "bg-cat-3/20 text-cat-3 shadow-sm"
                          : "text-fg-muted hover:bg-bg-elevated hover:text-fg",
                      )}
                    >
                      {value === "all" ? "Both" : value === "matched" ? "In" : "Out"}
                    </button>
                  ))}
                </div>
              </div>
              <div className="relative">
                <Search className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-fg-subtle" />
                <input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Search keys, image values, sequence numbers..."
                  className="w-full rounded-lg border border-border bg-bg py-2.5 pr-3 pl-9 text-sm text-fg transition-colors placeholder:text-fg-subtle hover:border-accent focus:border-accent focus:outline-none"
                />
              </div>
            </div>

            <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
              {decisions.length === 0 ? (
                <div className="rounded-xl border border-dashed border-border bg-bg/60 p-8 text-center">
                  <Filter className="mx-auto mb-3 h-8 w-8 text-fg-subtle" />
                  <p className="text-sm font-medium text-fg">
                    No records have reached this filter yet.
                  </p>
                  <p className="mt-1 text-xs text-fg-muted">
                    Write to the DynamoDB table after opening the map to see filter decisions here.
                  </p>
                </div>
              ) : visibleDecisions.length === 0 ? (
                <div className="rounded-xl border border-dashed border-border bg-bg/60 p-8 text-center text-sm text-fg-muted">
                  No records match the current toggle and search.
                </div>
              ) : (
                <div className="space-y-3">
                  {visibleDecisions.map(({ event, receipt }) => {
                    const didMatch = event.payload?.matched === true
                    return (
                      <article
                        key={`${event.time}:${receipt}`}
                        className={cn(
                          "overflow-hidden rounded-xl border bg-bg-muted shadow-sm",
                          didMatch ? "border-success/20" : "border-danger/20",
                        )}
                      >
                        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-bg/60 px-4 py-3">
                          <div className="flex items-center gap-2">
                            <span className="rounded-md bg-bg-elevated px-2 py-1 font-mono text-2xs font-bold text-fg-muted tabular-nums">
                              #{receipt}
                            </span>
                            <span
                              className={cn(
                                "rounded-md px-2 py-1 font-mono text-2xs tracking-[0.12em] uppercase",
                                didMatch
                                  ? "bg-success-muted text-success"
                                  : "bg-danger-muted text-danger",
                              )}
                            >
                              {didMatch ? "Filtered in" : "Filtered out"}
                            </span>
                            {event.payload?.eventName && (
                              <span className="rounded-md bg-fg-muted/10 px-2 py-1 text-2xs font-semibold text-fg-muted">
                                {event.payload.eventName}
                              </span>
                            )}
                          </div>
                          {event.time && (
                            <time className="text-xs text-fg-subtle">{event.time}</time>
                          )}
                        </div>
                        <div className="p-4">
                          <ESMRecordDetails event={event} query={query} />
                        </div>
                      </article>
                    )
                  })}
                </div>
              )}
            </div>
          </RadixDialog.Content>
        </RadixDialog.Portal>
      </RadixDialog.Root>
    </>
  )
}

export const ServiceNode = memo(function ServiceNode({ data }: NodeProps) {
  const {
    service,
    label,
    streamEnabled,
    eventCount,
    writeCount,
    writeBurstCount,
    isNew,
    approximateNumberOfMessages,
    approximateNumberOfMessagesNotVisible,
    onPeekStream,
    status,
    protocolType,
    routeCount,
    stageCount,
    authenticationType,
    dataSourceCount,
    resolverCount,
    ecsResourceType,
    clusterName,
    taskId,
    desiredCount,
    runningCount,
    scope,
    ruleCount,
  } = data as ServiceNodeData
  const { esmId, filterPatterns = [] } = data as ServiceNodeData

  // Capture isNew at mount time only — never re-triggers on subsequent renders
  const [animated] = useState(() => !!isNew)
  // useMemo so the class string is stable after first render
  const enterClass = useMemo(() => (animated ? "overcast-node-enter" : ""), [animated])

  // Selected message for the detail modal (SQS only)
  const [selectedMsg, setSelectedMsg] = useState<SQSMessage | null>(null)

  // Action dialog state
  const [sendOpen, setSendOpen] = useState(false)
  const [publishOpen, setPublishOpen] = useState(false)
  const [testOpen, setTestOpen] = useState(false)

  const sqsMessages = useSqsEventMessages(service === "sqs" ? label : "")
  const { events: sqsEvents } = useEventStream({ source: "sqs" })

  const navigate = useNavigate()
  const endpoint = useEndpoint()
  const nodeId = useNodeId()
  const route = nodeRoute({
    service,
    label,
    nodeId: nodeId ?? undefined,
    protocolType,
    ecsResourceType,
    clusterName,
    taskId,
    scope,
  })
  const nodeRegion = (data as ServiceNodeData).region
  const handleClick = useCallback(() => {
    if (!route) return
    // Switch region first if this resource lives in a different region.
    if (nodeRegion && nodeRegion !== endpoint.region) {
      endpointStore.set({ ...endpoint, region: nodeRegion })
    }
    void navigate({ to: route.to, params: route.params, search: route.search })
  }, [navigate, route, nodeRegion, endpoint])
  const handleMouseDown = useCallback(
    (e: React.MouseEvent) => {
      if (!route || e.button !== 1) return
      e.preventDefault()
      e.stopPropagation()
      openRouteInNewTab(route, { region: nodeRegion ?? endpoint.region })
    },
    [route, nodeRegion, endpoint.region],
  )

  const { hasTarget, hasSource } = data as ServiceNodeData

  const meta =
    service === "esm-filter"
      ? {
          color: "text-cat-3",
          bg: "bg-cat-3/10",
          border: "border-cat-3/30",
          css: "var(--cat-3)",
          letter: "F",
        }
      : (SERVICE_THEME[service] ?? {
          color: "text-fg-muted",
          bg: "bg-fg-muted/10",
          border: "border-fg-muted/30",
          css: FALLBACK_COLOR,
          letter: "?",
        })
  const Icon =
    service === "esm-filter"
      ? Filter
      : ((SERVICES as Record<string, (typeof SERVICES)[keyof typeof SERVICES] | undefined>)[service]
          ?.icon ?? Box)
  const actionButtonClass = {
    sqs: "hover:bg-cat-3/15 hover:text-cat-3",
    sns: "hover:bg-cat-10/15 hover:text-cat-10",
    lambda: "hover:bg-cat-9/15 hover:text-cat-9",
  }[service]

  const {
    messages: visualMessages,
    visibleCount: liveVisibleCount,
    inFlightCount: liveInFlightCount,
  } = useSqsVisualMessages(label, sqsMessages, sqsEvents)
  const visibleCount = service === "sqs" ? liveVisibleCount : (approximateNumberOfMessages ?? 0)
  const inFlightCount =
    service === "sqs" ? liveInFlightCount : (approximateNumberOfMessagesNotVisible ?? 0)
  const totalMsgs = service === "sqs" ? visualMessages.length : visibleCount + inFlightCount
  const showMsgList = service === "sqs" && totalMsgs > 0

  if (service === "esm-filter" && esmId) {
    return (
      <div className={cn("relative h-full w-full", enterClass)}>
        {(eventCount ?? 0) > 0 && (
          <span
            key={eventCount}
            aria-hidden
            className="pointer-events-none absolute -inset-1 rounded-full ring-2 ring-cat-3"
            style={{ animation: `overcastPulseRing ${PULSE_TTL}ms ease-out forwards` }}
          />
        )}
        {hasTarget && (
          <Handle
            type="target"
            position={Position.Left}
            className="size-2! rounded-full! border-0! bg-cat-3/70!"
          />
        )}
        <ESMFilterPanel esmId={esmId} patterns={filterPatterns} />
        {hasSource && (
          <Handle
            type="source"
            position={Position.Right}
            className="size-2! rounded-full! border-0! bg-cat-3/70!"
          />
        )}
      </div>
    )
  }

  // The card is not itself a button. It holds the drawer triggers, the log peek and
  // React Flow's own connection handles, and an element with a widget role may not
  // contain focusable descendants — a screen reader announces one control where there
  // are four, and Tab lands inside something already claiming to be a button. The whole
  // surface still navigates on click, which is what a canvas node wants from a pointer;
  // the keyboard route is the node's title, which is a real <button> below.
  return (
    <div
      onClick={handleClick}
      onMouseDown={handleMouseDown}
      className={cn(
        "relative flex flex-col rounded-lg border px-3 py-2 shadow-sm transition-colors",
        "bg-bg-elevated text-fg",
        meta.border,
        route && "cursor-pointer hover:brightness-110",
        enterClass,
      )}
    >
      {/* Ring pulse — keyed by eventCount to re-trigger animation on each event */}
      {(eventCount ?? 0) > 0 && (
        <span
          key={eventCount}
          aria-hidden
          className="pointer-events-none absolute -inset-0.5 rounded-lg"
          style={{
            boxShadow: `0 0 0 2px ${meta.css}`,
            animation: `overcastPulseRing ${PULSE_TTL}ms ease-out forwards`,
          }}
        />
      )}
      {/* Inbound-write sweep flash — keyed by writeCount to re-trigger animation */}
      {(writeCount ?? 0) > 0 && (
        <span
          key={writeCount}
          aria-hidden
          className="pointer-events-none absolute inset-0 rounded-lg"
          style={{
            background: `linear-gradient(90deg, transparent 0%, ${toSweep(meta.css)} 50%, transparent 100%)`,
            animation: `overcastSweep ${FLASH_TTL}ms ease-out forwards`,
          }}
        />
      )}
      {/* Left target handle — only when this node is a connection target */}
      {hasTarget && (
        <Handle
          type="target"
          position={Position.Left}
          className="size-2! rounded-full! border-0! bg-fg-muted/50!"
        />
      )}

      {/* Header row: icon + label + stats + stream indicator */}
      <div className="flex items-center gap-2.5">
        <div
          className={cn("flex h-9 w-9 shrink-0 items-center justify-center rounded-md", meta.bg)}
        >
          <Icon className={cn("h-5 w-5", meta.color)} />
        </div>

        <div className="min-w-0 flex-1">
          <Tooltip content={<span className="break-all">{label}</span>}>
            {route ? (
              <button
                type="button"
                onClick={(event) => {
                  event.stopPropagation()
                  handleClick()
                }}
                className="block w-full truncate text-left text-base leading-tight font-semibold hover:underline"
              >
                {label}
              </button>
            ) : (
              <p className="truncate text-base leading-tight font-semibold">{label}</p>
            )}
          </Tooltip>
          {service === "sqs" ? (
            <SqsStatsBar visible={visibleCount} inFlight={inFlightCount} />
          ) : service === "rds" && status ? (
            <RdsStatusBadge status={status} />
          ) : service === "apigateway" ? (
            <div className="flex items-center gap-1.5">
              <span className="rounded bg-fg-muted/15 px-1.5 py-0.5 font-mono text-2xs font-semibold text-fg-muted uppercase">
                {protocolType ?? "API"}
              </span>
              {routeCount != null && (
                <span className="text-xs text-fg-subtle">
                  {routeCount} {routeCount === 1 ? "route" : "routes"}
                </span>
              )}
              {stageCount != null && stageCount > 0 && (
                <span className="text-xs text-fg-subtle">
                  &middot; {stageCount} {stageCount === 1 ? "stage" : "stages"}
                </span>
              )}
            </div>
          ) : service === "appsync" ? (
            <div className="flex items-center gap-1.5">
              <span className="rounded bg-fg-muted/15 px-1.5 py-0.5 font-mono text-2xs font-semibold text-fg-muted uppercase">
                {authenticationType ?? "GraphQL"}
              </span>
              {dataSourceCount != null && (
                <span className="text-xs text-fg-subtle">
                  {dataSourceCount} {dataSourceCount === 1 ? "source" : "sources"}
                </span>
              )}
              {resolverCount != null && resolverCount > 0 && (
                <span className="text-xs text-fg-subtle">
                  &middot; {resolverCount} {resolverCount === 1 ? "resolver" : "resolvers"}
                </span>
              )}
            </div>
          ) : service === "waf" ? (
            <div className="flex items-center gap-1.5">
              <span className="rounded bg-fg-muted/15 px-1.5 py-0.5 font-mono text-2xs font-semibold text-fg-muted uppercase">
                {scope ?? "WAF"}
              </span>
              {ruleCount != null && (
                <span className="text-xs text-fg-subtle">
                  {ruleCount} {ruleCount === 1 ? "stored rule" : "stored rules"}
                </span>
              )}
            </div>
          ) : service === "ecs" ? (
            <div className="flex items-center gap-1.5 text-xs text-fg-subtle">
              <span>ECS {ecsResourceType ?? "resource"}</span>
              {ecsResourceType === "service" && desiredCount != null && runningCount != null && (
                <>
                  <span aria-hidden="true">&middot;</span>
                  <span>
                    {runningCount}/{desiredCount} running
                  </span>
                </>
              )}
              {(ecsResourceType === "task" || ecsResourceType === "cluster") && status && (
                <>
                  <span aria-hidden="true">&middot;</span>
                  <span>{status}</span>
                </>
              )}
            </div>
          ) : service === "esm-filter" ? (
            <p className="font-mono text-sm leading-tight text-cat-3">DynamoDB stream filter</p>
          ) : (
            <p className="text-sm leading-tight text-fg-subtle capitalize">{service}</p>
          )}
        </div>

        {/* Right side: stream indicator + action button */}
        <div className="flex shrink-0 flex-col items-center gap-1">
          {streamEnabled && (
            <div className="h-1.5 w-1.5 rounded-full bg-cat-7" title="Streams enabled" />
          )}
          {(service === "sqs" || service === "sns" || service === "lambda") && (
            <button
              type="button"
              onKeyDown={(e) => e.stopPropagation()}
              onClick={(e) => {
                e.stopPropagation()
                if (service === "sqs") setSendOpen(true)
                else if (service === "sns") setPublishOpen(true)
                else setTestOpen(true)
              }}
              className={cn(
                "flex h-5 w-5 items-center justify-center rounded text-fg-muted transition-colors",
                actionButtonClass,
              )}
              title={
                service === "lambda"
                  ? "Test function"
                  : service === "sns"
                    ? "Publish message"
                    : "Send message"
              }
            >
              {service === "lambda" ? <Play className="h-3 w-3" /> : <Send className="h-3 w-3" />}
            </button>
          )}
          {service === "ecr" && (data as ServiceNodeData).repositoryUri && (
            <CopyButton
              value={(data as ServiceNodeData).repositoryUri!}
              noun="repository URI"
              onKeyDown={(e) => e.stopPropagation()}
              className="h-5 w-5 rounded text-fg-muted hover:bg-cat-1/15 hover:text-cat-1"
            />
          )}
        </div>
      </div>

      {/* Message list — SQS only, shown when queue has messages */}
      {showMsgList && (
        <SqsMessageList messages={visualMessages} onSelect={(msg) => setSelectedMsg(msg)} />
      )}

      {/* Stream list — CloudWatch Logs only */}
      {service === "logs" && (
        <LogStreamList
          groupName={label}
          onSelect={(stream) =>
            onPeekStream?.({
              title: label,
              subtitle: stream.logStreamName ?? "",
              logGroup: label,
              logStream: stream.logStreamName ?? "",
            })
          }
        />
      )}

      {/* Event counter badge */}
      {(eventCount ?? 0) > 0 && (
        <span
          className={cn(
            "absolute -top-1.5 -right-1.5 flex h-4 min-w-4 items-center justify-center rounded-full px-1 font-mono text-2xs font-bold tabular-nums",
            "bg-accent text-fg-on-accent",
          )}
        >
          {(eventCount ?? 0) > 99 ? "99+" : eventCount}
        </span>
      )}

      {/* Write-burst badge (S3 / DynamoDB) — drains over time so rapid writes stay visible */}
      {(writeBurstCount ?? 0) > 1 && (
        <span
          className={cn(
            "absolute -right-1.5 -bottom-1.5 flex h-4 min-w-4 items-center justify-center rounded-full px-1 font-mono text-2xs font-bold tabular-nums",
            "border border-current bg-bg-elevated",
            meta.color,
          )}
          title={`${writeBurstCount} recent writes`}
        >
          ×{writeBurstCount}
        </span>
      )}

      {/* Right source handle — only when this node has outgoing connections */}
      {hasSource && (
        <Handle
          type="source"
          position={Position.Right}
          className="size-2! rounded-full! border-0! bg-fg-muted/50!"
        />
      )}

      {/* Message detail modal — rendered via Radix portal, DOM position doesn't matter */}
      {service === "sqs" && (
        <SqsMessageModal
          msg={selectedMsg}
          queueName={label}
          open={selectedMsg !== null}
          onClose={() => setSelectedMsg(null)}
        />
      )}

      {/* Action dialogs — portaled outside the node DOM tree */}
      {service === "sqs" && (
        <SendMessageDialog
          queueName={label}
          isFifo={label.endsWith(".fifo")}
          open={sendOpen}
          onClose={() => setSendOpen(false)}
        />
      )}
      {service === "sns" && (
        <PublishMessageDialog topicName={label} open={publishOpen} onOpenChange={setPublishOpen} />
      )}
      {service === "lambda" && (
        <LambdaInvokeDialog name={label} open={testOpen} onOpenChange={setTestOpen} />
      )}
    </div>
  )
}, areServiceNodePropsEqual)

// ─── LambdaGroupNode ────────────────────────────────────────────────────────

export interface LambdaGroupNodeData extends Record<string, unknown> {
  label: string
  eventCount?: number
  onPeek?: (target: LogStreamTarget) => void
}

interface LambdaInstanceEventPayload {
  instanceId: string
  functionName: string
  status: "running" | "idle"
  startedAt: number
  lastUsed: number
  expiresAt: number
  logGroup?: string
  logStream?: string
  triggerEvent?: unknown
  lastInvocationStatus?: "succeeded" | "failed"
  lastInvocationError?: string
  initOrigin?: LambdaInstance["initOrigin"]
  /** Only present on the `lambda:InstanceEvicted` payload. */
  evictedReason?: LambdaInstance["evictedReason"]
}

function normalizeTriggerEvent(value: unknown): string | undefined {
  if (value == null) return undefined
  if (typeof value === "string") return value
  try {
    return JSON.stringify(value)
  } catch {
    return undefined
  }
}

/** Ordering rank for the instance list: busy environments first. */
function instanceSortRank(instance: LambdaInstance): number {
  switch (instance.status) {
    case "running":
      return 0
    case "initializing":
      return 1
    case "starting":
      return 2
    default:
      // Idle. Provisioned capacity sorts last — it is always there, so it is
      // the least interesting thing to look at.
      return instance.provisioned ? 4 : 3
  }
}

export interface LambdaInstanceSummary {
  total: number
  active: number
  idle: number
  provisioned: number
}

function summarizeInstances(instances: LambdaInstance[]): LambdaInstanceSummary {
  let active = 0
  let idle = 0
  let provisioned = 0
  for (const i of instances) {
    if (i.status === "idle") idle++
    else active++
    if (i.provisioned) provisioned++
  }
  return { total: instances.length, active, idle, provisioned }
}

/**
 * Concurrency summary shown under the function name. Replaces a static
 * "lambda" label that said nothing — with several environments running, the
 * useful information is how many there are and what they are doing.
 */
function LambdaConcurrencySummary({ summary }: { summary: LambdaInstanceSummary }) {
  if (summary.total === 0) {
    return <p className="font-mono text-sm leading-tight text-fg-subtle">no instances</p>
  }
  return (
    <p className="flex items-center gap-1.5 font-mono text-sm leading-tight text-fg-subtle">
      <span title={`${summary.total} execution environment${summary.total === 1 ? "" : "s"}`}>
        {summary.total}&times;
      </span>
      {summary.active > 0 && (
        <span className="flex items-center gap-1 text-success" title="running or starting">
          <span className="h-1.5 w-1.5 rounded-full bg-success" />
          {summary.active}
        </span>
      )}
      {summary.idle > 0 && (
        <span className="flex items-center gap-1 text-fg-muted" title="idle and reusable">
          <span className="h-1.5 w-1.5 rounded-full bg-fg-muted/50" />
          {summary.idle}
        </span>
      )}
      {summary.provisioned > 0 && (
        <span className="flex items-center gap-1 text-cat-9" title="provisioned concurrency">
          <span className="h-1.5 w-1.5 rounded-full bg-cat-9" />
          {summary.provisioned}
        </span>
      )}
    </p>
  )
}

/**
 * LambdaGroupNode — container node that renders its function's live +
 * ghost instances as a bounded, internally-scrollable list.
 *
 * Instances are fetched directly by this component (mirroring how
 * ServiceNode fetches its own SQS messages/log streams) rather than passed
 * down via `data`, so per-instance updates don't depend on this node's memo
 * comparator — see areLambdaGroupPropsEqual below.
 */
function areLambdaGroupPropsEqual(prev: NodeProps, next: NodeProps): boolean {
  if (prev.selected !== next.selected) return false
  const pd = prev.data as LambdaGroupNodeData
  const nd = next.data as LambdaGroupNodeData
  return pd.label === nd.label && pd.eventCount === nd.eventCount
}

export const LambdaGroupNode = memo(function LambdaGroupNode({ data }: NodeProps) {
  const { label, eventCount, onPeek } = data as LambdaGroupNodeData
  const navigate = useNavigate()
  const [testOpen, setTestOpen] = useState(false)
  const [showInvocations, setShowInvocations] = useState(false)
  const [invocations, setInvocations] = useState<Invocation[]>([])
  const { events: lambdaEvents } = useEventStream({ source: "lambda" })

  // Instances are fetched here (not via `data`) so this node can render its
  // own bounded, scrollable list independent of the parent's memoization —
  // same pattern ServiceNode uses for SQS messages / log streams.
  const instancesByFunction = useLambdaInstances()
  const liveInstances = useMemo(
    () => instancesByFunction[label] ?? [],
    [instancesByFunction, label],
  )
  const ghostInstances = useGhostTracker({
    items: liveInstances,
    getKey: (i) => i.instanceId,
    ttl: LAMBDA_GHOST_TTL,
  })
  const allInstances = useMemo(() => {
    const ghosts = [...ghostInstances.values()]
      .filter((g) => !liveInstances.some((li) => li.instanceId === g.item.instanceId))
      .sort((a, b) => a.deletedAt - b.deletedAt)
    // Only LAMBDA_GROUP_MAX_VISIBLE rows fit before the list scrolls, so order
    // by what someone watching the map wants to see first: environments doing
    // work, then ones warming up, then idle capacity, then fading ghosts.
    const live = [...liveInstances].sort(
      (a, b) =>
        instanceSortRank(a) - instanceSortRank(b) || a.instanceId.localeCompare(b.instanceId),
    )
    return [
      ...live.map((i) => ({ instance: i, isGhost: false, deletedAt: 0 })),
      ...ghosts.map((g) => ({ instance: g.item, isGhost: true, deletedAt: g.deletedAt })),
    ]
  }, [liveInstances, ghostInstances])
  const instanceSummary = useMemo(() => summarizeInstances(liveInstances), [liveInstances])

  // Eviction reasons only ride the `lambda:InstanceEvicted` SSE event, not the
  // instance list, so they're captured here (keyed by instanceId) and handed
  // to each ghost card as a prop. Pruned below once an instanceId is neither
  // live nor a ghost any more, so this can't grow without bound.
  const evictedReasonsRef = useRef<Map<string, LambdaInstance["evictedReason"]>>(new Map())
  const [evictedReasons, setEvictedReasons] = useState<
    Map<string, LambdaInstance["evictedReason"]>
  >(new Map())

  useEffect(() => {
    const keep = new Set<string>([
      ...liveInstances.map((i) => i.instanceId),
      ...ghostInstances.keys(),
    ])
    let changed = false
    for (const key of evictedReasonsRef.current.keys()) {
      if (!keep.has(key)) {
        evictedReasonsRef.current.delete(key)
        changed = true
      }
    }
    if (changed) setEvictedReasons(new Map(evictedReasonsRef.current))
  }, [liveInstances, ghostInstances])

  const eventCursorRef = useRef(0)
  const activeByInstanceRef = useRef<Map<string, string[]>>(new Map())
  const invocationsRef = useRef<Map<string, Invocation>>(new Map())
  const route = useMemo<NodeRoute>(
    () => ({ to: "/lambda/$name", params: { name: label } }),
    [label],
  )
  const handleOpenFunction = useCallback(() => {
    void navigate({ to: route.to, params: route.params })
  }, [navigate, route])
  const handleFunctionMouseDown = useCallback(
    (e: React.MouseEvent) => {
      if (e.button !== 1) return
      e.preventDefault()
      e.stopPropagation()
      openRouteInNewTab(route)
    },
    [route],
  )

  useEffect(() => {
    if (lambdaEvents.length < eventCursorRef.current) eventCursorRef.current = 0

    let sawEviction = false

    for (let i = eventCursorRef.current; i < lambdaEvents.length; i++) {
      const ev = lambdaEvents[i]
      if (
        ev.type !== EventType.lambda.InstanceAcquired &&
        ev.type !== EventType.lambda.InstanceReleased &&
        ev.type !== EventType.lambda.InstanceEvicted
      ) {
        continue
      }

      const payload = ev.payload as LambdaInstanceEventPayload | undefined
      if (!payload || payload.functionName !== label) continue

      if (ev.type === EventType.lambda.InstanceEvicted) {
        if (payload.evictedReason) {
          evictedReasonsRef.current.set(payload.instanceId, payload.evictedReason)
          sawEviction = true
        }
        continue
      }

      const t = Date.parse(ev.time)
      if (!Number.isFinite(t)) continue

      if (ev.type === EventType.lambda.InstanceAcquired) {
        const key = `${payload.instanceId}:${t}`
        const inv: Invocation = {
          key,
          acquiredAt: ev.time,
          instanceId: payload.instanceId,
          triggerEvent: normalizeTriggerEvent(payload.triggerEvent),
          logGroup: payload.logGroup,
          logStream: payload.logStream,
        }
        invocationsRef.current.set(key, inv)
        const list = activeByInstanceRef.current.get(payload.instanceId) ?? []
        list.push(key)
        activeByInstanceRef.current.set(payload.instanceId, list)
      } else {
        const list = activeByInstanceRef.current.get(payload.instanceId)
        const key = list?.shift()
        if (list && list.length === 0) activeByInstanceRef.current.delete(payload.instanceId)
        if (!key) continue
        const existing = invocationsRef.current.get(key)
        if (!existing) continue
        invocationsRef.current.set(key, {
          ...existing,
          releasedAt: ev.time,
          durationMs: Math.max(0, t - Date.parse(existing.acquiredAt)),
          outcomeStatus: payload.lastInvocationStatus,
          outcomeReason: payload.lastInvocationError,
          logGroup: payload.logGroup ?? existing.logGroup,
          logStream: payload.logStream ?? existing.logStream,
        })
      }
    }

    eventCursorRef.current = lambdaEvents.length

    const next = Array.from(invocationsRef.current.values()).sort(
      (a, b) => Date.parse(b.acquiredAt) - Date.parse(a.acquiredAt),
    )
    setInvocations(next)
    if (sawEviction) setEvictedReasons(new Map(evictedReasonsRef.current))
  }, [lambdaEvents, label])

  return (
    <div
      className={cn(
        "relative flex flex-col rounded-lg border shadow-sm",
        "border-cat-9/30 bg-bg-elevated",
      )}
      style={{ width: "100%", height: "100%" }}
    >
      {/* Left target handle */}
      <Handle
        type="target"
        position={Position.Left}
        className="size-2! rounded-full! border-0! bg-fg-muted/50!"
      />

      {/* Header */}
      <div
        className="flex items-center gap-2 rounded-t-lg px-3 py-2"
        style={{ height: LAMBDA_GROUP_HEADER_H }}
      >
        <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-cat-9/10">
          <Zap className="h-5 w-5 text-cat-9" />
        </div>
        <div className="min-w-0 flex-1">
          <Tooltip content={<span className="break-all">{label}</span>}>
            <button
              type="button"
              onClick={handleOpenFunction}
              onMouseDown={handleFunctionMouseDown}
              className="block max-w-full truncate text-left text-base leading-tight font-semibold hover:text-cat-9 hover:underline"
              title={`Open ${label}`}
            >
              {label}
            </button>
          </Tooltip>
          <LambdaConcurrencySummary summary={instanceSummary} />
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <button
            type="button"
            onKeyDown={(e) => e.stopPropagation()}
            onClick={(e) => {
              e.stopPropagation()
              setTestOpen(true)
            }}
            className="flex h-5 w-5 items-center justify-center rounded text-fg-muted transition-colors hover:bg-cat-9/15 hover:text-cat-9"
            title="Test function"
          >
            <Play className="h-3 w-3" />
          </button>
          <button
            type="button"
            onKeyDown={(e) => e.stopPropagation()}
            onClick={(e) => {
              e.stopPropagation()
              setShowInvocations(true)
            }}
            className="flex h-5 w-5 items-center justify-center rounded text-fg-muted transition-colors hover:bg-accent-muted hover:text-accent"
            title={`${invocations.length} invocations`}
          >
            <Clock className="h-3 w-3" />
          </button>
        </div>
      </div>

      {/* Divider */}
      <div className="mx-2 border-t border-cat-9/20" />

      {/* Instance list — fixed row height, scrolls internally once more than
          LAMBDA_GROUP_MAX_VISIBLE instances are running/fading out. The
          `nowheel` class stops scroll wheel input here from also zooming
          the canvas. */}
      <div className="map-detail nowheel min-h-0 flex-1 overflow-y-auto px-2">
        {allInstances.map(({ instance, isGhost, deletedAt }) => (
          <LambdaInstanceCard
            key={instance.instanceId}
            instance={instance}
            isGhost={isGhost}
            deletedAt={deletedAt}
            onPeek={isGhost ? undefined : onPeek}
            evictedReason={isGhost ? evictedReasons.get(instance.instanceId) : undefined}
          />
        ))}
      </div>
      {allInstances.length > LAMBDA_GROUP_MAX_VISIBLE && (
        <div className="map-detail pointer-events-none absolute right-2 bottom-1 rounded bg-bg-elevated/90 px-1 font-mono text-2xs text-fg-muted">
          +{allInstances.length - LAMBDA_GROUP_MAX_VISIBLE} more · scroll
        </div>
      )}

      {/* Event counter badge */}
      {(eventCount ?? 0) > 0 && (
        <span
          className={cn(
            "absolute -top-1.5 -right-1.5 flex h-4 min-w-4 items-center justify-center rounded-full px-1 font-mono text-2xs font-bold tabular-nums",
            "bg-accent text-fg-on-accent",
          )}
        >
          {(eventCount ?? 0) > 99 ? "99+" : eventCount}
        </span>
      )}

      {/* Right source handle */}
      <Handle
        type="source"
        position={Position.Right}
        className="size-2! rounded-full! border-0! bg-fg-muted/50!"
      />

      <LambdaInvokeDialog name={label} open={testOpen} onOpenChange={setTestOpen} />
      <LambdaInvocationsDrawer
        open={showInvocations}
        onOpenChange={setShowInvocations}
        invocations={invocations}
        functionName={label}
      />
    </div>
  )
}, areLambdaGroupPropsEqual)
