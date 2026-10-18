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
 * The data plane shows too. Each row says how many execution environments
 * (containers) its endpoint has — `0 envs` once it has scaled to zero, and the
 * node says so when every endpoint has — and moves with traffic: a request
 * pulses its endpoint's row in its status class's colour (a cold start adds a
 * marker), and an environment starting or stopping sweeps the row. Pulses are
 * fixed-length whatever the request took (CONTRIBUTING § Topology map
 * methodology); the counts are the topology's, re-read on each start and stop.
 * The node shows no domain; the endpoints tab has it, with a way to open it.
 */

import { memo } from "react"
import type { NodeProps } from "@xyflow/react"
import { Moon, Snowflake } from "lucide-react"
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
  type StatusClass,
} from "./data-lake-overlay"
import { SERVICE_THEME, toSweep } from "./map-theme"
import { nodeRoute, useNodeNavigation, type NodeRoute } from "./node-route"
import {
  ENDPOINT_ROW_H,
  endpointList,
  environmentsText,
  scalingOf,
  servesText,
  type DisplayEndpoint,
} from "./web-function-layout"

/** How long a write flash sweeps across the node or a row (ms) — the node sweep's length. */
const FLASH_TTL = 2_000
/** How long a request's pulse is on screen (ms): fixed, whatever the request took. */
const PULSE_TTL = 1_200
/** How long a cold start's marker stays (ms). */
const COLD_MARK_TTL = 3_000

/** A request's pulse by status class. */
const PULSE_COLOR: Record<StatusClass, string> = {
  "2xx": "var(--color-success)",
  "3xx": "var(--color-accent)",
  "4xx": "var(--color-warning)",
  "5xx": "var(--color-danger)",
}

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
  // The header's count and every row's chip read the same endpoints.
  const scaling = scalingOf(node.webEndpoints ?? [])

  return (
    <DataLakeCard
      data={nodeData}
      route={route}
      className={cn(scaling.scaledToZero && "border-dashed")}
      subtitle={
        <>
          <span className="shrink-0">Web function</span>
          <span aria-hidden>·</span>
          <span
            key={revisionTicks ?? 0}
            className="origin-left truncate tabular-nums"
            style={revisionTicks ? { animation: "overcastTick 600ms ease-out" } : undefined}
          >
            {formatQuantity(node.webRevisions ?? 0, "revision")}
          </span>
          {scaling.scaledToZero ? (
            <span
              role="img"
              aria-label="Scaled to zero"
              className="inline-flex shrink-0 items-center gap-0.5 italic"
              title="Scaled to zero: every endpoint has no environments. The next request starts one."
            >
              <Moon className="size-3" aria-hidden />
              {environmentsText(0)}
            </span>
          ) : (
            scaling.environments > 0 && (
              <span className="shrink-0 text-success tabular-nums">
                {environmentsText(scaling.environments)}
              </span>
            )
          )}
        </>
      }
    >
      {nodeFlashes > 0 && (
        // The sweep travels beyond the box, so it is clipped to the card.
        <span
          aria-hidden
          className="pointer-events-none absolute inset-0 overflow-hidden rounded-lg"
        >
          <span
            key={nodeFlashes}
            className="absolute inset-0"
            style={{
              background: `linear-gradient(90deg, transparent 0%, ${toSweep(color)} 50%, transparent 100%)`,
              animation: `overcastSweep ${FLASH_TTL}ms ease-out forwards`,
            }}
          />
        </span>
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
  const requests = overlay?.requests ?? 0
  const coldStarts = overlay?.coldStarts ?? 0
  const envStarts = overlay?.envStarts ?? 0
  const envStops = overlay?.envStops ?? 0
  const pulse = PULSE_COLOR[overlay?.requestClass ?? "2xx"]
  const environments = endpoint.environments
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
      {flashes > 0 && <RowSweep key={`write-${flashes}`} color={toSweep(color)} />}
      {envStarts > 0 && (
        <RowSweep key={`start-${envStarts}`} color={toSweep("var(--color-success)")} />
      )}
      {envStops > 0 && (
        <RowSweep key={`stop-${envStops}`} color={toSweep("var(--color-fg-muted)")} />
      )}
      {requests > 0 && (
        <span
          key={`req-${requests}`}
          aria-hidden
          data-request-class={overlay?.requestClass}
          className="pointer-events-none absolute inset-0 rounded"
          style={{
            boxShadow: `inset 0 0 0 1.5px ${pulse}`,
            background: `color-mix(in oklab, ${pulse} 18%, transparent)`,
            animation: `overcastPulseRing ${PULSE_TTL}ms ease-out forwards`,
          }}
        />
      )}
      <span className="flex min-w-0 items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate font-mono text-xs">{endpoint.name}</span>
        <span
          className={cn(
            "shrink-0 text-2xs tabular-nums",
            environments > 0 ? "text-success" : "text-fg-subtle italic",
          )}
          title={
            environments > 0
              ? "Execution environments running or starting"
              : "Scaled to zero: the next request starts an environment"
          }
        >
          {environmentsText(environments)}
        </span>
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
      {coldStarts > 0 && (
        <Snowflake
          key={`cold-${coldStarts}`}
          aria-label="Cold start"
          className="pointer-events-none absolute right-1.5 bottom-1 size-3 text-accent"
          style={{ animation: `overcastColdMark ${COLD_MARK_TTL}ms ease-out forwards` }}
        />
      )}
    </button>
  )
}

/** A sweep across a row: a write, or an environment that started (green) or stopped (grey). */
function RowSweep({ color }: { color: string }) {
  return (
    <span
      aria-hidden
      className="pointer-events-none absolute inset-0"
      style={{
        background: `linear-gradient(90deg, transparent 0%, ${color} 50%, transparent 100%)`,
        animation: `overcastSweep ${FLASH_TTL}ms ease-out forwards`,
      }}
    />
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
