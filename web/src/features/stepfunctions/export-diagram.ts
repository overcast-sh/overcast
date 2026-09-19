/**
 * Renders the flow diagram as a standalone SVG document — the same layout and
 * the same statuses as the canvas, but self-contained: every colour resolved
 * to a literal, every icon inlined, no CSS variables and no React Flow, so the
 * file opens the same in a browser, a design tool or a pull request.
 */
import type { AslModel } from "./asl"
import type { DiagramState, EdgeState } from "./diagram-state"
import { laneLabel, pillStatus } from "./diagram-labels"
import type { IterationSelection, NodeStatus } from "./execution-trace"
import { formatDuration, runDuration } from "./execution-trace"
import {
  arrowHead,
  roundedPath,
  shortLabel,
  type FlowLayout,
  type LayoutEdgeKind,
} from "./graph-layout"

/** Literal colours for everything the diagram paints. */
export interface ExportPalette {
  bg: string
  surface: string
  border: string
  fg: string
  fgMuted: string
  fgSubtle: string
  accent: string
  success: string
  danger: string
  warning: string
  /** Icon tile colour per state type. */
  types: Record<string, string>
}

/** Icon markup (a complete `<svg>`), keyed by state type and by run status. */
export interface ExportIcons {
  types: Record<string, string>
  statuses: Partial<Record<NodeStatus, string>>
}

export interface ExportOptions {
  title: string
  subtitle?: string
  palette: ExportPalette
  icons?: ExportIcons
  /** Timestamp used for running durations; the export is a snapshot. */
  now?: number
}

const PAD = 32
const HEADER = 64
const FONT = `ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`
const MONO = `ui-monospace, SFMono-Regular, Menlo, Consolas, monospace`

