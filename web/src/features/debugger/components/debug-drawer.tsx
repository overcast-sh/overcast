/**
 * The drawer under the code pane while a session is open: Logs and the
 * Debug console, one at a time (docs/plans/compute-debugger-console.md
 * § 3.5). The console tab carries a count of entries since it was last
 * looked at, so output arriving while Logs is up is not missed.
 */
import { useState } from "react"
import { Badge } from "@/components/ui/badge"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import { cn } from "@/lib/utils"
import { useDebugSessionState } from "../session/hooks"
import { DebugConsole } from "./debug-console"
import { DebugLogs } from "./debug-logs"

type DrawerTab = "logs" | "console"

export function DebugDrawer({
  logGroup,
  className,
}: {
  logGroup: string | null
  className?: string
}) {
  const [selected, setSelected] = useState<DrawerTab>("console")
  const entryCount = useDebugSessionState((s) => s.console.length)
  // The count the console tab was last opened at; what has arrived since is the badge.
  const [seen, setSeen] = useState(0)
  const unseen = selected === "console" ? 0 : Math.max(0, entryCount - seen)

  const select = (key: string) => {
    setSeen(entryCount)
    setSelected(key as DrawerTab)
  }

  return (
    <Tabs
      selectedKey={selected}
      onSelectionChange={select}
      className={cn("flex min-h-0 flex-col", className)}
    >
      <TabList aria-label="Debug drawer" className="gap-4">
        <Tab id="logs">Logs</Tab>
        <Tab id="console">
          Debug console
          {unseen > 0 && (
            <Badge variant="accent" className="ml-1.5" aria-label={`${unseen} new entries`}>
              {unseen}
            </Badge>
          )}
        </Tab>
      </TabList>
      <TabPanel id="logs" className="flex min-h-0 flex-1 flex-col pt-2">
        <DebugLogs logGroup={logGroup} />
      </TabPanel>
      <TabPanel id="console" className="flex min-h-0 flex-1 flex-col pt-2">
        <DebugConsole />
      </TabPanel>
    </Tabs>
  )
}
