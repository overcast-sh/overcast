import type { LucideIcon } from "lucide-react"
import { Tooltip } from "@/components/ui/tooltip"
import { handleRovingKeyDown, RADIO_KEYS } from "@/lib/roving-focus"
import { cn } from "@/lib/utils"

export interface SegmentedOption<T extends string> {
  value: T
  /** The visible text, or the accessible name and tooltip when `iconOnly`. */
  label: string
  icon?: LucideIcon
  /** A longer explanation, shown as a tooltip. */
  hint?: string
}

// `sm` sits in a toolbar or a panel header; `md` stands beside the 32px inputs
// and buttons of a form or a dialog. Both are the segment height; the pill adds
// its 2px padding and 1px border on each side.
const SEGMENT_SIZES = {
  sm: { text: "h-6 px-2 text-2xs", icon: "h-6 w-7" },
  md: { text: "h-6.5 px-2.5 text-xs", icon: "h-6.5 w-8" },
} as const

/**
 * Two to four mutually exclusive views or modes of one thing, as a pill of
 * segments: Table/Raw, Grid/List, This folder/All nested.
 *
 * It is a radio group, so a screen reader hears the choice and which option is
 * on. Only the checked segment is in the tab order; the arrow keys move between
 * segments and select as they go, as they do for native radio buttons. Clicking
 * the segment that is already checked does nothing.
 *
 * For more options, or options that each need a sentence, reach for a select
 * or a set of option cards; for views with panels of their own, `Tabs`.
 */
export function SegmentedControl<T extends string>({
  label,
  value,
  options,
  onChange,
  iconOnly = false,
  size = "sm",
  className,
}: {
  /** The accessible name of the group, e.g. "Parquet view". */
  label: string
  value: T
  options: readonly SegmentedOption<T>[]
  onChange: (value: T) => void
  /** Show only each option's icon, with its label as the name and tooltip. */
  iconOnly?: boolean
  size?: keyof typeof SEGMENT_SIZES
  className?: string
}) {
  // A value outside the options must still leave one segment tabbable.
  const tabbable = options.some((o) => o.value === value) ? value : options[0]?.value
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn(
        "inline-flex items-center gap-0.5 rounded-control border border-border bg-bg-elevated p-0.5",
        className,
      )}
      onKeyDown={(event) => handleRovingKeyDown(event, '[role="radio"]', RADIO_KEYS)}
    >
      {options.map((option) => (
        <Segment
          key={option.value}
          option={option}
          checked={option.value === value}
          tabbable={option.value === tabbable}
          iconOnly={iconOnly}
          size={size}
          onSelect={() => onChange(option.value)}
        />
      ))}
    </div>
  )
}

function Segment<T extends string>({
  option,
  checked,
  tabbable,
  iconOnly,
  size,
  onSelect,
}: {
  option: SegmentedOption<T>
  checked: boolean
  tabbable: boolean
  iconOnly: boolean
  size: keyof typeof SEGMENT_SIZES
  onSelect: () => void
}) {
  const Icon = option.icon
  const segment = (
    <button
      type="button"
      role="radio"
      aria-checked={checked}
      aria-label={iconOnly ? option.label : undefined}
      tabIndex={tabbable ? 0 : -1}
      onClick={checked ? undefined : onSelect}
      className={cn(
        "inline-flex shrink-0 items-center justify-center gap-1.5 rounded-sm font-mono whitespace-nowrap transition-colors",
        "focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent",
        SEGMENT_SIZES[size][iconOnly ? "icon" : "text"],
        checked ? "bg-accent-muted text-accent" : "text-fg-muted hover:text-fg",
      )}
    >
      {Icon && <Icon aria-hidden className="size-3.5" strokeWidth={1.9} />}
      {!iconOnly && option.label}
    </button>
  )
  const tooltip = option.hint ?? (iconOnly ? option.label : undefined)
  return tooltip ? <Tooltip content={tooltip}>{segment}</Tooltip> : segment
}
