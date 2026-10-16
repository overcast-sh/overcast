/**
 * WebFunctionNode — a Lambda Web Function on the map.
 *
 * The function's name opens its page; under it, one row per endpoint with its
 * type and authentication and — the thing a developer opens the map to see —
 * which revisions it serves and in what proportion, e.g. `rev-ab12c 90% ·
 * rev-de34f 10%`. The header, the rows, the flashes and the ghost of a deleted
 * endpoint all read the one overlay (data-lake-overlay.ts), so a canary split
 * moving shows in the row, the count and the pulse at once.
 *
 * Nothing serves an endpoint's domain yet, so the node shows no domain and no
 * link to one; the endpoints tab has it, with the caveat.
 */

import { memo } from "react"
import type { NodeProps } from "@xyflow/react"
import { Badge } from "@/components/ui/badge"
import { formatQuantity } from "@/lib/format"
import { cn } from "@/lib/utils"
import {
  areDataLakeNodePropsEqual,
  useDataLakeOverlay,
  type DataLakeNodeData,
} from "./data-lake-context"
import { DATA_MORE_H } from "./data-lake-layout"
import { DataLakeCard } from "./data-lake-node"
import {
  nodeSuffix,
  ROW_GHOST_TTL,
  rowKey,
  WEB_NODE_ROW,
  WEB_REVISIONS_ROW,
  type RowOverlay,
} from "./data-lake-overlay"
import { TickCount } from "./data-table-row"
import { SERVICE_THEME, toSweep } from "./map-theme"
import { nodeRoute, useNodeNavigation, type NodeRoute } from "./node-route"
import {
  ENDPOINT_ROW_H,
  endpointList,
  servesText,
  type DisplayEndpoint,
} from "./web-function-layout"

/** How long a write flash sweeps across the node or a row (ms) — the node sweep's length. */
const FLASH_TTL = 2_000

const TYPE_LABEL: Record<string, string> = {
  HomeRegion: "home",
  MultiRegion: "multi",
  PerRegion: "per-region",
}

export const WebFunctionNode = memo(function WebFunctionNode({ data }: NodeProps) {
  const nodeData = data as DataLakeNodeData
  const node = nodeData.topologyNode
  const overlay = useDataLakeOverlay()
  const suffix = nodeSuffix(node.id)
  const list = endpointList(node.webEndpoints ?? [], overlay.ghosts[suffix])
  const route = nodeRoute({ service: node.service, label: node.label })
  const color = SERVICE_THEME[node.service]?.css ?? "currentColor"
  // A row the overlay has not seen an event for is not in it.
  const nodeRow = overlay.rows[rowKey(suffix, WEB_NODE_ROW)] as RowOverlay | undefined
  const revisionsRow = overlay.rows[rowKey(suffix, WEB_REVISIONS_ROW)] as RowOverlay | undefined
  const nodeFlashes = nodeRow?.flashes ?? 0
  const revisionTicks = revisionsRow?.ticks

  return (
    <DataLakeCard
      data={nodeData}
      route={route}
      subtitle={
        <>
          <span className="shrink-0">Web function</span>
          <span aria-hidden>·</span>
          <TickCount ticks={revisionTicks}>
            <span className="truncate">{formatQuantity(node.webRevisions ?? 0, "revision")}</span>
          </TickCount>
        </>
      }
    >
      {nodeFlashes > 0 && (
        <span
          key={nodeFlashes}
          aria-hidden
          className="pointer-events-none absolute inset-0 rounded-lg"
          style={{
            background: `linear-gradient(90deg, transparent 0%, ${toSweep(color)} 50%, transparent 100%)`,
            animation: `overcastSweep ${FLASH_TTL}ms ease-out forwards`,
          }}
        />
      )}
      {list.items.length > 0 && (
        <div className="map-detail -mx-1.5 mt-1.5 flex flex-col">
          {list.items.map((item) => (
            <EndpointRow
              key={item.name}
              item={item}
              route={route}
              region={node.region}
              color={color}
              overlay={overlay.rows[rowKey(suffix, item.name)]}
            />
          ))}
          {list.hidden > 0 && (
            <p
              className="flex items-center px-1.5 text-2xs text-fg-muted"
              style={{ height: DATA_MORE_H }}
            >
              +{formatQuantity(list.hidden, "more endpoint")}
            </p>
          )}
        </div>
      )}
    </DataLakeCard>
  )
}, areDataLakeNodePropsEqual)

interface EndpointRowProps {
  item: DisplayEndpoint
  route: NodeRoute | null
  region: string
  color: string
  overlay?: RowOverlay
}

function EndpointRow({ item, route, region, color, overlay }: EndpointRowProps) {
  const endpointsTab = route ? { ...route, search: { tab: "endpoints" } } : null
  const { open, openInNewTab } = useNodeNavigation(endpointsTab, region)
  if (!item.endpoint) return <GhostEndpointRow name={item.name} />
  const endpoint = item.endpoint
  const flashes = overlay?.flashes ?? 0
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation()
        open()
      }}
      onMouseDown={openInNewTab}
      aria-label={`Endpoint ${endpoint.name}: ${servesText(endpoint.serves)}`}
      className="relative flex w-full flex-col justify-center gap-0.5 overflow-hidden rounded px-1.5 text-left hover:bg-bg-muted focus-visible:bg-bg-muted"
      style={{ height: ENDPOINT_ROW_H }}
    >
      {flashes > 0 && (
        <span
          key={flashes}
          aria-hidden
          className="pointer-events-none absolute inset-0"
          style={{
            background: `linear-gradient(90deg, transparent 0%, ${toSweep(color)} 50%, transparent 100%)`,
            animation: `overcastSweep ${FLASH_TTL}ms ease-out forwards`,
          }}
        />
      )}
      <span className="flex min-w-0 items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate font-mono text-xs">{endpoint.name}</span>
        <Badge variant="outline" className="px-1 py-0">
          {TYPE_LABEL[endpoint.type] ?? endpoint.type}
        </Badge>
        <Badge
          variant={endpoint.authType === "IamAuth" ? "accent" : "default"}
          className="px-1 py-0"
        >
          {endpoint.authType === "IamAuth" ? "IAM" : "app"}
        </Badge>
      </span>
      <span
        className={cn(
          "truncate font-mono text-2xs",
          endpoint.serves.length === 0 ? "text-fg-subtle italic" : "text-fg-muted",
        )}
      >
        {endpoint.autoDeployment === "LatestRevision" && (
          <span className="mr-1 text-accent" title="Follows the newest revision">
            latest
          </span>
        )}
        {servesText(endpoint.serves)}
      </span>
    </button>
  )
}

/** An endpoint that was just deleted: struck through and fading, not clickable. */
function GhostEndpointRow({ name }: { name: string }) {
  return (
    <div
      className="flex items-center gap-2 px-1.5 text-xs text-fg-subtle"
      style={{
        height: ENDPOINT_ROW_H,
        animation: `overcastGhostFade ${ROW_GHOST_TTL}ms linear forwards`,
      }}
    >
      <span className="min-w-0 flex-1 truncate font-mono line-through">{name}</span>
      <span className="shrink-0 text-2xs">deleted</span>
    </div>
  )
}
