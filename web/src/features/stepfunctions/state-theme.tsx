/**
 * How each state type and each run status looks, in one place, so the diagram,
 * the timeline, the event list and the inspector all agree on it.
 */
import {
  ArrowDown,
  CircleCheck,
  CircleDashed,
  CircleX,
  GitFork,
  Hourglass,
  LoaderCircle,
  OctagonX,
  Repeat,
  Split,
  TriangleAlert,
  Zap,
  type LucideIcon,
} from "lucide-react"
import type { NodeStatus } from "./execution-trace"

export interface StateTypeTheme {
  icon: LucideIcon
  /** A `var(--…)` colour for the icon tile. */
  color: string
  label: string
}

const TYPE_THEME: Record<string, StateTypeTheme> = {
  Task: { icon: Zap, color: "var(--cat-7)", label: "Task" },
  Pass: { icon: ArrowDown, color: "var(--fg-subtle)", label: "Pass" },
  Choice: { icon: Split, color: "var(--cat-3)", label: "Choice" },
  Wait: { icon: Hourglass, color: "var(--cat-6)", label: "Wait" },
  Succeed: { icon: CircleCheck, color: "var(--success)", label: "Succeed" },
  Fail: { icon: OctagonX, color: "var(--danger)", label: "Fail" },
  Parallel: { icon: GitFork, color: "var(--cat-8)", label: "Parallel" },
  Map: { icon: Repeat, color: "var(--cat-9)", label: "Map" },
}

const UNKNOWN_TYPE: StateTypeTheme = {
  icon: CircleDashed,
  color: "var(--fg-subtle)",
  label: "Unknown",
}

export function stateTypeTheme(type: string): StateTypeTheme {
  return TYPE_THEME[type] ?? { ...UNKNOWN_TYPE, label: type }
}

export interface StatusTheme {
  label: string
  /** Border/stroke colour. */
  color: string
  icon?: LucideIcon
  badge: "default" | "success" | "danger" | "warning" | "info"
}

export const STATUS_THEME: Record<NodeStatus, StatusTheme> = {
  idle: { label: "Not run", color: "var(--border)", badge: "default" },
  running: { label: "Running", color: "var(--accent)", icon: LoaderCircle, badge: "info" },
  succeeded: { label: "Succeeded", color: "var(--success)", icon: CircleCheck, badge: "success" },
  failed: { label: "Failed", color: "var(--danger)", icon: CircleX, badge: "danger" },
  caught: { label: "Caught", color: "var(--warning)", icon: TriangleAlert, badge: "warning" },
  aborted: { label: "Aborted", color: "var(--warning)", icon: CircleX, badge: "warning" },
}

/** Legend order for the diagram's status key. */
export const LEGEND_STATUSES: NodeStatus[] = ["running", "succeeded", "caught", "failed", "idle"]
