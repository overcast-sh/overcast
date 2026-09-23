import { useMemo, useRef, useState } from "react"
import * as PopoverPrimitive from "@radix-ui/react-popover"
import { ChevronDown, Check, Search } from "lucide-react"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

export interface CheckboxFilterItem {
  id: string
  label: string
  count?: number
}

interface CheckboxFilterDropdownProps {
  items: CheckboxFilterItem[]
  /** "hide" = checked means visible (deny-list, e.g. events). "show" = checked means filtered in (include-list, e.g. traces). */
  model: "hide" | "show"
  /** IDs whose semantics depend on `model`: hide-model = currently hidden; show-model = currently selected. */
  selected: Set<string>
  onToggle: (id: string) => void
  /** Make every item visible (hide-model: clear the hidden set; show-model: select all). */
  onShowAll: () => void
  /** Make every item hidden (hide-model: hide all; show-model: deselect all). */
  onHideAll: () => void
  triggerLabel: string
  /** Which edge the menu is anchored to. `"end"` for a trigger sitting at the right of its row. */
  align?: "start" | "end"
  /** Extra classes for the trigger — a denser toolbar sets its height. */
  triggerClassName?: string
}

/**
 * A filter menu on Radix Popover rather than a hand-rolled one.
 *
 * It used to be `useState` plus a bare `document.addEventListener("mousedown")`: no
 * Escape, no focus trap, no focus return to the trigger, no `aria-expanded` or
 * `aria-haspopup` on the trigger, and no portal — so the menu could be clipped by any
 * ancestor with `overflow: hidden`, and a keyboard user who opened it had no way back
 * out except Tab-ing through the whole page. Popover (already a dependency, already
 * used by `combobox.tsx`) brings all of that with it.
 *
 * Popover and not DropdownMenu because the menu holds a search field: a menu's
 * typeahead would eat every keystroke meant for the input.
 */
export function CheckboxFilterDropdown({
  items,
  model,
  selected,
  onToggle,
  onShowAll,
  onHideAll,
  triggerLabel,
  align = "start",
  triggerClassName,
}: CheckboxFilterDropdownProps) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState("")
  const searchRef = useRef<HTMLInputElement>(null)

  const filtered = useMemo(() => {
    if (!search.trim()) return items
    const lower = search.toLowerCase()
    return items.filter(
      (s) => s.id.toLowerCase().includes(lower) || s.label.toLowerCase().includes(lower),
    )
  }, [items, search])

  const visibleCount = model === "hide" ? items.length - selected.size : selected.size

  return (
    <PopoverPrimitive.Root
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (next) setSearch("")
      }}
    >
      <PopoverPrimitive.Trigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className={cn(
            "h-9 gap-1 border border-border text-xs",
            selected.size > 0 && model === "show" && "bg-fg/5 font-medium",
            triggerClassName,
          )}
        >
          {triggerLabel}
          <ChevronDown aria-hidden className="h-3 w-3" />
        </Button>
      </PopoverPrimitive.Trigger>

      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          align={align}
          sideOffset={4}
          // Focus the search field rather than the first checkbox: typing is what this
          // menu is for, and Tab reaches the list from there in one step.
          onOpenAutoFocus={(event) => {
            event.preventDefault()
            searchRef.current?.focus()
          }}
          // Radix gives the content `role="dialog"`, which has to be named.
          aria-label={triggerLabel}
          className="z-50 w-52 rounded-lg border border-border bg-bg-elevated shadow-lg"
        >
          <button
            type="button"
            className="flex w-full items-center gap-2 rounded-t-lg px-3 py-1.5 text-xs text-fg-muted hover:bg-fg/5"
            onClick={visibleCount > 0 ? onHideAll : onShowAll}
          >
            {visibleCount > 0
              ? model === "hide"
                ? "Hide all"
                : "Deselect all"
              : model === "hide"
                ? "Show all"
                : "Select all"}
          </button>
          <div aria-hidden className="mx-2 h-px bg-border" />
          <div className="px-2 py-1">
            <div className="relative">
              <Search
                aria-hidden
                className="absolute top-1/2 left-2 h-3 w-3 -translate-y-1/2 text-fg-muted"
              />
              <Input
                ref={searchRef}
                aria-label={`Search ${triggerLabel}`}
                placeholder="Search…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="h-7 pl-7 text-xs"
              />
            </div>
          </div>
          <div role="group" aria-label={triggerLabel} className="max-h-56 overflow-y-auto">
            {filtered.length === 0 ? (
              <div className="px-3 py-4 text-center text-xs text-fg-subtle">No matches</div>
            ) : (
              filtered.map((s) => {
                const checked = model === "hide" ? !selected.has(s.id) : selected.has(s.id)
                return (
                  <button
                    key={s.id}
                    type="button"
                    role="checkbox"
                    aria-checked={checked}
                    // Stated rather than taken from the contents: the events stream has
                    // at least one source with neither a label nor an id, which without
                    // this renders as a checkbox with no name at all. The count is
                    // deliberately not part of the name — it changes as events arrive.
                    aria-label={s.label || s.id || "Unnamed source"}
                    className="flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-fg/5"
                    onClick={() => onToggle(s.id)}
                  >
                    <span
                      aria-hidden
                      className={cn(
                        "flex h-4 w-4 shrink-0 items-center justify-center rounded border",
                        checked ? "border-accent bg-accent text-bg" : "border-border",
                      )}
                    >
                      {checked && <Check className="h-3 w-3" />}
                    </span>
                    <span className="flex-1 truncate text-left">{s.label}</span>
                    {s.count !== undefined && (
                      <span className="text-fg-muted tabular-nums">{s.count}</span>
                    )}
                  </button>
                )
              })
            )}
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}
