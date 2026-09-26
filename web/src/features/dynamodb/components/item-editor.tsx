import { useState, useCallback } from "react"
import { Plus, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { JsonEditor } from "@/components/ui/json-editor"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Spinner } from "@/components/ui/primitives"
import type { DynamoItem, DynamoAttrValue } from "@/types"
import { createId } from "@/lib/id"
import { SegmentedControl, type SegmentedOption } from "@/components/ui/segmented-control"
import { isRecord } from "@/lib/utils"

type AttrType = "S" | "N" | "BOOL" | "NULL" | "SS" | "NS" | "L" | "M"
type InputMode = "form" | "json"
type JsonFormat = "unmarshalled" | "marshalled"

const INPUT_MODES = [
  { value: "form", label: "Form" },
  { value: "json", label: "JSON" },
] as const satisfies readonly SegmentedOption<InputMode>[]

const JSON_FORMATS = [
  { value: "unmarshalled", label: "Unmarshalled" },
  { value: "marshalled", label: "Marshalled" },
] as const satisfies readonly SegmentedOption<JsonFormat>[]

interface AttrRow {
  id: string
  name: string
  type: AttrType
  value: string
}

function makeRow(): AttrRow {
  return { id: createId(), name: "", type: "S", value: "" }
}

function buildAttrValue(type: AttrType, value: string): DynamoAttrValue {
  switch (type) {
    case "S":
      return { S: value }
    case "N":
      return { N: value }
    case "BOOL":
      return { BOOL: value === "true" }
    case "NULL":
      return { NULL: true }
    case "SS":
      return {
        SS: value
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
      }
    case "NS":
      return {
        NS: value
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
      }
    case "L":
    case "M":
      try {
        return JSON.parse(value) as DynamoAttrValue
      } catch {
        return { S: value }
      }
    default:
      return { S: value }
  }
}

// Convert a plain JSON value to DynamoDB attribute JSON.
function marshalValue(v: unknown): DynamoAttrValue {
  if (v === null) return { NULL: true }
  if (typeof v === "boolean") return { BOOL: v }
  if (typeof v === "number") return { N: String(v) }
  if (typeof v === "string") return { S: v }
  if (Array.isArray(v)) return { L: v.map(marshalValue) }
  if (isRecord(v)) {
    const m: Record<string, DynamoAttrValue> = {}
    for (const [k, val] of Object.entries(v)) {
      m[k] = marshalValue(val)
    }
    return { M: m }
  }
  return { S: String(v) }
}

function marshalItem(plain: Record<string, unknown>): DynamoItem {
  const item: DynamoItem = {}
  for (const [k, v] of Object.entries(plain)) {
    item[k] = marshalValue(v)
  }
  return item
}

// Snapshot the current rows as JSON text for the given format.
function rowsToJsonText(rows: AttrRow[], format: JsonFormat): string {
  if (format === "marshalled") {
    const item: DynamoItem = {}
    for (const row of rows) {
      if (!row.name.trim()) continue
      item[row.name.trim()] = buildAttrValue(row.type, row.value)
    }
    return JSON.stringify(item, null, 2)
  }
  // Unmarshalled: best-effort plain representation
  const plain: Record<string, unknown> = {}
  for (const row of rows) {
    if (!row.name.trim()) continue
    switch (row.type) {
      case "S":
        plain[row.name] = row.value
        break
      case "N":
        plain[row.name] = row.value === "" ? 0 : Number(row.value)
        break
      case "BOOL":
        plain[row.name] = row.value === "true"
        break
      case "NULL":
        plain[row.name] = null
        break
      case "SS":
      case "NS":
        plain[row.name] = row.value
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean)
        break
      case "L":
      case "M":
        try {
          plain[row.name] = JSON.parse(row.value)
        } catch {
          plain[row.name] = row.value
        }
        break
      default:
        plain[row.name] = row.value
    }
  }
  return JSON.stringify(plain, null, 2)
}

