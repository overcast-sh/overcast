import * as React from "react"
import { cn } from "@/lib/utils"

interface SwitchProps {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  disabled?: boolean
  className?: string
  id?: string
  /** `sm` fits a dense strip such as an explorer header; the default suits a form row. */
  size?: "sm" | "md"
}

const Switch = React.forwardRef<HTMLButtonElement, SwitchProps>(
  ({ checked, onCheckedChange, disabled, className, id, size = "md" }, ref) => {
    const small = size === "sm"
    return (
      <button
        ref={ref}
        id={id}
        role="switch"
        type="button"
        aria-checked={checked}
        disabled={disabled}
        onClick={() => onCheckedChange(!checked)}
        className={cn(
          "relative inline-flex shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-bg focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50",
          small ? "h-4 w-7" : "h-5 w-9",
          checked ? "bg-accent" : "bg-bg-muted",
          className,
        )}
      >
        <span
          className={cn(
            "pointer-events-none block rounded-full bg-fg-on-accent shadow-sm transition-transform",
            small ? "h-3 w-3" : "h-4 w-4",
            checked ? (small ? "translate-x-3" : "translate-x-4") : "translate-x-0",
          )}
        />
      </button>
    )
  },
)
Switch.displayName = "Switch"

export { Switch }
