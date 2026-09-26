import { fieldLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
/**
 * Small labeled value pill — shared by MetricsPage's summary band and
 * HealthPills. `justify-center` matters when the band also holds the taller
 * StartupCard: the pills stretch to its height, and a top-aligned label/value
 * pair would leave a ragged gap under each one. The value is text, or a
 * status badge.
 */
export function StatPill({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex flex-col justify-center gap-0.5 rounded-md border border-border bg-bg-elevated px-3 py-2">
      <span className={cn(fieldLabel, "text-fg-muted")}>{label}</span>
      <span className="font-mono text-sm font-medium text-fg">{value}</span>
    </div>
  )
}