// Parse marshalled DynamoDB JSON back into rows (best-effort).
function marshalledToRows(item: DynamoItem): AttrRow[] {
  return Object.entries(item).map(([name, attr]) => {
    const row = { ...makeRow(), name }
    if (!attr) return { ...row, type: "S", value: "" }
    if ("S" in attr) return { ...row, type: "S", value: String(attr.S) }
    if ("N" in attr) return { ...row, type: "N", value: String(attr.N) }
    if ("BOOL" in attr) return { ...row, type: "BOOL", value: String(attr.BOOL) }
    if ("NULL" in attr) return { ...row, type: "NULL", value: "" }
    if ("SS" in attr)
      return {
        ...row,
        type: "SS",
        value: attr.SS.join(", "),
      }
    if ("NS" in attr)
      return {
        ...row,
        type: "NS",
        value: attr.NS.join(", "),
      }
    if ("L" in attr) return { ...row, type: "L", value: JSON.stringify(attr.L) }
    if ("M" in attr) return { ...row, type: "M", value: JSON.stringify(attr.M) }
    return { ...row, type: "S", value: JSON.stringify(attr) }
  })
}

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  requiredKeys?: { name: string; type: "S" | "N" | "B" }[]
  isPending?: boolean
  onSubmit: (item: DynamoItem) => void
  /** When provided the dialog opens in "Edit" mode pre-populated with this item. */
  initialItem?: DynamoItem
}

function defaultRows(requiredKeys: Props["requiredKeys"], initialItem?: DynamoItem): AttrRow[] {
  if (initialItem) {
    return marshalledToRows(initialItem)
  }
  if (requiredKeys && requiredKeys.length > 0) {
    return requiredKeys.map((k) => ({ ...makeRow(), name: k.name, type: k.type as AttrType }))
  }
  return [makeRow()]
}

