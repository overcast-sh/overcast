import { LayoutGrid, List } from "lucide-react"
import { SegmentedControl, type SegmentedOption } from "@/components/ui/segmented-control"
import { formatQuantity } from "@/lib/format"

export type DashboardView = "grid" | "list"

const VIEWS = [
  { value: "grid", label: "Grid view", icon: LayoutGrid },
  { value: "list", label: "List view", icon: List },
] as const satisfies readonly SegmentedOption<DashboardView>[]

export function DashboardHeader({
  totalCount,
  view,
  onViewChange,
}: {
  totalCount: number
  view: DashboardView
  onViewChange: (view: DashboardView) => void
}) {
  return (
    <div className="mb-6 flex items-center gap-4">
      <h1 className="font-mono text-[22px] font-bold tracking-[-0.02em] text-fg">Dashboard</h1>
      <span className="font-mono text-xs text-fg-muted">
        {formatQuantity(totalCount, "service")}
      </span>
      <SegmentedControl
        label="Dashboard layout"
        value={view}
        options={VIEWS}
        onChange={onViewChange}
        iconOnly
        className="ml-auto"
      />
    </div>
  )
}
