import type { KeyboardEvent } from "react"
import { IndentDecrease, IndentIncrease, Plus, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { RowAction } from "@/components/ui/resource-list-page"
import { Select } from "@/components/ui/select"
import {
  maxDepth,
  newSchemaRow,
  PRIMITIVE_TYPES,
  type ColumnType,
  type SchemaRow,
} from "../create-table-model"

/**
 * The columns of a new table, one row each. A `struct` column's fields are
 * the rows indented under it — `⌥→` / `⌥←` in a name, or the indent buttons —
 * so a nested schema is typed top to bottom the way it reads.
 */
export function SchemaBuilder({
  rows,
  onChange,
}: {
  rows: SchemaRow[]
  onChange: (rows: SchemaRow[]) => void
}) {
  const update = (index: number, patch: Partial<SchemaRow>) =>
    onChange(rows.map((r, i) => (i === index ? { ...r, ...patch } : r)))

  /** Moves a row and the fields under it one level in or out. */
  const shift = (index: number, delta: 1 | -1) => {
    const row = rows[index]
    const target = row.depth + delta
    if (target < 0 || target > maxDepth(rows, index)) return
    let end = index + 1
    while (end < rows.length && rows[end].depth > row.depth) end++
    onChange(rows.map((r, i) => (i >= index && i < end ? { ...r, depth: r.depth + delta } : r)))
  }

  const remove = (index: number) => {
    let end = index + 1
    while (end < rows.length && rows[end].depth > rows[index].depth) end++
    onChange(rows.filter((_, i) => i < index || i >= end))
  }

  const onNameKeyDown = (index: number) => (event: KeyboardEvent) => {
    if (!event.altKey) return
    if (event.key === "ArrowRight") shift(index, 1)
    else if (event.key === "ArrowLeft") shift(index, -1)
    else return
    event.preventDefault()
  }

  /** A new row lands where typing continues: under a struct, as its first field. */
  const add = () => onChange([...rows, newSchemaRow(maxDepth(rows, rows.length))])

  return (
    <div className="flex flex-col gap-1.5" role="group" aria-label="Columns">
      {rows.map((row, index) => (
        <div key={row.key} className="flex items-center gap-2">
          {row.depth > 0 && (
            // One step per level, with a rule marking the struct it belongs to.
            <div
              aria-hidden
              className="h-8 shrink-0 border-l border-border"
              style={{ width: `${row.depth * 1.25}rem` }}
            />
          )}
          <Input
            aria-label={`Column ${index + 1} name`}
            placeholder={row.depth > 0 ? "field" : "column"}
            value={row.name}
            onChange={(event) => update(index, { name: event.target.value })}
            onKeyDown={onNameKeyDown(index)}
            className="min-w-0 flex-1"
          />
          <Select
            aria-label={`Column ${index + 1} type`}
            value={row.type}
            onChange={(event) => update(index, { type: event.target.value as ColumnType })}
            className="w-36"
          >
            {PRIMITIVE_TYPES.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
            <option value="struct">struct</option>
          </Select>
          <label className="flex shrink-0 items-center gap-1.5 text-xs text-fg-muted">
            <input
              type="checkbox"
              checked={row.required}
              onChange={(event) => update(index, { required: event.target.checked })}
              className="accent-accent"
            />
            required
          </label>
          <RowAction
            type="button"
            label={`Outdent column ${index + 1}`}
            onClick={() => shift(index, -1)}
            disabled={row.depth === 0}
          >
            <IndentDecrease className="size-3.5" />
          </RowAction>
          <RowAction
            type="button"
            label={`Indent column ${index + 1} under the struct above`}
            onClick={() => shift(index, 1)}
            disabled={row.depth >= maxDepth(rows, index)}
          >
            <IndentIncrease className="size-3.5" />
          </RowAction>
          <RowAction
            type="button"
            label={`Remove column ${index + 1}`}
            tone="danger"
            onClick={() => remove(index)}
          >
            <X className="size-3.5" />
          </RowAction>
        </div>
      ))}
      <div>
        <Button type="button" size="sm" variant="ghost" onClick={add}>
          <Plus className="size-3.5" />
          Add column
        </Button>
      </div>
    </div>
  )
}
