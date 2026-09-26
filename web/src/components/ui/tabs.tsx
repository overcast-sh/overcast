import { createContext, useContext, useId, type ReactNode } from "react"
import { cn } from "@/lib/utils"
import { handleRovingKeyDown } from "@/lib/roving-focus"

// ─── Context ──────────────────────────────────────────────────────────────

interface TabsContext {
  selectedKey: string
  onSelectionChange: (key: string) => void
  /** Per-instance id namespace, so two tab sets on one page cannot collide. */
  tabId: (key: string) => string
  panelId: (key: string) => string
}

const TabsCtx = createContext<TabsContext | null>(null)

function useTabsContext() {
  const ctx = useContext(TabsCtx)
  if (!ctx) throw new Error("Tab components must be used within <Tabs>")
  return ctx
}

// ─── Tabs (root) ──────────────────────────────────────────────────────────

interface TabsProps {
  selectedKey: string
  onSelectionChange: (key: string) => void
  children: ReactNode
  className?: string
}

export function Tabs({ selectedKey, onSelectionChange, children, className }: TabsProps) {
  // A tab and its panel have to point at each other — `aria-controls` on the tab,
  // `aria-labelledby` on the panel — or the pair is two unrelated things that happen to
  // sit next to each other: nothing tells a screen reader which panel a tab opened, and
  // the "move to the controlled panel" command has nowhere to go. The ids are namespaced
  // per instance because several pages render more than one tab set.
  const scope = useId()
  const tabId = (key: string) => `${scope}-tab-${key}`
  const panelId = (key: string) => `${scope}-panel-${key}`
  return (
    <TabsCtx.Provider value={{ selectedKey, onSelectionChange, tabId, panelId }}>
      <div className={className}>{children}</div>
    </TabsCtx.Provider>
  )
}

// ─── TabList ──────────────────────────────────────────────────────────────

interface TabListProps {
  children: ReactNode
  className?: string
  /**
   * Accessible name for the tablist. Optional, because a page with one set of
   * tabs reads fine without it — but required in practice wherever several
   * tablists share tab labels, since "PowerShell" on its own says nothing
   * about which group it switches.
   */
  "aria-label"?: string
}

export function TabList({ children, className, "aria-label": ariaLabel }: TabListProps) {
  return (
    <div
      role="tablist"
      aria-label={ariaLabel}
      className={cn("flex gap-6 border-b border-border", className)}
      // Only the selected tab is in the tab order (see `Tab`), so the arrow keys reach the rest.
      onKeyDown={(event) => handleRovingKeyDown(event, '[role="tab"]')}
    >
      {children}
    </div>
  )
}

// ─── Tab ──────────────────────────────────────────────────────────────────

// 3b/4b: mono 12, the selected tab bold and accent, over a 2px accent underline.
const tabBase = cn(
  "inline-flex items-center border-b-2 px-1 pb-2 font-mono text-xs transition-colors",
  "cursor-pointer disabled:cursor-not-allowed disabled:opacity-50",
)
const activeCls = "border-accent font-bold text-accent"
const inactiveCls = "border-transparent text-fg-muted not-disabled:hover:text-fg"

interface TabProps {
  id: string
  children: ReactNode
  isDisabled?: boolean
  className?: string
}

export function Tab({ id, children, isDisabled, className }: TabProps) {
  const { selectedKey, onSelectionChange, tabId, panelId } = useTabsContext()
  const isSelected = selectedKey === id
  return (
    <button
      id={tabId(id)}
      role="tab"
      aria-selected={isSelected}
      // Only the selected panel is mounted, so an unselected tab has nothing to point
      // at — claiming otherwise would be a dangling reference.
      aria-controls={isSelected ? panelId(id) : undefined}
      aria-disabled={isDisabled || undefined}
      tabIndex={isSelected ? 0 : -1}
      disabled={isDisabled}
      className={cn(tabBase, isSelected ? activeCls : inactiveCls, className)}
      onClick={() => onSelectionChange(id)}
    >
      {children}
    </button>
  )
}

// ─── TabPanel ─────────────────────────────────────────────────────────────

interface TabPanelProps {
  id: string
  children: ReactNode
  className?: string
}

export function TabPanel({ id, children, className }: TabPanelProps) {
  const { selectedKey, tabId, panelId } = useTabsContext()
  if (selectedKey !== id) return null
  return (
    // `tabIndex={0}`: the panel is named by its tab and is the next stop after it, so it
    // takes focus itself. Without it, a panel whose content has no focusable element is
    // skipped entirely and its tab appears to lead nowhere.
    <div
      id={panelId(id)}
      role="tabpanel"
      aria-labelledby={tabId(id)}
      tabIndex={0}
      className={cn("focus-visible:outline-2", className)}
    >
      {children}
    </div>
  )
}