export function escapeXml(text: string): string {
  return text.replace(
    /[<>&"']/g,
    (c) => ({ "<": "&lt;", ">": "&gt;", "&": "&amp;", '"': "&quot;", "'": "&apos;" })[c] ?? c,
  )
}

/** Truncates text to roughly fit `width` pixels at the given average glyph width. */
function fit(text: string, width: number, glyph: number): string {
  const max = Math.max(1, Math.floor(width / glyph))
  return text.length > max ? `${text.slice(0, max - 1)}…` : text
}

function statusColor(status: NodeStatus, p: ExportPalette): string {
  switch (status) {
    case "running":
      return p.accent
    case "succeeded":
      return p.success
    case "failed":
      return p.danger
    case "caught":
    case "aborted":
      return p.warning
    default:
      return p.border
  }
}

function edgeColor(kind: LayoutEdgeKind, state: EdgeState, p: ExportPalette): string {
  switch (state) {
    case "active":
      return p.accent
    case "taken":
      return p.success
    case "catch":
      return p.warning
    case "idle":
      return p.border
    default:
      return kind === "catch" ? p.danger : p.fgSubtle
  }
}

function icon(markup: string | undefined, x: number, y: number, color: string): string {
  if (!markup) return ""
  return `<g transform="translate(${x},${y})" color="${color}" style="color:${color}">${markup}</g>`
}

export function diagramToSvg(
  model: AslModel,
  layout: FlowLayout,
  view: DiagramState,
  selection: IterationSelection,
  options: ExportOptions,
): string {
  const { palette: p, icons } = options
  const now = options.now ?? Date.now()
  const width = Math.ceil(layout.width + PAD * 2)
  const height = Math.ceil(layout.height + PAD * 2 + HEADER)
  const ox = PAD
  const oy = PAD + HEADER
  const out: string[] = []

  out.push(
    `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" font-family='${FONT}'>`,
    `<title>${escapeXml(options.title)}</title>`,
    `<rect width="100%" height="100%" fill="${p.bg}"/>`,
    `<text x="${PAD}" y="${PAD + 18}" font-size="18" font-weight="700" fill="${p.fg}">${escapeXml(options.title)}</text>`,
  )
  if (options.subtitle) {
    out.push(
      `<text x="${PAD}" y="${PAD + 40}" font-size="12" fill="${p.fgMuted}" font-family='${MONO}'>${escapeXml(options.subtitle)}</text>`,
    )
  }

  // Containers first, outermost first, so nested ones and everything else paint over them.
  const containers = layout.nodes
    .filter((n) => n.kind === "container")
    .sort((a, b) => a.depth - b.depth)
  for (const n of containers) {
    const state = model.states.get(n.id)
    if (!state) continue
    const summary = view.summaries.get(n.id)
    const status = summary?.status ?? "idle"
    const typeColor = p.types[state.type] ?? p.fgSubtle
    const stroke = status === "idle" ? typeColor : statusColor(status, p)
    out.push(
      `<g>`,
      `<rect x="${n.x + ox}" y="${n.y + oy}" width="${n.width}" height="${n.height}" rx="12" fill="${typeColor}" fill-opacity="0.05" stroke="${stroke}" stroke-opacity="${status === "idle" ? 0.6 : 1}" stroke-width="${status === "idle" ? 1.5 : 2}" stroke-dasharray="6 4"/>`,
      `<rect x="${n.x + ox + 12}" y="${n.y + oy + 14}" width="24" height="24" rx="6" fill="${typeColor}" fill-opacity="0.16"/>`,
      icon(icons?.types[state.type], n.x + ox + 16, n.y + oy + 18, typeColor),
      `<text x="${n.x + ox + 44}" y="${n.y + oy + 30}" font-size="12" font-weight="600" fill="${p.fg}">${escapeXml(fit(state.name, n.width * 0.45, 7))}<tspan dx="8" font-size="11" font-weight="400" fill="${p.fgMuted}">${escapeXml(fit(state.summary, n.width * 0.35, 6))}</tspan></text>`,
    )
    if (summary?.focusRun) {
      const items = summary.focusRun.itemCount
      const done = [...(summary.focusRun.iterations?.values() ?? [])].filter(
        (i) => i.status === "succeeded",
      ).length
      const detail = [
        formatDuration(runDuration(summary.focusRun, now)),
        items !== undefined ? `${done}/${items} items` : "",
        selection[n.id] !== undefined ? `showing #${selection[n.id]}` : "",
      ]
        .filter(Boolean)
        .join(" · ")
      out.push(
        `<text x="${n.x + ox + n.width - 14}" y="${n.y + oy + 30}" font-size="11" text-anchor="end" fill="${statusColor(status, p)}" font-family='${MONO}'>${escapeXml(detail)}</text>`,
      )
    }
    ;(n.lanes ?? []).forEach((lane, i) => {
      const laneStatus = view.laneStatus(lane.scopeId)
      out.push(
        `<rect x="${lane.x + ox}" y="${lane.y + oy}" width="${lane.width}" height="${lane.height}" rx="8" fill="${p.bg}" fill-opacity="0.4" stroke="${laneStatus === "idle" ? p.border : statusColor(laneStatus, p)}" stroke-opacity="0.6"/>`,
        `<text x="${lane.x + ox + 10}" y="${lane.y + oy + 4}" font-size="9" letter-spacing="1" fill="${p.fgSubtle}" font-family='${MONO}' paint-order="stroke" stroke="${p.surface}" stroke-width="4">${escapeXml(laneLabel(state, i).toUpperCase())}</text>`,
      )
    })
    out.push(`</g>`)
  }

  // Edges, then their labels over them.
  const labels: string[] = []
  for (const e of layout.edges) {
    const ev = view.edges.get(e.id)
    const state = ev?.state ?? "plain"
    const color = edgeColor(e.kind, state, p)
    const emphasised = state === "taken" || state === "catch" || state === "active"
    const dashed = (e.kind === "catch" || e.kind === "default") && state !== "active"
    const points = e.points.map((pt) => ({ x: pt.x + ox, y: pt.y + oy }))
    out.push(
      `<path d="${roundedPath(points)}" fill="none" stroke="${color}" stroke-width="${emphasised ? 2 : 1.5}" stroke-linejoin="round"${dashed ? ' stroke-dasharray="5 4"' : ""}/>`,
    )
    if (e.kind !== "fork")
      out.push(
        `<path d="${arrowHead(points)}" fill="${color}" stroke="${color}" stroke-linejoin="round"/>`,
      )
    if (e.label && e.labelPosition) {
      const text = shortLabel(e.label) + ((ev?.count ?? 0) > 1 ? ` ×${ev?.count}` : "")
      const w = text.length * 6.2 + 12
      const { x, y } = e.labelPosition
      const tone = e.kind === "catch" ? p.danger : p.fgMuted
      labels.push(
        `<g opacity="${state === "idle" ? 0.6 : 1}"><rect x="${x + ox - w / 2}" y="${y + oy - 9}" width="${w}" height="18" rx="5" fill="${p.surface}" stroke="${e.kind === "catch" ? p.danger : p.border}" stroke-opacity="0.5"/>`,
        `<text x="${x + ox}" y="${y + oy + 4}" font-size="10" text-anchor="middle" fill="${tone}" font-family='${MONO}'>${escapeXml(text)}</text></g>`,
      )
    }
  }
  out.push(...labels)

  // States and the Start/End markers on top.
  for (const n of layout.nodes) {
    if (n.kind === "container") continue
    if (n.kind === "start" || n.kind === "end") {
      const status = pillStatus(n.kind, view)
      const color = status === "idle" ? p.fgSubtle : statusColor(status, p)
      out.push(
        `<rect x="${n.x + ox}" y="${n.y + oy}" width="${n.width}" height="${n.height}" rx="${n.height / 2}" fill="${p.surface}" stroke="${color}" stroke-width="${status === "idle" ? 1.5 : 2}"/>`,
        `<text x="${n.x + ox + n.width / 2}" y="${n.y + oy + n.height / 2 + 4}" font-size="10" font-weight="700" letter-spacing="2" text-anchor="middle" fill="${color}" font-family='${MONO}'>${n.kind === "start" ? "START" : "END"}</text>`,
      )
      continue
    }
    const state = model.states.get(n.id)
    if (!state) continue
    const summary = view.summaries.get(n.id)
    const status = summary?.status ?? "idle"
    const typeColor = p.types[state.type] ?? p.fgSubtle
    const dim = view.hasTrace && status === "idle"
    const focus = summary?.focusRun
    const runs = summary?.runs.length ?? 0
    const x = n.x + ox
    const y = n.y + oy
    out.push(
      `<g opacity="${dim ? 0.5 : 1}">`,
      `<rect x="${x}" y="${y}" width="${n.width}" height="${n.height}" rx="8" fill="${p.surface}" stroke="${statusColor(status, p)}" stroke-width="${status === "idle" ? 1 : 2}"/>`,
      `<rect x="${x + 10}" y="${y + n.height / 2 - 16}" width="32" height="32" rx="6" fill="${typeColor}" fill-opacity="0.15"/>`,
      icon(icons?.types[state.type], x + 18, y + n.height / 2 - 8, typeColor),
      `<text x="${x + 52}" y="${y + n.height / 2 - 3}" font-size="12" font-weight="600" fill="${p.fg}">${escapeXml(fit(state.name, n.width - (status === "idle" ? 64 : 110), 7))}</text>`,
      `<text x="${x + 52}" y="${y + n.height / 2 + 13}" font-size="10.5" fill="${p.fgMuted}">${escapeXml(fit(state.summary, n.width - (status === "idle" ? 64 : 110), 6))}</text>`,
    )
    if (status !== "idle") {
      const color = statusColor(status, p)
      out.push(icon(icons?.statuses[status], x + n.width - 26, y + 10, color))
      if (focus) {
        out.push(
          `<text x="${x + n.width - 10}" y="${y + n.height - 12}" font-size="10" text-anchor="end" fill="${p.fgSubtle}" font-family='${MONO}'>${escapeXml(formatDuration(runDuration(focus, now)))}</text>`,
        )
      }
    }
    if (runs > 1) {
      const badge = `×${runs}`
      const w = badge.length * 6.5 + 10
      out.push(
        `<rect x="${x + n.width - w - 8}" y="${y - 9}" width="${w}" height="16" rx="8" fill="${p.surface}" stroke="${p.border}"/>`,
        `<text x="${x + n.width - 8 - w / 2}" y="${y + 3}" font-size="10" text-anchor="middle" fill="${p.fgMuted}" font-family='${MONO}'>${badge}</text>`,
      )
    }
    out.push(`</g>`)
  }

  out.push(`</svg>`)
  return out.filter(Boolean).join("\n")
}
