import { PinButton } from "@/components/service/pin-button"
import { ServiceIconTile } from "@/components/service/service-icon-tile"
import { cn } from "@/lib/utils"
import type { ServiceTierEntry } from "../service-defs"
import { ServiceTile, ServiceTileLink, TILE_FOCUS_RING } from "./service-tile"
import { TierMeta } from "./tier-badge"

export function AvailableServiceCard({
  entry,
  onNavigate,
}: {
  entry: ServiceTierEntry
  onNavigate: (key: string) => void
}) {
  const { service, tier } = entry

  return (
    <ServiceTile
      className="group flex items-center gap-2.5 rounded-card border border-border p-3"
      interactiveClassName={cn(
        "transition-colors hover:border-accent hover:bg-bg-elevated",
        TILE_FOCUS_RING,
      )}
    >
      <ServiceIconTile service={service} variant="outline" size={26} iconSize={15} />
      <span className="flex min-w-0 flex-1 flex-col gap-px">
        <ServiceTileLink
          entry={entry}
          onNavigate={onNavigate}
          className="truncate font-mono text-xs font-bold text-fg-muted"
        >
          {service.label}
        </ServiceTileLink>
        <TierMeta tier={tier} />
      </span>
      {service.pinKey && (
        <PinButton
          serviceKey={service.pinKey}
          label={service.label}
          reveal="pinned"
          className="relative z-10 -mr-0.5"
        />
      )}
    </ServiceTile>
  )
}
