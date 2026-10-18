import type { ReactNode } from "react"
import { XCircle } from "lucide-react"
import { cn } from "@/lib/utils"

/**
 * Why something failed, where it cannot be missed: a revision that could not
 * be prepared is Failed for good, and the reason is the only thing that helps.
 */
export function FailureNotice({
  title,
  children,
  className,
}: {
  title: ReactNode
  children?: ReactNode
  className?: string
}) {
  return (
    <div
      role="alert"
      className={cn(
        "flex items-start gap-2.5 rounded-md border border-danger/40 bg-danger-muted px-3 py-2 text-xs text-fg-muted",
        className,
      )}
    >
      <XCircle aria-hidden className="mt-0.5 size-3.5 shrink-0 text-danger" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <p className="font-medium text-fg">{title}</p>
        {children && <div className="leading-relaxed break-words">{children}</div>}
      </div>
    </div>
  )
}
