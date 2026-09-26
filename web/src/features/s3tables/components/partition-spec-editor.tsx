import { Plus, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { RowAction } from "@/components/ui/resource-list-page"
import { Select } from "@/components/ui/select"
import { createId } from "@/lib/id"
import {
  PARTITION_TRANSFORMS,
  type PartitionRow,
  type PartitionTransform,
} from "../create-table-model"

/**
 * The table's partition spec: a transform of a top-level column per row —
 * `day(ordered_at)`, `bucket[16](customer_id)`. Optional; a table without
 * one is unpartitioned.
 */
export function PartitionSpecEditor({
  columns,
  rows,
  onChange,
}: {
  /** The top-level columns that can be partitioned by, by name. */
  columns: string[]
  rows: PartitionRow[]
  onChange: (rows: PartitionRow[]) => void
}) {
  const update = (index: number, patch: Partial<PartitionRow>) =>
    onChange(rows.map((r, i) => (i === index ? { ...r, ...patch } : r)))
  const usable = columns.filter((c) => c !== "")
  return (
    <div className="flex flex-col gap-1.5" role="group" aria-label="Partition fields">
      {rows.map((row, index) => (
        <div key={row.key} className="flex items-center gap-2">
          <Select
            aria-label={`Partition ${index + 1} transform`}
            value={row.transform}
            onChange={(event) =>
              update(index, { transform: event.target.value as PartitionTransform })
            }
            className="w-36"
          >
            {PARTITION_TRANSFORMS.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </Select>
          <span aria-hidden className="font-mono text-xs text-fg-subtle">
            of
          </span>
          <Select
            aria-label={`Partition ${index + 1} column`}
            value={row.column}
            onChange={(event) => update(index, { column: event.target.value })}
            className="min-w-0 flex-1"
          >
            {!usable.includes(row.column) && <option value={row.column}>pick a column</option>}
            {usable.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </Select>
          <RowAction
            type="button"
            label={`Remove partition ${index + 1}`}
            tone="danger"
            onClick={() => onChange(rows.filter((_, i) => i !== index))}
          >
            <X className="size-3.5" />
          </RowAction>
        </div>
      ))}
      <div>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          disabled={usable.length === 0}
          onClick={() =>
            onChange([...rows, { key: createId(), column: usable[0] ?? "", transform: "identity" }])
          }
        >
          <Plus className="size-3.5" />
          Add partition field
        </Button>
      </div>
    </div>
  )
}
