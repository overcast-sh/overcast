import { cn } from "@/lib/utils"
import { historyEventVariant, humanizeEventType } from "../format"

const DOT: Record<ReturnType<typeof historyEventVariant>, string> = {
  default: "bg-fg-subtle",
  info: "bg-accent",
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-danger",
}

/**
 * A history event type as words with a status dot — "Task state entered" —
 * rather than an upper-cased badge, which runs a camel-cased name into one
 * unreadable word. The raw type stays in the tooltip for searching the docs.
 */
export function EventType({ type, className }: { type: string | undefined; className?: string }) {
  const variant = historyEventVariant(type)
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 text-xs whitespace-nowrap",
        variant === "danger" ? "text-danger" : "text-fg",
        className,
      )}
      title={type}
    >
      <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", DOT[variant])} />
      {humanizeEventType(type)}
    </span>
  )
}
