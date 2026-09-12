/**
 * The four panels beside the code pane while a session is open — Locals,
 * Watch, Call stack, Breakpoints (docs/plans/compute-debugger-console.md
 * § 3.5). Wide, they stack as collapsible sections the way VS Code's Run
 * view does, all open to begin with; narrow (`narrow`), they become a tab
 * strip showing one at a time, which is what fits above the drawer. Both
 * forms render the same four panel components; only the chrome differs.
 */
import { useState, type ReactNode } from "react"
import { ChevronRight } from "lucide-react"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import { sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { BreakpointsPanel } from "./breakpoints-panel"
import { CallStackPanel } from "./call-stack-panel"
import { LocalsPanel } from "./locals-panel"
import { WatchPanel } from "./watch-panel"

const PANELS: Array<{ id: string; label: string; render: () => ReactNode }> = [
  { id: "locals", label: "Locals", render: () => <LocalsPanel /> },
  { id: "watch", label: "Watch", render: () => <WatchPanel /> },
  { id: "call-stack", label: "Call stack", render: () => <CallStackPanel /> },
  { id: "breakpoints", label: "Breakpoints", render: () => <BreakpointsPanel /> },
]

export function DebugSidebar({ narrow, className }: { narrow: boolean; className?: string }) {
  return narrow ? <TabStrip className={className} /> : <Sections className={className} />
}

function Sections({ className }: { className?: string }) {
  const [closed, setClosed] = useState<ReadonlySet<string>>(() => new Set())
  const toggle = (id: string) =>
    setClosed((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  return (
    <div className={cn("flex flex-col divide-y divide-border", className)}>
      {PANELS.map((panel) => {
        const open = !closed.has(panel.id)
        const headingId = `debug-panel-${panel.id}`
        return (
          <section key={panel.id} aria-labelledby={headingId} className="flex flex-col">
            <h3 id={headingId} className="m-0">
              <button
                type="button"
                aria-expanded={open}
                onClick={() => toggle(panel.id)}
                className={cn(
                  sectionLabel,
                  "flex w-full items-center gap-1 px-2 py-1.5 text-left text-fg-muted hover:bg-bg-muted focus-visible:ring-1 focus-visible:ring-accent focus-visible:outline-none",
                )}
              >
                <ChevronRight
                  aria-hidden
                  className={cn("h-3 w-3 transition-transform", open && "rotate-90")}
                />
                {panel.label}
              </button>
            </h3>
            {open && <div className="px-1.5 pb-2">{panel.render()}</div>}
          </section>
        )
      })}
    </div>
  )
}

function TabStrip({ className }: { className?: string }) {
  const [selected, setSelected] = useState(PANELS[0].id)
  return (
    <Tabs selectedKey={selected} onSelectionChange={setSelected} className={className}>
      <TabList aria-label="Debug panels" className="gap-4">
        {PANELS.map((panel) => (
          <Tab key={panel.id} id={panel.id}>
            {panel.label}
          </Tab>
        ))}
      </TabList>
      {PANELS.map((panel) => (
        <TabPanel key={panel.id} id={panel.id} className="max-h-64 overflow-auto px-1 pt-2">
          {panel.render()}
        </TabPanel>
      ))}
    </Tabs>
  )
}
