import { useQuery } from "@tanstack/react-query"
import { Tooltip } from "@/components/ui/tooltip"
import { Spinner } from "@/components/ui/primitives"
import { useFavourites } from "@/hooks/use-favourites"
import { useLocalStorage } from "@/hooks/use-local-storage"
import { healthQueryOptions } from "@/hooks/use-health"
import type { EmulationTier, HealthResponse } from "@/types/common"
import { ALL_SERVICES, type ServiceTierEntry } from "./service-defs"
import { AvailableServiceCard } from "./components/available-service-card"
import { DashboardHeader, type DashboardView } from "./components/dashboard-header"
import { DashboardSection } from "./components/dashboard-section"
import { NotEmulatedChips } from "./components/not-emulated-chips"
import { ServiceCard } from "./components/service-card"
import { ServiceListView } from "./components/service-list-view"
import { formatQuantity } from "@/lib/format"

export const DASHBOARD_VIEW_STORAGE_KEY = "overcast.dashboard.view"

/** Sections group services by how completely they are emulated. */
const SECTION_BY_TIER = {
  full: "fully",
  partial: "partially",
  inert: "partially",
  stub: "not",
  unsupported: "not",
} satisfies Record<EmulationTier, "fully" | "partially" | "not">

type SectionKey = (typeof SECTION_BY_TIER)[EmulationTier]

export function Dashboard() {
  const { data, isLoading } = useQuery(healthQueryOptions)
  const { recentServices, addRecentService } = useFavourites()
  const [view, setView] = useLocalStorage<DashboardView>(DASHBOARD_VIEW_STORAGE_KEY, "grid")

  // No health payload (loading or error) means the emulator's view is unknown —
  // fall back to treating every registry service as fully emulated.
  const tierOf = (name: string): EmulationTier => data?.serviceTiers[name] ?? "full"

  // Recently visited first (recentServices holds route paths), then A-Z.
  const recentRank = new Map(recentServices.map((key, index) => [key, index]))
  const entries: ServiceTierEntry[] = ALL_SERVICES.map((service) => ({
    service,
    tier: tierOf(service.name),
  })).sort((a, b) => {
    const rankA = recentRank.get(a.service.to) ?? Number.MAX_SAFE_INTEGER
    const rankB = recentRank.get(b.service.to) ?? Number.MAX_SAFE_INTEGER
    return (
      rankA - rankB ||
      a.service.label.localeCompare(b.service.label)
    )
  })

  const inSection = (section: SectionKey) =>
    entries.filter((e) => SECTION_BY_TIER[e.tier] === section)
  const fullyEmulated = inSection("fully")
  const partiallyEmulated = inSection("partially")
  const notEmulated = inSection("not")

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-20">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-7xl">
      <DashboardHeader
        totalCount={ALL_SERVICES.length}
        view={view}
        onViewChange={setView}
      />

      {view === "list" ? (
        <ServiceListView
          onNavigate={addRecentService}
          groups={[
            { title: "fully emulated", tone: "strong", entries: fullyEmulated },
            { title: "partially emulated", tone: "muted", entries: partiallyEmulated },
            { title: "not emulated", tone: "subtle", entries: notEmulated },
          ]}
        />
      ) : (
        <div className="flex flex-col gap-6">
          <DashboardSection title="fully emulated" count={fullyEmulated.length} tone="strong">
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4 2xl:grid-cols-5">
              {fullyEmulated.map((entry) => (
                <ServiceCard key={entry.service.name} entry={entry} onNavigate={addRecentService} />
              ))}
            </div>
          </DashboardSection>

          <DashboardSection
            title="partially emulated"
            count={partiallyEmulated.length}
            tone="muted"
          >
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4 2xl:grid-cols-5">
              {partiallyEmulated.map((entry) => (
                <AvailableServiceCard
                  key={entry.service.name}
                  entry={entry}
                  onNavigate={addRecentService}
                />
              ))}
            </div>
          </DashboardSection>

          <DashboardSection
            title="not emulated"
            count={notEmulated.length}
            tone="subtle"
            note="no operations implemented"
          >
            <NotEmulatedChips entries={notEmulated} />
          </DashboardSection>
        </div>
      )}

      {data && <DashboardFooter data={data} />}
    </div>
  )
}

function DashboardFooter({ data }: { data: HealthResponse }) {
  const overrides = data.storage.serviceOverrides ?? {}
  const overrideCount = Object.keys(overrides).length

  return (
    <p className="mt-6 text-center font-mono text-2xs text-fg-subtle">
      Emulator {data.version} &middot; {ALL_SERVICES.length} services
      &middot; storage: {data.storage.default}
      {overrideCount > 0 && (
        <Tooltip
          content={
            <ul className="space-y-0.5 text-left">
              {Object.entries(overrides).map(([svc, mode]) => (
                <li key={svc}>
                  {svc}: {mode}
                </li>
              ))}
            </ul>
          }
        >
          <span className="ml-1 cursor-help border-b border-dotted border-fg-subtle">
            ({formatQuantity(overrideCount, "override")})
          </span>
        </Tooltip>
      )}
    </p>
  )
}
