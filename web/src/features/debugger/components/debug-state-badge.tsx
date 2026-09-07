import { Bug } from "lucide-react"
import { Badge, type BadgeProps } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

/**
 * The state vocabulary is the server's (`internal/debugger/target.go`):
 * inert | unbound | listening | attached | paused | error. Only the colour and
 * the one word shown for `inert` are decided here — a tagged resource whose
 * server flag is off reads better as "off" in a list than as "inert".
 */
const STATES: Partial<Record<string, { variant: BadgeProps["variant"]; label: string }>> = {
  inert: { variant: "default", label: "off" },
  unbound: { variant: "warning", label: "unbound" },
  listening: { variant: "accent", label: "listening" },
  attached: { variant: "success", label: "attached" },
  paused: { variant: "warning", label: "paused" },
  error: { variant: "danger", label: "error" },
}

/**
 * State-coloured pill for a debug target, small enough for a list row and the
 * same one the Debug tab opens with. The glyph says "debugger" where the row
 * has no room for the word; the visually hidden prefix says it to a screen
 * reader.
 */
export function DebugStateBadge({ state, className }: { state: string; className?: string }) {
  const { variant, label } = STATES[state] ?? { variant: "default", label: state }
  return (
    <Badge
      variant={variant}
      title={`Debugger: ${label}`}
      className={cn("gap-1", className)}
      data-state={state}
    >
      <Bug aria-hidden className="h-3 w-3" />
      <span className="sr-only">Debugger </span>
      {label}
    </Badge>
  )
}
