/**
 * The state machine as a flow diagram — and, given an execution trace, a live
 * view of that execution moving through it.
 *
 * Built on React Flow for pan, zoom and the minimap, but every position and
 * every edge route comes from `graph-layout`: nodes are not draggable, because
 * the diagram is a reading of the definition, not an editor of it, and a
 * dragged node would detach from the routes drawn to it.
 */
import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react"
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  BackgroundVariant,
  Controls,
  EdgeLabelRenderer,
  Handle,
  MiniMap,
  Panel,
  Position,
  useReactFlow,
  type Edge,
  type EdgeProps,
  type Node,
  type NodeProps,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { ChevronLeft, ChevronRight, LocateFixed, RotateCcw, X } from "lucide-react"
import { cn } from "@/lib/utils"
import { Tooltip } from "@/components/ui/tooltip"
import type { AslModel, AslState } from "../asl"
import {
  END_ID,
  START_ID,
  arrowHead,
  layoutModel,
  roundedPath,
  shortLabel,
  type LayoutEdgeKind,
  type Point,
} from "../graph-layout"
import {
  formatDuration,
  runDuration,
  type ExecutionTrace,
  type IterationSelection,
  type NodeStatus,
  type NodeSummary,
} from "../execution-trace"
import { computeDiagramState, type EdgeState } from "../diagram-state"
import { laneLabel, pillStatus } from "../diagram-labels"
import { ExportMenu } from "./export-menu"
import { LEGEND_STATUSES, STATUS_THEME, stateTypeTheme } from "../state-theme"
import { formatQuantity } from "@/lib/format"

// ─── Node and edge data ──────────────────────────────────────────────────────

interface StateNodeData extends Record<string, unknown> {
  state: AslState
  summary?: NodeSummary
  selected: boolean
  dimmed: boolean
  now: number
}

interface ContainerNodeData extends StateNodeData {
  lanes: Array<{
    scopeId: string
    x: number
    y: number
    width: number
    height: number
    status: NodeStatus
    label: string
  }>
  /** Map only: item count and per-iteration status of the run on show. */
  iterations?: Array<{ index: number; status: string }>
  itemCount?: number
  selectedIteration?: number
  onIterationChange?: (index: number | undefined) => void
}

interface PillNodeData extends Record<string, unknown> {
  label: string
  status: NodeStatus
  dimmed: boolean
}

interface FlowEdgeData extends Record<string, unknown> {
  points: Point[]
  kind: LayoutEdgeKind
  label?: string
  labelPosition?: Point
  state: EdgeState
  count: number
  /** Changes whenever this edge becomes the newest move, replaying the particle. */
  pulseKey?: number
}

// ─── Nodes ───────────────────────────────────────────────────────────────────

const hiddenHandle = "!pointer-events-none !h-px !w-px !min-h-0 !min-w-0 !border-0 !bg-transparent"

function StatusGlyph({ status, className }: { status: NodeStatus; className?: string }) {
  const theme = STATUS_THEME[status]
  const Icon = theme.icon
  if (!Icon) return null
  return (
    <Icon
      aria-label={theme.label}
      className={cn("h-4 w-4 shrink-0", status === "running" && "animate-spin", className)}
      style={{ color: theme.color }}
    />
  )
}

function borderFor(summary: NodeSummary | undefined): { color: string; width: number } {
  if (!summary || summary.status === "idle") return { color: "var(--border)", width: 1 }
  return { color: STATUS_THEME[summary.status].color, width: 2 }
}

