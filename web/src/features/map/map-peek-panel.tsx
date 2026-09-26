/**
 * MapPeekPanel — the slide-in panel a map node opens to show more of a
 * resource without leaving the graph: a log stream, a table's first rows, a
 * table's latest commit. The panel owns the frame (title, actions, close,
 * dismissal); what it shows is its children.
 */

import type { ReactNode } from "react"
import * as Dialog from "@radix-ui/react-dialog"
import { X } from "lucide-react"
import { cn } from "@/lib/utils"

export interface MapPeekPanelProps {
  open: boolean
  onClose: () => void
  /** The resource, e.g. a function or table name. Also the dialog's accessible name. */
  title: string
  /** Under the title, in monospace: a stream name, a location. */
  subtitle?: string
  /** Links beside the close button, such as *Open in Logs*. */
  actions?: ReactNode
  /**
   * Keep the panel open when the click outside it lands on another peek
   * trigger (`[data-peek-trigger]`), whose own click then retargets the panel.
   * For a panel the whole map shares; a node's own panel just closes.
   */
  retargetable?: boolean
  children?: ReactNode
}

/** The shared style of a link in the panel's header. */
export const peekActionClass =
  "flex items-center gap-1 rounded px-2 py-1 font-mono text-2xs text-fg-muted uppercase hover:bg-fg-muted/15 hover:text-fg"

export function MapPeekPanel({
  open,
  onClose,
  title,
  subtitle,
  actions,
  retargetable = false,
  children,
}: MapPeekPanelProps) {
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
    >
      <Dialog.Portal>
        {/* Backdrop — pointer-events-none so a click on the canvas reaches the
            node under it; outside-click-to-close is handled below. */}
        <Dialog.Overlay className="pointer-events-none fixed inset-0 z-60" />

        {/* Wide enough for a pretty-printed document beside its timestamp;
            capped at the viewport so it never overflows a small window. */}
        <Dialog.Content
          aria-describedby={undefined}
          onEscapeKeyDown={onClose}
          onInteractOutside={(e) => {
            const target = e.detail.originalEvent.target as HTMLElement | null
            if (retargetable && target?.closest("[data-peek-trigger]")) {
              e.preventDefault()
              return
            }
            onClose()
          }}
          className={cn(
            "fixed inset-y-0 right-0 z-70 flex w-[min(44rem,100vw)] flex-col border-l border-border bg-bg-elevated shadow-2xl",
            "transition-transform duration-300",
            "data-[state=closed]:translate-x-full data-[state=open]:translate-x-0",
          )}
        >
          <Dialog.Title className="sr-only">{title}</Dialog.Title>
          {open && (
            <>
              <div className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-4 py-3">
                <div className="min-w-0">
                  <p className="truncate text-sm font-semibold">{title}</p>
                  {subtitle && (
                    <p className="truncate font-mono text-xs text-fg-muted" title={subtitle}>
                      {subtitle}
                    </p>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  {actions}
                  <Dialog.Close asChild>
                    <button
                      type="button"
                      className="shrink-0 rounded p-1 text-fg-muted hover:bg-fg-muted/15 hover:text-fg"
                      aria-label="Close"
                    >
                      <X className="h-4 w-4" />
                    </button>
                  </Dialog.Close>
                </div>
              </div>
              {children}
            </>
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
