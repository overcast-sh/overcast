/**
 * The card Athena, Glue and S3 Tables nodes share: the service's icon, the
 * resource's name (the keyboard route to its page), a line under it, one
 * on-node action, and the node's own list below.
 */

import { useState, type ReactNode } from "react"
import { Handle, Position } from "@xyflow/react"
import { Box } from "lucide-react"
import { Tooltip } from "@/components/ui/tooltip"
import { SERVICES } from "@/lib/service-registry"
import { cn } from "@/lib/utils"
import type { DataLakeNodeData } from "./data-lake-context"
import { FALLBACK_COLOR, SERVICE_THEME } from "./map-theme"
import { useNodeNavigation, type NodeRoute } from "./node-route"

interface DataLakeCardProps {
  data: DataLakeNodeData
  route: NodeRoute | null
  /** The line under the name. */
  subtitle: ReactNode
  /** The node's one on-node action. */
  action?: ReactNode
  children?: ReactNode
}

const handleClass = "size-2! rounded-full! border-0! bg-fg-muted/50!"

export function DataLakeCard({ data, route, subtitle, action, children }: DataLakeCardProps) {
  const { topologyNode: node, hasTarget, hasSource, isNew } = data
  const [enter] = useState(() => (isNew ? "overcast-node-enter" : ""))
  const { open, openInNewTab } = useNodeNavigation(route, node.region)
  const meta = SERVICE_THEME[node.service]
  const Icon =
    (SERVICES as Record<string, (typeof SERVICES)[keyof typeof SERVICES] | undefined>)[node.service]
      ?.icon ?? Box

  // Like ServiceNode, the card is not itself a button: it holds rows and an
  // action. The whole surface navigates on click; the keyboard route is the
  // name, which is a real button.
  return (
    <div
      onClick={open}
      onMouseDown={openInNewTab}
      className={cn(
        "relative flex h-full flex-col rounded-lg border bg-bg-elevated px-3 py-2 text-fg shadow-sm",
        meta?.border ?? "border-border",
        route && "cursor-pointer",
        enter,
      )}
    >
      {hasTarget && <Handle type="target" position={Position.Left} className={handleClass} />}
      <div className="flex items-center gap-2.5">
        <div
          className={cn(
            "flex h-9 w-9 shrink-0 items-center justify-center rounded-md",
            meta?.bg ?? "bg-fg-muted/10",
          )}
        >
          <Icon
            className={cn("h-5 w-5", meta?.color)}
            style={meta ? undefined : { color: FALLBACK_COLOR }}
          />
        </div>
        <div className="min-w-0 flex-1">
          <Tooltip content={<span className="break-all">{node.label}</span>}>
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                open()
              }}
              className="block w-full truncate text-left text-base leading-tight font-semibold hover:underline"
            >
              {node.label}
            </button>
          </Tooltip>
          <div className="flex min-w-0 items-center gap-1.5 text-xs text-fg-subtle">{subtitle}</div>
        </div>
        {action && (
          <div className="shrink-0" onClick={(e) => e.stopPropagation()}>
            {action}
          </div>
        )}
      </div>
      {children}
      {hasSource && <Handle type="source" position={Position.Right} className={handleClass} />}
    </div>
  )
}
