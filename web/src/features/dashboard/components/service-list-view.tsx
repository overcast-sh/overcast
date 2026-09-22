import { ScrollX } from "@/components/ui/scroll-x"
import { fieldLabel, sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { useServiceIconColor } from "@/hooks/use-service-icon-color"
import { PinButton } from "@/components/service/pin-button"
import type { ServiceTierEntry } from "../service-defs"
import { TIER_BADGE } from "../tiers"
import type { SectionTone } from "./dashboard-section"
import { ServiceTile, ServiceTileLink, TILE_FOCUS_RING } from "./service-tile"

/**
 * `min-w-max` on the description column rather than a bare `1fr`: inside the
 * scroller the grid is free to be wider than the pane, and `1fr` would collapse
 * the column to nothing instead of letting the row overflow. The named columns
 * already total ~400px, so below about 580px the row scrolls (#1611) — before
 * this it simply ran off the right edge of a card with `overflow-hidden` and
 * took Scope and Tier with it.
 */
const GRID =
  "grid grid-cols-[26px_200px_minmax(180px,1fr)_120px_18px] gap-3 items-center min-w-[36rem]"

const TONE_CLASS: Record<SectionTone, string> = {
  strong: "text-fg",
  muted: "text-fg-muted",
  subtle: "text-fg-subtle",
}

const COLUMNS = ["", "service", "scope", "tier", ""]

export interface ServiceListGroup {
  title: string
  tone: SectionTone
  entries: ServiceTierEntry[]
}

export function ServiceListView({
  groups,
  onNavigate,
}: {
  groups: ServiceListGroup[]
  onNavigate: (key: string) => void
}) {
  return (
    // The card's own `overflow-hidden` is what rounds its corners; the scroller
    // lives inside it, so the rows can run wider than the pane without the
    // corners squaring off.
    <ScrollX className="overflow-hidden rounded-card border border-border bg-bg-elevated">
      <div role="table" aria-label="Services">
        <div
          role="row"
          className={cn(
            GRID,
            fieldLabel,
            "border-b border-border bg-bg px-4 py-[7px] text-fg-subtle",
          )}
        >
          {COLUMNS.map((column, i) => (
            <span role="columnheader" key={column || i}>
              {column}
            </span>
          ))}
        </div>
        {groups.map((group) => (
          <div key={group.title} role="rowgroup">
            <div
              role="row"
              className={cn(
                GRID,
                sectionLabel,
                "border-b border-border bg-bg px-4 py-1.5",
                TONE_CLASS[group.tone],
              )}
            >
              <span role="rowheader" className="col-span-5">
                {group.title} &middot; {group.entries.length}
              </span>
            </div>
            {group.entries.map((entry) => (
              <ServiceListRow key={entry.service.name} entry={entry} onNavigate={onNavigate} />
            ))}
          </div>
        ))}
      </div>
    </ScrollX>
  )
}

function ServiceListRow({
  entry,
  onNavigate,
}: {
  entry: ServiceTierEntry
  onNavigate: (key: string) => void
}) {
  const { service, tier } = entry
  const Icon = service.icon
  const { enabled: colorEnabled } = useServiceIconColor()
  return (
    <ServiceTile
      role="row"
      className={cn(GRID, "group border-b border-border px-4 py-2 last:border-b-0")}
      interactiveClassName={cn(
        "transition-colors hover:bg-accent-muted",
        TILE_FOCUS_RING,
        // Inset: the rows sit edge to edge inside a clipped card.
        "has-[a:focus-visible]:-outline-offset-2",
      )}
    >
      <span role="cell">
        <Icon
          className={cn("h-4 w-4", colorEnabled ? service.color : "text-accent")}
          strokeWidth={1.75}
        />
      </span>
      <span role="cell" className="truncate font-mono text-[13px] font-bold text-fg">
        <ServiceTileLink entry={entry} onNavigate={onNavigate}>
          {service.label}
        </ServiceTileLink>
      </span>
      <span role="cell" className="truncate text-xs text-fg-subtle">
        {service.description}
      </span>
      <span role="cell" className="font-mono text-xs text-accent">
        {TIER_BADGE[tier]?.label ?? "Full"}
      </span>
      <span role="cell" className="flex justify-end">
        {service.pinKey && (
          <PinButton
            serviceKey={service.pinKey}
            label={service.label}
            reveal="pinned"
            className="relative z-10"
          />
        )}
      </span>
    </ServiceTile>
  )
}