export function ItemEditorDialog({
  open,
  onOpenChange,
  requiredKeys = [],
  isPending,
  onSubmit,
  initialItem,
}: Props) {
  const isEditMode = !!initialItem
  const [inputMode, setInputMode] = useState<InputMode>("form")
  const [jsonFormat, setJsonFormat] = useState<JsonFormat>("unmarshalled")
  const [jsonText, setJsonText] = useState("")
  const [jsonError, setJsonError] = useState<string | null>(null)
  const [rows, setRows] = useState<AttrRow[]>(() => defaultRows(requiredKeys, initialItem))

  // Re-populate when a different item is opened for editing.
  //
  // This is React's "adjusting state when a prop changes" — done during render,
  // guarded by the props it is a function of, rather than from an effect. An
  // effect would paint the previous item's attributes first and only then
  // replace them, which is both a wasted render and a visible flash of the
  // wrong item; React instead discards this render and re-runs with the new
  // state before touching the DOM. `requiredKeys` is deliberately not part of
  // the guard: it is static per table, and reading it here is not a trigger.
  const [seededFor, setSeededFor] = useState<{ open: boolean; item: DynamoItem | undefined }>({
    open,
    item: initialItem,
  })
  if (seededFor.open !== open || seededFor.item !== initialItem) {
    setSeededFor({ open, item: initialItem })
    if (open) {
      setRows(defaultRows(requiredKeys, initialItem))
      setInputMode("form")
      setJsonText("")
      setJsonError(null)
    }
  }

  // Live validation: parse on every keystroke
  const handleJsonChange = useCallback((value: string) => {
    setJsonText(value)
    if (value.trim() === "") {
      setJsonError(null)
      return
    }
    try {
      const parsed = JSON.parse(value)
      if (!isRecord(parsed)) {
        setJsonError("Must be a JSON object { … }")
      } else {
        setJsonError(null)
      }
    } catch (e) {
      setJsonError(e instanceof SyntaxError ? e.message : "Invalid JSON")
    }
  }, [])

  function resetState() {
    setInputMode("form")
    setJsonFormat("unmarshalled")
    setJsonText("")
    setJsonError(null)
    setRows(defaultRows(requiredKeys, initialItem))
  }

  function handleOpenChange(next: boolean) {
    if (!next) resetState()
    onOpenChange(next)
  }

  function switchToJson(format: JsonFormat) {
    setJsonText(rowsToJsonText(rows, format))
    setJsonError(null)
    setJsonFormat(format)
    setInputMode("json")
  }

  function switchToForm() {
    setJsonError(null)
    if (jsonText.trim() === "") {
      setInputMode("form")
      return
    }
    try {
      const parsed = JSON.parse(jsonText) as Record<string, unknown>
      let newRows: AttrRow[]
      if (jsonFormat === "marshalled") {
        newRows = marshalledToRows(parsed as DynamoItem)
      } else {
        newRows = marshalledToRows(marshalItem(parsed))
      }
      // Re-apply required key rows on top
      const requiredNames = new Set(requiredKeys.map((k) => k.name))
      const requiredRows = requiredKeys.map((k) => {
        const existing = newRows.find((r) => r.name === k.name)
        return existing ?? { ...makeRow(), name: k.name, type: k.type as AttrType }
      })
      const otherRows = newRows.filter((r) => !requiredNames.has(r.name))
      setRows([...requiredRows, ...otherRows])
      setInputMode("form")
    } catch {
      setJsonError("Invalid JSON — fix errors before switching to form view.")
    }
  }

  function changeJsonFormat(format: JsonFormat) {
    // Re-serialise in the new format if possible, otherwise just update the label.
    if (jsonText.trim() !== "") {
      try {
        const parsed = JSON.parse(jsonText) as Record<string, unknown>
        if (jsonFormat === "marshalled" && format === "unmarshalled") {
          // DynamoDB JSON → plain: best-effort by extracting scalar values
          const plain: Record<string, unknown> = {}
          for (const [k, attr] of Object.entries(parsed as DynamoItem)) {
            const a = attr as Record<string, unknown>
            if ("S" in a) plain[k] = a.S
            else if ("N" in a) plain[k] = Number(a.N)
            else if ("BOOL" in a) plain[k] = a.BOOL
            else if ("NULL" in a) plain[k] = null
            else plain[k] = attr
          }
          setJsonText(JSON.stringify(plain, null, 2))
        } else if (jsonFormat === "unmarshalled" && format === "marshalled") {
          setJsonText(JSON.stringify(marshalItem(parsed), null, 2))
        }
        setJsonError(null)
      } catch {
        // leave text as-is, just change the format label
      }
    }
    setJsonFormat(format)
  }

  function handleSubmit() {
    if (inputMode === "json") {
      let item: DynamoItem
      try {
        const parsed = JSON.parse(jsonText) as Record<string, unknown>
        item = jsonFormat === "marshalled" ? (parsed as DynamoItem) : marshalItem(parsed)
      } catch {
        setJsonError("Invalid JSON — cannot submit.")
        return
      }
      onSubmit(item)
      resetState()
    } else {
      const item: DynamoItem = {}
      for (const row of rows) {
        if (!row.name.trim()) continue
        item[row.name.trim()] = buildAttrValue(row.type, row.value)
      }
      onSubmit(item)
      resetState()
    }
  }

  const addRow = () => setRows((prev) => [...prev, makeRow()])
  const removeRow = (id: string) => setRows((prev) => prev.filter((r) => r.id !== id))
  const updateRow = (id: string, patch: Partial<AttrRow>) =>
    setRows((prev) => prev.map((r) => (r.id === id ? { ...r, ...patch } : r)))

  // In edit mode, all key fields (from requiredKeys) + the item's own keys are locked.
  const requiredKeyNames = new Set([
    ...requiredKeys.map((k) => k.name),
    ...(isEditMode ? Object.keys(initialItem) : []),
  ])
  const canSubmit =
    inputMode === "json" ? jsonText.trim().length > 0 : rows.some((r) => r.name.trim().length > 0)

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{isEditMode ? "Edit Item" : "Put Item"}</DialogTitle>
        </DialogHeader>

        {/* Mode toggle */}
        <SegmentedControl
          label="Input mode"
          value={inputMode}
          options={INPUT_MODES}
          onChange={(mode) => (mode === "form" ? switchToForm() : switchToJson(jsonFormat))}
          size="md"
          className="self-start"
        />

        {inputMode === "form" ? (
          <div className="flex flex-col gap-2 py-1">
            <div className="grid grid-cols-[1fr_7rem_1fr_auto] gap-2 px-0.5 text-xs font-medium text-fg-muted">
              <span>Attribute name</span>
              <span>Type</span>
              <span>Value</span>
              <span />
            </div>

            {rows.map((row) => {
              const isRequired = requiredKeyNames.has(row.name)
              return (
                <div key={row.id} className="grid grid-cols-[1fr_7rem_1fr_auto] items-center gap-2">
                  <Input
                    value={row.name}
                    onChange={(e) => updateRow(row.id, { name: e.target.value })}
                    placeholder="name"
                    disabled={isRequired}
                    className="h-7 font-mono text-xs"
                  />
                  <select
                    value={row.type}
                    onChange={(e) => updateRow(row.id, { type: e.target.value as AttrType })}
                    disabled={isRequired}
                    className="h-7 rounded-md border border-border bg-bg px-1.5 text-xs text-fg disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    <option value="S">String (S)</option>
                    <option value="N">Number (N)</option>
                    <option value="BOOL">Boolean</option>
                    <option value="NULL">Null</option>
                    <option value="SS">String Set</option>
                    <option value="NS">Number Set</option>
                    <option value="L">List (JSON)</option>
                    <option value="M">Map (JSON)</option>
                  </select>
                  {row.type === "BOOL" ? (
                    <select
                      value={row.value}
                      onChange={(e) => updateRow(row.id, { value: e.target.value })}
                      className="h-7 rounded-md border border-border bg-bg px-1.5 text-xs text-fg"
                    >
                      <option value="true">true</option>
                      <option value="false">false</option>
                    </select>
                  ) : row.type === "NULL" ? (
                    <Input disabled value="null" className="h-7 text-xs text-fg-muted" />
                  ) : (
                    <Input
                      value={row.value}
                      onChange={(e) => updateRow(row.id, { value: e.target.value })}
                      placeholder={
                        row.type === "SS" || row.type === "NS"
                          ? "a, b, c"
                          : row.type === "L"
                            ? '["a","b"]'
                            : row.type === "M"
                              ? '{"k":"v"}'
                              : "value"
                      }
                      className="h-7 font-mono text-xs"
                    />
                  )}
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    className="text-fg-muted hover:text-danger"
                    disabled={isRequired || rows.length <= 1}
                    onClick={() => removeRow(row.id)}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              )
            })}

            <Button size="sm" variant="ghost" className="mt-1 w-fit text-xs" onClick={addRow}>
              <Plus className="mr-1 h-3.5 w-3.5" />
              Add attribute
            </Button>
          </div>
        ) : (
          <div className="flex flex-col gap-2 py-1">
            {/* Format toggle */}
            <div className="flex items-center gap-2">
              <span className="font-mono text-xs text-fg-muted">Format:</span>
              <SegmentedControl
                label="JSON format"
                value={jsonFormat}
                options={JSON_FORMATS}
                onChange={changeJsonFormat}
              />
              <span className="text-xs text-fg-muted">
                {jsonFormat === "unmarshalled"
                  ? '— plain JSON, e.g. {"pk": "hello", "count": 42}'
                  : '— DynamoDB JSON, e.g. {"pk": {"S": "hello"}}'}
              </span>
            </div>

            <JsonEditor
              value={jsonText}
              onChange={handleJsonChange}
              error={jsonError}
              placeholder={
                jsonFormat === "unmarshalled"
                  ? '{\n  "pk": "hello",\n  "count": 42,\n  "active": true\n}'
                  : '{\n  "pk": {"S": "hello"},\n  "count": {"N": "42"}\n}'
              }
            />
          </div>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={() => handleOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={handleSubmit} disabled={!canSubmit || isPending}>
            {isPending ? <Spinner className="mr-1.5" /> : null}
            {isEditMode ? "Save" : "Put Item"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
