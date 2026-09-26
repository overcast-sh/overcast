import { useState, type KeyboardEvent } from "react"
import { Plus, X } from "lucide-react"
import { cn } from "@/lib/utils"
import type { QueryTab } from "../../query-tabs"

/** The DOM id of a query tab's button, which focus returns to after a rename. */
function tabButtonId(id: string): string {
  return `athena-query-tab-${id}`
}

/**
 * The query tabs: select one, double-click (or F2) to rename it, close it
 * (its ✕, or Delete), or open another. Arrow keys, Home and End move between
 * them. The close buttons stay out of the tab order: the tab list is one tab
 * stop, as a tab list should be.
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

  const focusTab = (id: string) => {
    // After the render that brings the button back (a rename) or selects it.
    requestAnimationFrame(() => document.getElementById(tabButtonId(id))?.focus())
  }
  const onTabKey = (event: KeyboardEvent, from: string) => {
    if (event.key === "F2") setRenaming(from)
    if (event.key === "Delete") onClose(from)
    const index = tabs.findIndex((t) => t.id === from)
    const target = {
      ArrowRight: tabs[(index + 1) % tabs.length],
      ArrowLeft: tabs[(index - 1 + tabs.length) % tabs.length],
      Home: tabs[0],
      End: tabs.at(-1),
    }[event.key]
    if (!target) return
    event.preventDefault()
    onSelect(target.id)
    focusTab(target.id)
  }
  const endRename = (id: string, title?: string) => {
    if (title !== undefined) onRename(id, title)
    setRenaming(null)
    focusTab(id)
  }

  return (
    <div className="flex min-w-0 flex-1 items-end gap-1">
      <div role="tablist" aria-label="Query tabs" className="flex min-w-0 gap-1 overflow-x-auto">
        {tabs.map((tab) => {
          const active = tab.id === activeId
          return (
            <div
              key={tab.id}
              role="presentation"
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
                  onBlur={(e) => endRename(tab.id, e.target.value.trim() || tab.title)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") e.currentTarget.blur()
                    if (e.key === "Escape") endRename(tab.id)
                  }}
                />
              ) : (
                <button
                  id={tabButtonId(tab.id)}
                  type="button"
                  role="tab"
                  aria-selected={active}
                  aria-keyshortcuts="F2 Delete"
                  tabIndex={active ? 0 : -1}
                  title="Double-click or F2 to rename, Delete to close"
                  onClick={() => onSelect(tab.id)}
                  onDoubleClick={() => setRenaming(tab.id)}
                  onKeyDown={(e) => onTabKey(e, tab.id)}
                  className={cn("max-w-48 truncate font-mono text-xs", active && "font-bold")}
                >
                  {tab.title}
                </button>
              )}
              <button
                type="button"
                tabIndex={-1}
                aria-label={`Close ${tab.title}`}
                onClick={() => onClose(tab.id)}
                className={cn(
                  "rounded-sm p-0.5 text-fg-subtle hover:bg-bg-muted hover:text-fg",
                  !active && "opacity-0 group-hover:opacity-100",
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
