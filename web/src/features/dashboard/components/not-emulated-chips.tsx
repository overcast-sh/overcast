import { useState } from "react"
import { Link } from "@tanstack/react-router"
import { ChevronDown } from "lucide-react"
import { sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { CATALOG, CATALOG_CATEGORY_LABELS, type CatalogEntry } from "@/lib/unsupported-services"
import type { ServiceTierEntry } from "../service-defs"
import { TIER_DESCRIPTIONS } from "../tiers"
import { ServiceTile, ServiceTileLink, TILE_FOCUS_RING } from "./service-tile"

// Unfilled by design: fill and border weight both fall off as emulation
// coverage does — filled + solid (fully emulated), unfilled + solid
// (partially), unfilled + dashed (not emulated).
const CHIP_CLASS =
  "flex items-center gap-[7px] rounded-control border border-dashed border-border px-2.5 py-1.5 font-mono text-2xs text-fg-subtle"

export function NotEmulatedChips({ entries }: { entries: ServiceTierEntry[] }) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap gap-2">
        {entries.map((entry) => (
          <ServiceChip key={entry.service.name} entry={entry} />
        ))}
      </div>
      <OtherServicesExpander />
    </div>
  )
}

function ServiceChip({ entry }: { entry: ServiceTierEntry }) {
  const { service, tier } = entry
  const Icon = service.icon

  return (
    <ServiceTile
      className={CHIP_CLASS}
      interactiveClassName={cn(
        "transition-colors hover:border-solid hover:border-accent hover:text-accent",
        TILE_FOCUS_RING,
      )}
    >
      <Icon className="h-3.5 w-3.5" strokeWidth={1.75} />
      <ServiceTileLink entry={entry} tooltip={TIER_DESCRIPTIONS[tier]}>
        {service.label}
      </ServiceTileLink>
    </ServiceTile>
  )
}

function OtherServicesExpander() {
  const [open, setOpen] = useState(false)

  const byCategory = CATALOG.reduce<Record<string, CatalogEntry[]>>((acc, entry) => {
    ;(acc[entry.category] ??= []).push(entry)
    return acc
  }, {})

  return (
    <div className="mt-1 rounded-control border border-border bg-bg-elevated">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left"
      >
        <span className="font-mono text-2xs text-fg-muted">Other AWS Services</span>
        <span className="font-mono text-2xs text-fg-subtle">{CATALOG.length}</span>
        <ChevronDown
          className={cn(
            "ml-auto h-3.5 w-3.5 text-fg-subtle transition-transform",
            open && "rotate-180",
          )}
        />
      </button>
      {open && (
        <div className="flex flex-col gap-5 border-t border-border px-3 py-4">
          {Object.entries(byCategory).map(([category, catalogEntries]) => (
            <div key={category}>
              <p className={cn(sectionLabel, "mb-2 text-fg-subtle")}>
                {CATALOG_CATEGORY_LABELS[category as keyof typeof CATALOG_CATEGORY_LABELS]}
              </p>
              <div className="flex flex-wrap gap-2">
                {catalogEntries.map((catalogEntry) => (
                  <Link
                    key={catalogEntry.id}
                    to="/$service"
                    params={{ service: catalogEntry.id }}
                    className={cn(
                      CHIP_CLASS,
                      "transition-colors hover:border-solid hover:border-accent hover:text-accent focus-visible:outline-accent",
                    )}
                  >
                    {catalogEntry.label}
                  </Link>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