const StateNode = memo(function StateNode({ data }: NodeProps<Node<StateNodeData>>) {
  const { state, summary, selected, dimmed, now } = data
  const theme = stateTypeTheme(state.type)
  const Icon = theme.icon
  const border = borderFor(summary)
  const focus = summary?.focusRun
  const runs = summary?.runs.length ?? 0
  const retries = focus ? Math.max(0, focus.attempts - 1) : 0

  return (
    <div
      className={cn(
        "relative flex h-full w-full cursor-pointer items-center gap-2.5 rounded-lg bg-bg-elevated px-2.5 shadow-sm",
        "transition-[opacity,box-shadow,border-color] duration-200 hover:shadow-md",
        summary?.status === "running" && "oc-sfn-pulse",
        selected && "ring-2 ring-accent ring-offset-2 ring-offset-bg",
        dimmed && "opacity-45 hover:opacity-80",
      )}
      style={{ border: `${border.width}px solid ${border.color}` }}
    >
      <Handle
        type="target"
        position={Position.Top}
        isConnectable={false}
        className={hiddenHandle}
      />
      <span
        className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md"
        style={{
          background: `color-mix(in oklab, ${theme.color} 15%, transparent)`,
          color: theme.color,
        }}
      >
        <Icon className="h-4 w-4" />
      </span>
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-xs font-semibold text-fg" title={state.name}>
          {state.name}
        </span>
        <span className="truncate text-2xs text-fg-muted" title={state.summary}>
          {state.summary}
        </span>
      </div>
      {summary && summary.status !== "idle" && (
        <div className="flex shrink-0 flex-col items-end gap-0.5">
          <StatusGlyph status={summary.status} />
          {focus && (
            <span className="font-mono text-2xs text-fg-subtle tabular-nums">
              {formatDuration(runDuration(focus, now))}
            </span>
          )}
        </div>
      )}
      {(runs > 1 || retries > 0) && (
        <div className="absolute -top-2.5 right-2 flex gap-1">
          {retries > 0 && (
            <span
              className="rounded-full border border-warning/40 bg-warning-muted px-1.5 font-mono text-2xs leading-4 text-warning"
              title={formatQuantity(retries, "retry", "retries")}
            >
              ↻{retries}
            </span>
          )}
          {runs > 1 && (
            <span
              className="rounded-full border border-border bg-bg-elevated px-1.5 font-mono text-2xs leading-4 text-fg-muted tabular-nums"
              title={`${summary?.succeeded ?? 0} succeeded, ${summary?.failed ?? 0} failed, ${summary?.running ?? 0} running`}
            >
              ×{runs}
            </span>
          )}
        </div>
      )}
      <Handle
        type="source"
        position={Position.Bottom}
        isConnectable={false}
        className={hiddenHandle}
      />
    </div>
  )
})

