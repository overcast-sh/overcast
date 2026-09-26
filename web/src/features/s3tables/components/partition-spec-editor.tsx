import { Plus, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { RowAction } from "@/components/ui/resource-list-page"
import { Select } from "@/components/ui/select"
import { createId } from "@/lib/id"
import {
  transformsFor,
  type ColumnType,
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
  /** The top-level columns that can be partitioned by, with their types. */
  columns: { name: string; type: ColumnType }[]
  rows: PartitionRow[]
  onChange: (rows: PartitionRow[]) => void
}) {
  const update = (index: number, patch: Partial<PartitionRow>) =>
    onChange(rows.map((r, i) => (i === index ? { ...r, ...patch } : r)))
  const usable = columns.filter((c) => c.name !== "")
  const typeOf = (name: string) => usable.find((c) => c.name === name)?.type
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
            {transformsFor(typeOf(row.column)).map((t) => (
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
            onChange={(event) => {
              const column = event.target.value
              // A transform the new column cannot take falls back to identity.
              const valid = transformsFor(typeOf(column)).includes(row.transform)
              update(index, { column, transform: valid ? row.transform : "identity" })
            }}
            className="min-w-0 flex-1"
          >
            {!typeOf(row.column) && <option value={row.column}>pick a column</option>}
            {usable.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
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
            onChange([
              ...rows,
              { key: createId(), column: usable[0]?.name ?? "", transform: "identity" },
            ])
          }
        >
          <Plus className="size-3.5" />
          Add partition field
        </Button>
      </div>
    </div>
  )
}
