import { useId } from "react"
import { Plus, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { FieldLabel } from "@/components/ui/primitives"
import { RowAction } from "@/components/ui/resource-list-page"
import { cn } from "@/lib/utils"
import { draftColumn, type DraftColumn } from "./draft-column"

/** The Hive types offered as suggestions; any other type string is accepted as typed. */
const HIVE_TYPES = [
  "string",
  "bigint",
  "int",
  "double",
  "float",
  "boolean",
  "date",
  "timestamp",
  "decimal(10,2)",
  "array<string>",
  "map<string,string>",
]

interface ColumnsEditorProps {
  /** Names the list for assistive tech: "Columns", "Partition keys". */
  label: string
  columns: DraftColumn[]
  onChange: (columns: DraftColumn[]) => void
  /**
   * Names are fixed: a partition key's name is the `key=` of its folders, so
   * only its type is the reader's to change.
   */
  fixedNames?: boolean
}

/**
 * The inferred columns as editable rows — a name and a Hive type each, with
 * the common types suggested — plus add and remove. Order is the table's
 * column order.
 */
export function ColumnsEditor({ label, columns, onChange, fixedNames }: ColumnsEditorProps) {
  const typesId = useId()
  const update = (id: number, patch: Partial<DraftColumn>) =>
    onChange(columns.map((c) => (c.id === id ? { ...c, ...patch } : c)))

  return (
    <div role="group" aria-label={label} className="flex flex-col gap-1.5">
      <datalist id={typesId}>
        {HIVE_TYPES.map((t) => (
          <option key={t} value={t} />
        ))}
      </datalist>
      <div className="grid grid-cols-[1fr_1fr_1.75rem] gap-x-2 px-0.5">
        <FieldLabel>Name</FieldLabel>
        <FieldLabel>Type</FieldLabel>
      </div>
      {columns.map((column, i) => (
        <div key={column.id} className="grid grid-cols-[1fr_1fr_1.75rem] items-center gap-x-2">
          <Input
            aria-label={`${label} ${i + 1} name`}
            // A fixed name reads as a fact, not a field waiting for input.
            className={cn("h-8 font-mono text-xs", fixedNames && "bg-bg text-fg-muted")}
            value={column.name}
            readOnly={fixedNames}
            onChange={(e) => update(column.id, { name: e.target.value })}
          />
          <Input
            aria-label={`${label} ${i + 1} type`}
            className="h-8 font-mono text-xs"
            list={typesId}
            value={column.type}
            onChange={(e) => update(column.id, { type: e.target.value })}
          />
          {fixedNames ? (
            <span />
          ) : (
            <RowAction
              type="button"
              label={`Remove ${column.name || `column ${i + 1}`}`}
              tone="danger"
              onClick={() => onChange(columns.filter((c) => c.id !== column.id))}
            >
              <X className="h-3.5 w-3.5" />
            </RowAction>
          )}
        </div>
      ))}
      {!fixedNames && (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="w-fit"
          onClick={() => onChange([...columns, draftColumn("", "string")])}
        >
          <Plus className="h-3.5 w-3.5" />
          Add column
        </Button>
      )}
    </div>
  )
}
