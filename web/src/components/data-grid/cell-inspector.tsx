import { useRef } from "react"
import * as PopoverPrimitive from "@radix-ui/react-popover"
import { X } from "lucide-react"
import { CopyButton } from "@/components/ui/copy-button"
import { HighlightedCode } from "@/components/ui/highlighted-code"
import type { DataColumn } from "@/lib/data-sources/row-source"
import { formatCount } from "@/lib/format"
import { cellText, isStructured, prettyJson } from "./cell-format"

/**
 * The whole of one value, for a cell too narrow or too nested to show it:
 * lists and structs as indented, highlighted JSON, anything else as its full
 * text (up to `INSPECT_CHARS`). Opens by the cell (Enter, a double-click or
 * the cell's inspect button), with the value's own copy button.
 */
export function CellInspector({
  anchor,
  row,
  column,
  loaded,
  value,
  onClose,
}: {
  /** The cell it opens by. */
  anchor: HTMLElement | null
  row: number
  column: DataColumn
  loaded: boolean
  value: unknown
  onClose: () => void
}) {
  const structured = loaded && isStructured(value)
  const text = !loaded
    ? "Not loaded yet."
    : structured
      ? prettyJson(value)
      : cellText(value, column).text
  // Read at open: the cell stays put while the popover is open, because the
  // grid does not scroll under a modal popover.
  const virtualRef = useRef({
    getBoundingClientRect: () => anchor?.getBoundingClientRect() ?? new DOMRect(),
  })
  return (
    <PopoverPrimitive.Root open onOpenChange={(open) => !open && onClose()}>
      <PopoverPrimitive.Anchor virtualRef={virtualRef} />
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          side="bottom"
          align="start"
          sideOffset={4}
          collisionPadding={12}
          sticky="always"
          aria-label={`Row ${formatCount(row + 1)}, ${column.name}`}
          className="z-50 flex max-h-80 w-[min(32rem,90vw)] flex-col overflow-hidden rounded-card border border-border bg-bg-elevated shadow-xl"
        >
          <div className="flex items-center gap-2 border-b border-border bg-bg-muted px-3 py-2">
            <span className="min-w-0 truncate font-mono text-xs text-fg">
              {column.name}
              <span className="text-fg-subtle"> · row {formatCount(row + 1)}</span>
              {column.type && <span className="text-fg-subtle"> · {column.type}</span>}
            </span>
            <span className="ml-auto flex items-center gap-1">
              {loaded && <CopyButton value={text} noun="value" tone="inline" />}
              <PopoverPrimitive.Close
                aria-label="Close"
                className="flex size-6 cursor-pointer items-center justify-center rounded-control text-fg-subtle hover:bg-accent-muted hover:text-accent"
              >
                <X aria-hidden className="size-3.5" />
              </PopoverPrimitive.Close>
            </span>
          </div>
          <HighlightedCode
            text={text}
            language={structured ? "json" : null}
            className="min-h-0 overflow-auto p-3 font-mono text-xs leading-relaxed wrap-break-word whitespace-pre-wrap text-fg"
          />
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}
