import { useState } from "react"
import { Plus, X } from "lucide-react"
import { cn } from "@/lib/utils"
import type { QueryTab } from "../../query-tabs"

/**
 * The query tabs: select one, double-click to rename it, close it, or open
 * another. Arrow keys move between them, as in any tab list.
 */
export function QueryTabStrip({
  tabs,
  activeId,
  onSelect,
  onRename,
  onClose,
  onNew,
}: {
  tabs: readonly QueryTab[]
  activeId: string
  onSelect: (id: string) => void
  onRename: (id: string, title: string) => void
  onClose: (id: string) => void
  onNew: () => void
}) {
  const [renaming, setRenaming] = useState<string | null>(null)
  return (
    <div className="flex min-w-0 items-end gap-1 border-b border-border">
      <div role="tablist" aria-label="Query tabs" className="flex min-w-0 gap-1 overflow-x-auto">
        {tabs.map((tab) => {
          const active = tab.id === activeId
          return (
            <div
              key={tab.id}
              className={cn(
                "group flex shrink-0 items-center gap-1 rounded-t-md border border-b-0 px-2 py-1",
                active
                  ? "border-border bg-bg-elevated text-fg"
                  : "border-transparent text-fg-muted hover:text-fg",
              )}
            >
              {renaming === tab.id ? (
                <input
                  autoFocus
                  aria-label="Tab name"
                  defaultValue={tab.title}
                  className="w-32 bg-transparent font-mono text-xs text-fg outline-none"
                  onBlur={(e) => {
                    onRename(tab.id, e.target.value.trim() || tab.title)
                    setRenaming(null)
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") e.currentTarget.blur()
                    if (e.key === "Escape") setRenaming(null)
                  }}
                />
              ) : (
                <button
                  type="button"
                  role="tab"
                  aria-selected={active}
                  tabIndex={active ? 0 : -1}
                  title="Double-click to rename"
                  onClick={() => onSelect(tab.id)}
                  onDoubleClick={() => setRenaming(tab.id)}
                  onKeyDown={(e) => {
                    if (e.key === "F2") setRenaming(tab.id)
                    const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0
                    if (!step) return
                    const index = tabs.findIndex((t) => t.id === tab.id)
                    const next = (index + step + tabs.length) % tabs.length
                    onSelect(tabs[next].id)
                    const list = e.currentTarget.closest('[role="tablist"]')
                    list?.querySelectorAll<HTMLElement>('[role="tab"]').item(next).focus()
                  }}
                  className={cn("max-w-48 truncate font-mono text-xs", active && "font-bold")}
                >
                  {tab.title}
                </button>
              )}
              <button
                type="button"
                aria-label={`Close ${tab.title}`}
                onClick={() => onClose(tab.id)}
                className={cn(
                  "rounded-sm p-0.5 text-fg-subtle hover:bg-bg-muted hover:text-fg",
                  !active && "opacity-0 group-hover:opacity-100 focus-visible:opacity-100",
                )}
              >
                <X aria-hidden className="size-3" />
              </button>
            </div>
          )
        })}
      </div>
      <button
        type="button"
        aria-label="New query tab"
        onClick={onNew}
        className="mb-1 rounded-sm p-1 text-fg-subtle hover:bg-bg-muted hover:text-fg"
      >
        <Plus aria-hidden className="size-3.5" />
      </button>
    </div>
  )
}