/** Segmented progress for a Map: one cell per item, or proportional bars when there are too many. */
function IterationStrip({ data }: { data: ContainerNodeData }) {
  const { iterations = [], itemCount, selectedIteration, onIterationChange } = data
  const total = Math.max(itemCount ?? 0, iterations.length)
  if (total === 0) return null
  const byIndex = new Map(iterations.map((i) => [i.index, i.status]))
  const done = iterations.filter((i) => i.status === "succeeded").length
  const failed = iterations.filter((i) => i.status === "failed").length
  const colorOf = (status: string | undefined) =>
    status === "succeeded"
      ? "var(--success)"
      : status === "failed"
        ? "var(--danger)"
        : status === "running"
          ? "var(--accent)"
          : status === "aborted"
            ? "var(--warning)"
            : "var(--border)"

  const step = (delta: number) => {
    if (!onIterationChange) return
    const current = selectedIteration ?? (delta > 0 ? -1 : total)
    const next = current + delta
    onIterationChange(next < 0 || next >= total ? undefined : next)
  }

  return (
    <div className="nodrag nopan flex items-center gap-2" onClick={(e) => e.stopPropagation()}>
      <div
        className="flex min-w-0 flex-1 gap-px overflow-hidden rounded-sm"
        aria-label={`${done} of ${total} iterations succeeded`}
      >
        {total <= 60 ? (
          Array.from({ length: total }, (_, index) => (
            <button
              key={index}
              type="button"
              title={`Iteration ${index} — ${byIndex.get(index) ?? "pending"}`}
              aria-label={`Show iteration ${index}`}
              aria-pressed={selectedIteration === index}
              onClick={() => onIterationChange?.(selectedIteration === index ? undefined : index)}
              className={cn(
                "h-2 min-w-1 flex-1 cursor-pointer transition-opacity hover:opacity-70",
                selectedIteration !== undefined && selectedIteration !== index && "opacity-35",
              )}
              style={{ background: colorOf(byIndex.get(index)) }}
            />
          ))
        ) : (
          <div className="flex h-2 w-full bg-border">
            <div style={{ width: `${(done / total) * 100}%`, background: "var(--success)" }} />
            <div style={{ width: `${(failed / total) * 100}%`, background: "var(--danger)" }} />
          </div>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-0.5">
        <button
          type="button"
          className="rounded p-0.5 text-fg-muted hover:bg-bg-muted hover:text-fg"
          aria-label="Previous iteration"
          onClick={() => step(-1)}
        >
          <ChevronLeft className="h-3.5 w-3.5" />
        </button>
        <span className="min-w-14 text-center font-mono text-2xs text-fg-muted tabular-nums">
          {selectedIteration === undefined ? `all ${total}` : `#${selectedIteration} / ${total}`}
        </span>
        <button
          type="button"
          className="rounded p-0.5 text-fg-muted hover:bg-bg-muted hover:text-fg"
          aria-label="Next iteration"
          onClick={() => step(1)}
        >
          <ChevronRight className="h-3.5 w-3.5" />
        </button>
        {selectedIteration !== undefined && (
          <button
            type="button"
            className="rounded p-0.5 text-fg-muted hover:bg-bg-muted hover:text-fg"
            aria-label="Show all iterations"
            onClick={() => onIterationChange?.(undefined)}
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
    </div>
  )
}

const ContainerNode = memo(function ContainerNode({
  data,
  positionAbsoluteX,
  positionAbsoluteY,
}: NodeProps<Node<ContainerNodeData>>) {
  const { state, summary, selected, dimmed, lanes, now } = data
  const theme = stateTypeTheme(state.type)
  const Icon = theme.icon
  const border = borderFor(summary)
  const focus = summary?.focusRun
  const isMap = state.type === "Map"

  return (
    <div
      className={cn(
        "relative h-full w-full rounded-xl transition-[opacity,box-shadow] duration-200",
        selected && "ring-2 ring-accent ring-offset-2 ring-offset-bg",
        dimmed && "opacity-60",
      )}
      style={{
        border: `${border.width === 1 ? 1.5 : 2}px dashed ${border.width === 1 ? `color-mix(in oklab, ${theme.color} 55%, var(--border))` : border.color}`,
        background: `color-mix(in oklab, ${theme.color} 5%, transparent)`,
      }}
    >
      <Handle
        type="target"
        position={Position.Top}
        isConnectable={false}
        className={hiddenHandle}
      />
      <div
        className={cn(
          "flex h-13 cursor-pointer flex-col justify-center gap-1 rounded-t-xl px-3",
          summary?.status === "running" && "oc-sfn-pulse",
        )}
      >
        <div className="flex items-center gap-2">
          <span
            className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md"
            style={{
              background: `color-mix(in oklab, ${theme.color} 16%, transparent)`,
              color: theme.color,
            }}
          >
            <Icon className="h-3.5 w-3.5" />
          </span>
          <span
            className="max-w-[70%] shrink-0 truncate text-xs font-semibold text-fg"
            title={state.name}
          >
            {state.name}
          </span>
          <span className="min-w-0 flex-1 truncate text-2xs text-fg-muted" title={state.summary}>
            {state.summary}
          </span>
          <span className="flex shrink-0 items-center gap-1.5">
            {focus && (
              <span className="font-mono text-2xs text-fg-subtle tabular-nums">
                {formatDuration(runDuration(focus, now))}
              </span>
            )}
            {summary && summary.status !== "idle" && <StatusGlyph status={summary.status} />}
          </span>
        </div>
        {isMap && <IterationStrip data={data} />}
      </div>
      {lanes.map((lane) => (
        <div
          key={lane.scopeId}
          className="pointer-events-none absolute rounded-lg border bg-bg/40"
          style={{
            left: lane.x - positionAbsoluteX,
            top: lane.y - positionAbsoluteY,
            width: lane.width,
            height: lane.height,
            borderColor:
              lane.status === "idle"
                ? "color-mix(in oklab, var(--border) 70%, transparent)"
                : `color-mix(in oklab, ${STATUS_THEME[lane.status].color} 45%, transparent)`,
          }}
        >
          <span className="absolute -top-2 left-2 rounded bg-bg-elevated px-1 font-mono text-2xs tracking-wider text-fg-subtle uppercase">
            {lane.label}
          </span>
        </div>
      ))}
      <Handle
        type="source"
        position={Position.Bottom}
        isConnectable={false}
        className={hiddenHandle}
      />
    </div>
  )
})

const PillNode = memo(function PillNode({ data }: NodeProps<Node<PillNodeData>>) {
  const reached = data.status !== "idle"
  const color = reached ? STATUS_THEME[data.status].color : "var(--fg-subtle)"
  return (
    <div
      className={cn(
        "flex h-full w-full items-center justify-center rounded-full bg-bg-elevated font-mono text-2xs font-semibold tracking-widest uppercase",
        data.dimmed && "opacity-50",
      )}
      style={{ border: `${reached ? 2 : 1.5}px solid ${color}`, color }}
    >
      <Handle
        type="target"
        position={Position.Top}
        isConnectable={false}
        className={hiddenHandle}
      />
      {data.label}
      <Handle
        type="source"
        position={Position.Bottom}
        isConnectable={false}
        className={hiddenHandle}
      />
    </div>
  )
})

// ─── Edge ────────────────────────────────────────────────────────────────────

function edgeColor(kind: LayoutEdgeKind, state: EdgeState): string {
  switch (state) {
    case "active":
      return "var(--accent)"
    case "taken":
      return "var(--success)"
    case "catch":
      return "var(--warning)"
    case "idle":
      return "color-mix(in oklab, var(--fg-subtle) 45%, transparent)"
    default:
      return kind === "catch"
        ? "color-mix(in oklab, var(--danger) 70%, var(--fg-subtle))"
        : "var(--fg-subtle)"
  }
}

const FlowEdge = memo(function FlowEdge({ data }: EdgeProps<Edge<FlowEdgeData>>) {
  const particle = useRef<SVGAnimateMotionElement>(null)
  const fade = useRef<SVGAnimateElement>(null)
  const {
    points = [],
    kind = "next",
    label,
    labelPosition,
    state = "plain",
    count = 0,
    pulseKey,
  } = data ?? {}
  const path = useMemo(() => roundedPath(points), [points])
  const head = useMemo(() => arrowHead(points), [points])
  const color = edgeColor(kind, state)
  const emphasised = state === "taken" || state === "catch" || state === "active"
  const dashed = kind === "catch" || kind === "default"

  // SMIL animations inserted after the document started would compute as
  // already finished; begin them explicitly so each new move replays.
  useEffect(() => {
    if (pulseKey === undefined) return
    particle.current?.beginElement()
    fade.current?.beginElement()
  }, [pulseKey])

  const labelTone =
    kind === "catch"
      ? "text-danger border-danger/30"
      : kind === "default"
        ? "text-fg-subtle italic border-border"
        : "text-fg-muted border-border"

  return (
    <>
      {emphasised && (
        <path
          d={path}
          fill="none"
          stroke={color}
          strokeWidth={6}
          strokeOpacity={0.14}
          strokeLinecap="round"
        />
      )}
      <path
        d={path}
        fill="none"
        stroke={color}
        strokeWidth={emphasised ? 2 : 1.5}
        strokeDasharray={state === "active" ? undefined : dashed ? "5 4" : undefined}
        strokeLinejoin="round"
        className={cn("transition-[stroke] duration-300", state === "active" && "oc-sfn-march")}
      />
      {kind !== "fork" && (
        <path d={head} fill={color} stroke={color} strokeWidth={1} strokeLinejoin="round" />
      )}
      {pulseKey !== undefined && (
        <circle
          key={pulseKey}
          r={4.5}
          fill={color}
          opacity={0}
          style={{ filter: `drop-shadow(0 0 4px ${color})` }}
        >
          <animateMotion ref={particle} begin="indefinite" dur="0.75s" fill="freeze" path={path} />
          <animate
            ref={fade}
            attributeName="opacity"
            begin="indefinite"
            dur="0.9s"
            values="1;1;0"
            keyTimes="0;0.8;1"
            fill="freeze"
          />
        </circle>
      )}
      {label && labelPosition && (
        <EdgeLabelRenderer>
          <div
            className={cn(
              "nodrag nopan pointer-events-auto absolute max-w-60 truncate rounded-md border bg-bg-elevated px-1.5 py-px font-mono text-2xs leading-4 shadow-xs",
              labelTone,
              state === "idle" && "opacity-60",
              (state === "taken" || state === "catch") && "font-semibold",
            )}
            style={{
              transform: `translate(-50%, -50%) translate(${labelPosition.x}px, ${labelPosition.y}px)`,
            }}
            title={label}
          >
            {shortLabel(label)}
            {count > 1 && <span className="ml-1 text-fg-subtle">×{count}</span>}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
})

const NODE_TYPES = { state: StateNode, container: ContainerNode, pill: PillNode }

const FIT_OPTIONS = { padding: 0.12, maxZoom: 1 }

/**
 * React Flow's stock node hint tells a screen-reader user they can move and
 * delete nodes. This diagram is a read-only view of the definition, so say
 * what Enter actually does here instead.
 */
const ARIA_LABELS = {
  "node.a11yDescription.default": "Press enter or space to show this state's details.",
  "node.a11yDescription.keyboardDisabled": "Press enter or space to show this state's details.",
  "edge.a11yDescription.default": "A transition between two states.",
}
const EDGE_TYPES = { flow: FlowEdge }

// ─── Canvas ──────────────────────────────────────────────────────────────────

export interface FlowDiagramProps {
  model: AslModel
  trace?: ExecutionTrace
  /** The execution is still running: animate moves and allow following. */
  live?: boolean
  selectedState?: string
  onSelectState?: (name: string | undefined) => void
  iterationSelection?: IterationSelection
  onIterationSelectionChange?: (next: IterationSelection) => void
  /** Wall-clock "now" for the durations of running states. */
  now?: number
  className?: string
  /** Hide the minimap, legend and toolbar, for small previews. */
  compact?: boolean
  /** File name (without extension) for the export menu; no menu without it. */
  exportName?: string
  /** Heading printed on an exported image, e.g. the machine or execution name. */
  exportTitle?: string
}

export function FlowDiagram(props: FlowDiagramProps) {
  return (
    <ReactFlowProvider>
      <FlowCanvas {...props} />
    </ReactFlowProvider>
  )
}

function FlowCanvas({
  model,
  trace,
  live = false,
  selectedState,
  onSelectState,
  iterationSelection = {},
  onIterationSelectionChange,
  now = Date.now(),
  className,
  compact = false,
  exportName,
  exportTitle,
}: FlowDiagramProps) {
  const layout = useMemo(() => layoutModel(model), [model])
  const { setCenter, getZoom, fitView } = useReactFlow()
  const [follow, setFollow] = useState(true)
  const view = useMemo(
    () => computeDiagramState(model, layout, trace, iterationSelection),
    [model, layout, trace, iterationSelection],
  )
  const { hasTrace, summaries } = view

  // A new layout (the definition changed, e.g. while editing) re-frames the
  // view. Not on mount: `onInit` below frames the first render, and fitting
  // again a frame later would undo follow mode's centring on the running
  // state.
  const framedLayout = useRef(layout)
  useEffect(() => {
    if (framedLayout.current === layout) return
    framedLayout.current = layout
    const id = window.requestAnimationFrame(() => void fitView(FIT_OPTIONS))
    return () => window.cancelAnimationFrame(id)
  }, [layout, fitView])

  const setIteration = useCallback(
    (map: string, index: number | undefined) => {
      onIterationSelectionChange?.({ ...iterationSelection, [map]: index })
    },
    [iterationSelection, onIterationSelectionChange],
  )

  const nodes = useMemo<Node[]>(() => {
    const out: Node[] = []
    for (const n of layout.nodes) {
      const base = {
        id: n.id,
        position: { x: n.x, y: n.y },
        width: n.width,
        height: n.height,
        draggable: false,
        connectable: false,
        selectable: false,
      }
      if (n.kind === "start" || n.kind === "end") {
        const status = pillStatus(n.kind, view)
        out.push({
          ...base,
          type: "pill",
          zIndex: 2,
          data: {
            label: n.kind === "start" ? "Start" : "End",
            status,
            dimmed: hasTrace && status === "idle",
          } satisfies PillNodeData,
        })
        continue
      }
      const state = model.states.get(n.id)
      if (!state) continue
      const summary = trace ? summaries.get(n.id) : undefined
      const common: StateNodeData = {
        state,
        summary,
        selected: selectedState === n.id,
        dimmed: hasTrace && (summary?.status ?? "idle") === "idle",
        now,
      }
      if (n.kind === "container") {
        const focus = summary?.focusRun
        const lanes = (n.lanes ?? []).map((lane, i) => ({
          ...lane,
          status: view.laneStatus(lane.scopeId),
          label: laneLabel(state, i),
        }))
        out.push({
          ...base,
          type: "container",
          zIndex: n.depth * 3,
          data: {
            ...common,
            lanes,
            itemCount: focus?.itemCount,
            iterations: focus?.iterations ? [...focus.iterations.values()] : undefined,
            selectedIteration: iterationSelection[n.id],
            onIterationChange: (index: number | undefined) => setIteration(n.id, index),
          } satisfies ContainerNodeData,
        })
      } else {
        out.push({ ...base, type: "state", zIndex: n.depth * 3 + 2, data: common })
      }
    }
    return out
  }, [
    layout,
    model,
    trace,
    summaries,
    view,
    selectedState,
    hasTrace,
    now,
    iterationSelection,
    setIteration,
  ])

  const edges = useMemo<Edge[]>(() => {
    const out: Edge[] = []
    for (const e of layout.edges) {
      const edgeView = view.edges.get(e.id)
      // Fork and join edges hang off the container node itself.
      const source = e.sourceNode.startsWith("__fork__:")
        ? containerOf(model, e.sourceNode)
        : e.sourceNode
      const target = e.targetNode.startsWith("__join__:")
        ? containerOf(model, e.targetNode)
        : e.targetNode
      out.push({
        id: e.id,
        source,
        target,
        type: "flow",
        zIndex: e.depth * 3 + 1,
        selectable: false,
        focusable: false,
        data: {
          points: e.points,
          kind: e.kind,
          label: e.label,
          labelPosition: e.labelPosition,
          state: edgeView?.state ?? "plain",
          count: edgeView?.count ?? 0,
          pulseKey: live && edgeView?.newest ? view.newestMove : undefined,
        } satisfies FlowEdgeData,
      })
    }
    return out
  }, [layout, view, live, model])

  // ── Follow the running state ────────────────────────────────────────────
  const runningLeaf = useMemo(() => {
    if (!live) return undefined
    const running = layout.nodes.filter(
      (n) => n.kind === "state" && summaries.get(n.id)?.status === "running",
    )
    return running.sort((a, b) => b.depth - a.depth)[0]
  }, [live, layout, summaries])

  const centreOn = (
    node: { x: number; y: number; width: number; height: number },
    duration: number,
  ) =>
    void setCenter(node.x + node.width / 2, node.y + node.height / 2, {
      zoom: Math.max(getZoom(), 0.75),
      duration,
    })

  // The first framing happens in `onInit`, once React Flow's pan and zoom
  // exist — earlier, a fit or a centre is silently dropped — and here rather
  // than through the `fitView` prop, whose fit would land after follow mode's
  // centring and undo it. A live execution opens on its running state.
  const framed = useRef(false)
  const latest = useRef({ follow, runningLeaf })
  latest.current = { follow, runningLeaf }
  const onInit = useCallback(() => {
    framed.current = true
    const { follow: following, runningLeaf: leaf } = latest.current
    if (following && leaf) centreOn(leaf, 0)
    else void fitView(FIT_OPTIONS)
    // centreOn reads the viewport through stable React Flow accessors.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fitView])

  const followKey = runningLeaf?.id
  useEffect(() => {
    if (!framed.current || !follow || !runningLeaf) return
    centreOn(runningLeaf, 450)
    // Re-centre only when the running state changes, not on every poll.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [followKey, follow])

  const onNodeClick = useCallback(
    (_: unknown, node: Node) => {
      if (node.type === "pill") return onSelectState?.(undefined)
      onSelectState?.(node.id === selectedState ? undefined : node.id)
    },
    [onSelectState, selectedState],
  )

  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") return onSelectState?.(undefined)
    // Nodes are focusable for keyboard users; Enter or Space on one opens its
    // details, as a click does.
    if (event.key !== "Enter" && event.key !== " ") return
    const id = (event.target as HTMLElement).closest(".react-flow__node")?.getAttribute("data-id")
    if (!id || id === START_ID || id === END_ID) return
    event.preventDefault()
    onSelectState?.(id === selectedState ? undefined : id)
  }

  const showMiniMap = !compact && layout.nodes.length > 14

  return (
    <div className={cn("relative h-full w-full", className)} onKeyDown={onKeyDown}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={NODE_TYPES}
        edgeTypes={EDGE_TYPES}
        minZoom={0.15}
        maxZoom={2}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        edgesFocusable={false}
        onNodeClick={onNodeClick}
        onPaneClick={() => onSelectState?.(undefined)}
        onMoveStart={(event) => {
          // A pan or zoom by the reader, not by follow mode: stop following.
          if (event && live) setFollow(false)
        }}
        onInit={onInit}
        proOptions={{ hideAttribution: true }}
        ariaLabelConfig={ARIA_LABELS}
        className="bg-bg"
      >
        <Background variant={BackgroundVariant.Dots} gap={18} size={1} className="opacity-30" />
        <Controls showInteractive={false} position="bottom-right" />
        {showMiniMap && (
          <MiniMap
            position="top-right"
            pannable
            zoomable
            className="!bg-bg-elevated"
            maskColor="color-mix(in oklab, var(--bg) 70%, transparent)"
            nodeColor={(node) => {
              const status = summaries.get(node.id)?.status
              if (node.type === "container") return "transparent"
              return status && status !== "idle" ? STATUS_THEME[status].color : "var(--border)"
            }}
            nodeStrokeColor={(node) =>
              node.type === "container" ? "var(--fg-subtle)" : "transparent"
            }
          />
        )}
        {!compact && (
          <Panel position="top-left" className="flex gap-1.5">
            <ToolbarButton
              label="Fit diagram to view"
              onClick={() => void fitView({ padding: 0.12, maxZoom: 1, duration: 300 })}
            >
              <RotateCcw className="h-3.5 w-3.5" />
              Fit
            </ToolbarButton>
            {exportName && (
              <ExportMenu
                model={model}
                layout={layout}
                view={view}
                iterationSelection={iterationSelection}
                fileName={exportName}
                title={exportTitle ?? exportName}
              />
            )}
            {live && (
              <ToolbarButton
                label={follow ? "Stop following the running state" : "Follow the running state"}
                pressed={follow}
                onClick={() => setFollow((f) => !f)}
              >
                <LocateFixed className="h-3.5 w-3.5" />
                {follow ? "Following" : "Follow"}
              </ToolbarButton>
            )}
          </Panel>
        )}
        {!compact && hasTrace && (
          <Panel
            position="bottom-left"
            className="rounded-lg border border-border bg-bg-elevated/90 px-2.5 py-1.5 backdrop-blur-sm"
          >
            <ul
              className="flex flex-wrap gap-x-3 gap-y-1 text-2xs text-fg-muted"
              aria-label="Status key"
            >
              {LEGEND_STATUSES.map((status) => (
                <li key={status} className="flex items-center gap-1.5">
                  <span
                    className="h-2.5 w-2.5 rounded-sm border-2"
                    style={{ borderColor: STATUS_THEME[status].color }}
                  />
                  {STATUS_THEME[status].label}
                </li>
              ))}
            </ul>
          </Panel>
        )}
      </ReactFlow>
    </div>
  )
}

function ToolbarButton({
  label,
  pressed,
  onClick,
  children,
}: {
  label: string
  pressed?: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <Tooltip content={label}>
      <button
        type="button"
        aria-label={label}
        aria-pressed={pressed}
        onClick={onClick}
        className={cn(
          "flex h-7 items-center gap-1.5 rounded-md border px-2 text-2xs font-medium shadow-xs backdrop-blur-sm transition-colors",
          pressed
            ? "border-accent/40 bg-accent-muted text-accent"
            : "border-border bg-bg-elevated/90 text-fg-muted hover:bg-bg-muted hover:text-fg",
        )}
      >
        {children}
      </button>
    </Tooltip>
  )
}

/** The Parallel/Map state that owns a fork or join pseudo-node. */
function containerOf(model: AslModel, pseudo: string): string {
  const scopeId = pseudo.replace(/^__(fork|join)__:/, "")
  return model.scopes.get(scopeId)?.container ?? pseudo
}
