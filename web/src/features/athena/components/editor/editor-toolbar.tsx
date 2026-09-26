import { useQuery } from "@tanstack/react-query"
import { AlignLeft, MoreHorizontal, Play, Save, Square, TextSelect } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/ui/menu"
import { Select } from "@/components/ui/select"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import { AWS_COMMAND_FLAVORS, copyAwsCommand, type AwsCommandFlavor } from "@/lib/aws-command"
import type { AthenaEngineStatus } from "@/types"
import { workGroupQueryOptions, workGroupsQueryOptions } from "../../data"
import { startQueryCall } from "../../query-input"
import type { QueryTab } from "../../query-tabs"
import { EngineStatusChip } from "./engine-status"

const MOD =
  typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform) ? "⌘" : "Ctrl+"

export interface EditorToolbarProps {
  tab: QueryTab
  onWorkGroupChange: (workGroup: string) => void
  running: boolean
  stopping: boolean
  onRun: () => void
  onRunSelection: () => void
  onStop: () => void
  onFormat: () => void
  onSave: () => void
  engine: AthenaEngineStatus | undefined
}

/**
 * The editor's toolbar: the workgroup the query runs in (with *enforced*
 * when its settings override the query's), Run, Run selection, Stop while
 * running, Format, Save as named query, and the *Copy as* snippets.
 */
export function EditorToolbar({
  tab,
  onWorkGroupChange,
  running,
  stopping,
  onRun,
  onRunSelection,
  onStop,
  onFormat,
  onSave,
  engine,
}: EditorToolbarProps) {
  const workGroups = useQuery(workGroupsQueryOptions())
  const { data: workGroup } = useQuery(workGroupQueryOptions(tab.workGroup))
  const enforced = workGroup?.Configuration?.EnforceWorkGroupConfiguration === true
  const names = workGroups.data?.map((w) => w.Name ?? "") ?? []
  const { copy } = useCopyToClipboard()

  return (
    <div
      role="toolbar"
      aria-label="Query"
      className="flex flex-wrap items-center gap-x-2 gap-y-1.5"
    >
      <label className="flex items-center gap-2 font-mono text-2xs text-fg-subtle">
        workgroup
        <Select
          value={tab.workGroup}
          onChange={(e) => onWorkGroupChange(e.target.value)}
          className="h-7 w-40"
        >
          {(names.includes(tab.workGroup) ? names : [tab.workGroup, ...names]).map((name) => (
            <option key={name}>{name}</option>
          ))}
        </Select>
      </label>
      {enforced && (
        <Badge
          variant="info"
          title="This workgroup's settings override the query's, including where its results go"
        >
          enforced
        </Badge>
      )}
      <Button
        size="sm"
        onClick={onRun}
        busy={running && !stopping}
        busyLabel="Running"
        title={`Run (${MOD}⏎)`}
      >
        <Play aria-hidden className="size-3.5" />
        Run
        <kbd className="font-mono opacity-70">{MOD}⏎</kbd>
      </Button>
      <Button
        size="sm"
        variant="outline"
        onClick={onRunSelection}
        disabled={running}
        title={`Run the selected SQL (⇧${MOD}⏎)`}
      >
        <TextSelect aria-hidden className="size-3.5" />
        Run selection
      </Button>
      {running && (
        <Button
          size="sm"
          variant="danger-ghost"
          onClick={onStop}
          busy={stopping}
          busyLabel="Stopping"
          title="Stop (esc in the editor)"
        >
          <Square aria-hidden className="size-3.5" />
          Stop
        </Button>
      )}
      <Button
        size="sm"
        variant="ghost"
        onClick={onFormat}
        title="Normalise whitespace and keyword case"
      >
        <AlignLeft aria-hidden className="size-3.5" />
        Format
      </Button>
      <Button size="sm" variant="ghost" onClick={onSave}>
        <Save aria-hidden className="size-3.5" />
        Save
      </Button>
      <Menu>
        <MenuTrigger asChild>
          <Button size="icon-sm" variant="ghost" aria-label="More query actions">
            <MoreHorizontal aria-hidden className="size-4" />
          </Button>
        </MenuTrigger>
        <MenuContent>
          {(Object.keys(AWS_COMMAND_FLAVORS) as AwsCommandFlavor[]).map((flavor) => (
            <MenuItem
              key={flavor}
              onSelect={() => copyAwsCommand(copy, startQueryCall(tab), flavor)}
            >
              {AWS_COMMAND_FLAVORS[flavor].label}
            </MenuItem>
          ))}
        </MenuContent>
      </Menu>
      {engine && (
        <div className="ml-auto">
          <EngineStatusChip status={engine} />
        </div>
      )}
    </div>
  )
}
