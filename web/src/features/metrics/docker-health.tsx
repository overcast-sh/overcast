/**
 * DockerHealthPanel — Docker connectivity table for the Metrics & Health
 * page. Shows per-service connection status and the socket each service is
 * wired to, plus a warning when any service is disconnected.
 *
 * It sits at the foot of the page: it is per-service diagnostics, read when
 * something is already suspected, and the amber banner above the table is
 * what makes it worth scrolling to.
 */
import { useQuery } from "@tanstack/react-query"
import { healthQueryOptions } from "@/hooks/use-health"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { sectionLabel } from "@/lib/typography"
import { AlertCircle } from "lucide-react"
import type { DockerServiceHealth } from "@/types/common"
import { formatQuantity } from "@/lib/format"

function healthBadge(svc: DockerServiceHealth) {
  if (svc.connected) return <Badge variant="success">Connected</Badge>
  return <Badge variant="danger">Disconnected</Badge>
}

export function DockerHealthPanel() {
  const { data: health } = useQuery(healthQueryOptions)
  const docker = health?.docker
  if (!docker || docker.services.length === 0) return null

  const disconnected = docker.services.filter((s) => !s.connected)

  const connected = docker.services.length - disconnected.length

  return (
    <div className="flex flex-col gap-3">
      {/* The count answers the whole section for a reader who is only
          checking, so it sits on the heading's row — the table below is for
          the reader who needs to know *which* service. */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h2 className={cn(sectionLabel, "shrink-0 text-fg-muted")}>Docker</h2>
        <p className="text-xs text-fg-muted">
          {connected} of {docker.services.length} services connected
        </p>
      </div>

      {disconnected.length > 0 && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-lg border border-warning/30 bg-warning-muted p-3 text-sm text-warning"
        >
          <AlertCircle size={16} className="mt-0.5 shrink-0" />
          <div>
            <p className="font-medium">
              {docker.available
                ? `${formatQuantity(disconnected.length, "service")} disconnected`
                : "Docker is not available"}
            </p>
            <p className="text-xs text-warning/70">
              Disconnected services operate in metadata-only mode — resources have no running
              containers.
            </p>
          </div>
        </div>
      )}

      <div className="overflow-hidden rounded-lg border border-border">
        <table className="w-full text-xs">
          <thead>
            <tr className="border-b border-border bg-bg-muted/50">
              <th className="px-3 py-2 text-left font-medium text-fg-muted">Service</th>
              <th className="px-3 py-2 text-left font-medium text-fg-muted">Status</th>
              <th className="hidden px-3 py-2 text-left font-medium text-fg-muted sm:table-cell">
                Socket
              </th>
            </tr>
          </thead>
          <tbody>
            {docker.services.map((svc) => (
              <tr key={svc.service} className="border-b border-border last:border-0">
                <td className="px-3 py-2 font-mono uppercase">{svc.service}</td>
                <td className="px-3 py-2">{healthBadge(svc)}</td>
                {/* The socket is the one column that actually differs per row.
                    A "Last Event" column used to sit here, repeating the same
                    daemon-wide value on every line; it is reported once, in the
                    footer below. */}
                <td className="hidden px-3 py-2 text-fg-muted sm:table-cell">
                  {svc.socket ? (
                    <span className="font-mono text-2xs">{svc.socket}</span>
                  ) : (
                    <span className="text-fg-subtle">—</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {docker.lastEventAt && (
        <p className="text-xs text-fg-subtle">
          Last Docker event
          {docker.lastEvent && (
            <span className="font-mono text-fg-muted"> {docker.lastEvent}</span>
          )}{" "}
          at{" "}
          {new Date(docker.lastEventAt).toLocaleTimeString(undefined, {
            hour: "2-digit",
            minute: "2-digit",
            second: "2-digit",
          })}
        </p>
      )}
    </div>
  )
}
