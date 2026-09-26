import { useState } from "react"
import { fieldLabel } from "@/lib/typography"
import { cn, isRecord } from "@/lib/utils"
import { useQuery } from "@tanstack/react-query"
import { ArnLink } from "@/components/ui/arn-link"
import { ResourceArnCombobox } from "@/components/ui/resource-arn-combobox"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Spinner } from "@/components/ui/primitives"
import {
  lambdaKeys,
  esmsQueryOptions,
  createEsmMutationOptions,
  deleteEsmMutationOptions,
} from "@/features/lambda/data"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { Plus as PlusIcon } from "lucide-react"
import type { EventSourceMapping } from "@/types"
import type { EventSourcePosition } from "@aws-sdk/client-lambda"

// ─── Filter humanization ─────────────────────────────────────────────────────

/**
 * Convert a single raw filter rule value into a readable string.
 * Handles all operators documented in the AWS Lambda filter spec.
 */
function humanizeRule(rule: unknown): string {
  if (rule === null) return "null"
  if (typeof rule === "string") return rule === "" ? '""' : rule
  if (typeof rule === "number" || typeof rule === "boolean") return String(rule)
  if (isRecord(rule)) {
    if ("prefix" in rule) return `starts with "${String(rule.prefix)}"`
    if ("suffix" in rule) return `ends with "${String(rule.suffix)}"`
    if ("equals-ignore-case" in rule) return `≈ "${String(rule["equals-ignore-case"])}"`
    if ("exists" in rule) return rule.exists ? "exists" : "not exists"
    if ("anything-but" in rule) {
      const ab = rule["anything-but"]
      if (Array.isArray(ab)) return `≠ ${ab.map(String).join(" | ")}`
      return `≠ "${String(ab)}"`
    }
    if ("numeric" in rule) {
      const ops = rule.numeric as unknown[]
      if (ops.length === 2) return `${String(ops[0])} ${String(ops[1])}`
      if (ops.length >= 4)
        return `${String(ops[0])} ${String(ops[1])} and ${String(ops[2])} ${String(ops[3])}`
    }
  }
  return JSON.stringify(rule)
}

/**
 * Convert a filter Pattern JSON string into a compact human-readable summary.
 * Examples:
 *   '{"eventName":["INSERT"]}'             → "eventName: INSERT"
 *   '{"eventName":["INSERT","MODIFY"]}'    → "eventName: INSERT | MODIFY"
 *   '{"awsRegion":[{"prefix":"us-"}]}'    → "awsRegion: starts with "us-""
 *   '{"a":["x"],"b":["y"]}'               → "a: x, b: y"
 */
function humanizePattern(patternJson: string): string {
  if (!patternJson) return ""
  try {
    const obj = JSON.parse(patternJson) as Record<string, unknown>
    const parts = Object.entries(obj).map(([key, val]) => {
      if (key === "$or") return "$or: …"
      if (Array.isArray(val)) {
        const rules = val.map(humanizeRule)
        return `${key}: ${rules.join(" | ")}`
      }
      // Nested object — just show the key with a drill-down indicator.
      return `${key}: {…}`
    })
    return parts.join(", ")
  } catch {
    return patternJson
  }
}

/**
 * Produce a display string for a full FilterCriteria object.
 * Returns null when no filters are configured.
 */
function describeFilterCriteria(
  fc: { Filters?: { Pattern?: string }[] } | undefined,
): string | null {
  if (!fc?.Filters?.length) return null
  if (fc.Filters.length === 1) {
    return humanizePattern(fc.Filters[0].Pattern ?? "")
  }
  // Multiple filters are OR'd — label them individually.
  return fc.Filters.map((f, i) => `#${i + 1}: ${humanizePattern(f.Pattern ?? "")}`).join(" OR ")
}

/**
 * Raw JSON string for the title tooltip so users can see the exact pattern.
 */
function rawFilterJson(fc: { Filters?: { Pattern?: string }[] } | undefined): string {
  if (!fc?.Filters?.length) return ""
  return fc.Filters.map((f) => f.Pattern ?? "").join("\n")
}

export function TriggersTab({ name }: { name: string }) {
  const { data: esms = [], isLoading } = useQuery(esmsQueryOptions(name))

  const createMut = useResourceMutation({
    options: createEsmMutationOptions(),
    invalidateKeys: [lambdaKeys.esms(name)],
    successTitle: "Trigger added",
    errorTitle: "Failed to add trigger",
    onSuccess: () => resetForm(),
  })

  const deleteMut = useResourceMutation({
    options: deleteEsmMutationOptions(),
    invalidateKeys: [lambdaKeys.esms(name)],
    successTitle: "Trigger removed",
    errorTitle: "Failed to remove trigger",
  })

  const [showAdd, setShowAdd] = useState(false)
  const [newSourceArn, setNewSourceArn] = useState("")
  const [newBatchSize, setNewBatchSize] = useState("10")
  const [newBatchingWindow, setNewBatchingWindow] = useState("0")
  const [newStartingPosition, setNewStartingPosition] = useState("TRIM_HORIZON")
  const [newFilterPattern, setNewFilterPattern] = useState("")
  const [newMaxRecordAge, setNewMaxRecordAge] = useState("")
  const [newMaxRetryAttempts, setNewMaxRetryAttempts] = useState("")
  const [newTumblingWindow, setNewTumblingWindow] = useState("0")
  const [newBisect, setNewBisect] = useState(false)
  const [newMaxConcurrency, setNewMaxConcurrency] = useState("")

  const isDynamoDBStream =
    newSourceArn.toLowerCase().includes(":dynamodb:") &&
    newSourceArn.toLowerCase().includes("/stream/")

  const handleAdd = () => {
    if (!newSourceArn.trim()) return
    const batchingWindow = parseInt(newBatchingWindow, 10)
    const filterCriteria = newFilterPattern.trim()
      ? { Filters: [{ Pattern: newFilterPattern.trim() }] }
      : undefined
    const concurrency =
      !isDynamoDBStream && newMaxConcurrency !== "" ? parseInt(newMaxConcurrency, 10) : undefined
    createMut.mutate({
      FunctionName: name,
      EventSourceArn: newSourceArn.trim(),
      BatchSize: parseInt(newBatchSize, 10) || 10,
      Enabled: true,
      ...(isDynamoDBStream ? { StartingPosition: newStartingPosition as EventSourcePosition } : {}),
      ...(batchingWindow > 0 ? { MaximumBatchingWindowInSeconds: batchingWindow } : {}),
      ...(filterCriteria ? { FilterCriteria: filterCriteria } : {}),
      ...(isDynamoDBStream && newMaxRecordAge !== ""
        ? { MaximumRecordAgeInSeconds: parseInt(newMaxRecordAge, 10) }
        : {}),
      ...(isDynamoDBStream && newMaxRetryAttempts !== ""
        ? { MaximumRetryAttempts: parseInt(newMaxRetryAttempts, 10) }
        : {}),
      ...(isDynamoDBStream && parseInt(newTumblingWindow, 10) > 0
        ? { TumblingWindowInSeconds: parseInt(newTumblingWindow, 10) }
        : {}),
      ...(isDynamoDBStream && newBisect ? { BisectBatchOnFunctionError: true } : {}),
      ...(concurrency != null && concurrency > 0
        ? { ScalingConfig: { MaximumConcurrency: concurrency } }
        : {}),
    })
  }

  const resetForm = () => {
    setShowAdd(false)
    setNewSourceArn("")
    setNewBatchSize("10")
    setNewBatchingWindow("0")
    setNewStartingPosition("TRIM_HORIZON")
    setNewFilterPattern("")
    setNewMaxRecordAge("")
    setNewMaxRetryAttempts("")
    setNewTumblingWindow("0")
    setNewBisect(false)
    setNewMaxConcurrency("")
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h3 className="font-mono text-sm font-medium text-fg">Event source mappings</h3>
        <Button size="sm" variant="ghost" onClick={() => setShowAdd((v) => !v)}>
          <PlusIcon className="mr-1 h-3.5 w-3.5" />
          Add trigger
        </Button>
      </div>

      {showAdd && (
        <div className="flex max-w-2xl flex-col gap-3 rounded-md border border-border bg-bg-elevated p-4">
          <div className="flex flex-col gap-1">
            <label className={cn(fieldLabel, "text-fg-muted")}>Event source ARN</label>
            <ResourceArnCombobox
              resourceType="esm-source"
              value={newSourceArn}
              onChange={setNewSourceArn}
            />
          </div>

          {/* Row: batch size + batching window + starting position (streams only) */}
          <div className="flex flex-wrap gap-4">
            <div className="flex flex-col gap-1">
              <label className={cn(fieldLabel, "text-fg-muted")}>Batch size</label>
              <input
                type="number"
                min={1}
                max={10000}
                className="w-24 rounded-md border border-border bg-bg px-3 py-1.5 text-xs text-fg focus:ring-1 focus:ring-accent focus:outline-none"
                value={newBatchSize}
                onChange={(e) => setNewBatchSize(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-1">
              <label className={cn(fieldLabel, "text-fg-muted")}>Batching window (s)</label>
              <input
                type="number"
                min={0}
                max={300}
                className="w-24 rounded-md border border-border bg-bg px-3 py-1.5 text-xs text-fg focus:ring-1 focus:ring-accent focus:outline-none"
                value={newBatchingWindow}
                onChange={(e) => setNewBatchingWindow(e.target.value)}
              />
            </div>
            {isDynamoDBStream && (
              <div className="flex flex-col gap-1">
                <label className={cn(fieldLabel, "text-fg-muted")}>Starting position</label>
                <select
                  className="rounded-md border border-border bg-bg px-3 py-1.5 text-xs text-fg focus:ring-1 focus:ring-accent focus:outline-none"
                  value={newStartingPosition}
                  onChange={(e) => setNewStartingPosition(e.target.value)}
                >
                  <option value="TRIM_HORIZON">TRIM_HORIZON</option>
                  <option value="LATEST">LATEST</option>
                </select>
              </div>
            )}
            {/* Max concurrency — SQS sources only */}
            {!isDynamoDBStream && (
              <div className="flex flex-col gap-1">
                <label className={cn(fieldLabel, "text-fg-muted")}>
                  Max concurrency <span className="font-normal text-fg-muted/70">(optional)</span>
                </label>
                <input
                  type="number"
                  min={2}
                  max={1000}
                  placeholder="unlimited"
                  className="w-28 rounded-md border border-border bg-bg px-3 py-1.5 text-xs text-fg focus:ring-1 focus:ring-accent focus:outline-none"
                  value={newMaxConcurrency}
                  onChange={(e) => setNewMaxConcurrency(e.target.value)}
                />
              </div>
            )}
          </div>

          {/* Filter pattern — both source types */}
          <div className="flex flex-col gap-1">
            <label className={cn(fieldLabel, "text-fg-muted")}>
              Filter pattern <span className="font-normal text-fg-muted/70">(optional)</span>
            </label>
            <input
              type="text"
              placeholder='{"key": ["value"]}'
              className="rounded-md border border-border bg-bg px-3 py-1.5 text-xs text-fg focus:ring-1 focus:ring-accent focus:outline-none"
              value={newFilterPattern}
              onChange={(e) => setNewFilterPattern(e.target.value)}
            />
          </div>

          {/* DynamoDB Streams-only fields */}
          {isDynamoDBStream && (
            <>
              <div className="flex flex-wrap gap-4">
                <div className="flex flex-col gap-1">
                  <label className={cn(fieldLabel, "text-fg-muted")}>
                    Max record age (s){" "}
                    <span className="font-normal text-fg-muted/70">(-1 = unlimited)</span>
                  </label>
                  <input
                    type="number"
                    min={-1}
                    max={604800}
                    placeholder="-1"
                    className="w-32 rounded-md border border-border bg-bg px-3 py-1.5 text-xs text-fg focus:ring-1 focus:ring-accent focus:outline-none"
                    value={newMaxRecordAge}
                    onChange={(e) => setNewMaxRecordAge(e.target.value)}
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <label className={cn(fieldLabel, "text-fg-muted")}>
                    Max retry attempts{" "}
                    <span className="font-normal text-fg-muted/70">(-1 = unlimited)</span>
                  </label>
                  <input
                    type="number"
                    min={-1}
                    max={10000}
                    placeholder="-1"
                    className="w-32 rounded-md border border-border bg-bg px-3 py-1.5 text-xs text-fg focus:ring-1 focus:ring-accent focus:outline-none"
                    value={newMaxRetryAttempts}
                    onChange={(e) => setNewMaxRetryAttempts(e.target.value)}
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <label className={cn(fieldLabel, "text-fg-muted")}>Tumbling window (s)</label>
                  <input
                    type="number"
                    min={0}
                    max={900}
                    className="w-28 rounded-md border border-border bg-bg px-3 py-1.5 text-xs text-fg focus:ring-1 focus:ring-accent focus:outline-none"
                    value={newTumblingWindow}
                    onChange={(e) => setNewTumblingWindow(e.target.value)}
                  />
                </div>
              </div>
              <label className="flex items-center gap-2 text-xs text-fg">
                <input
                  type="checkbox"
                  className="accent-accent"
                  checked={newBisect}
                  onChange={(e) => setNewBisect(e.target.checked)}
                />
                Bisect batch on function error
              </label>
            </>
          )}

          <div className="flex gap-2">
            <Button
              size="sm"
              disabled={!newSourceArn.trim() || createMut.isPending}
              onClick={handleAdd}
            >
              {createMut.isPending ? <Spinner className="mr-1 h-3.5 w-3.5" /> : null}
              Add
            </Button>
            <Button size="sm" variant="ghost" onClick={resetForm}>
              Cancel
            </Button>
          </div>
        </div>
      )}

      {isLoading ? (
        <div className="flex items-center justify-center py-16">
          <Spinner className="h-5 w-5" />
        </div>
      ) : esms.length === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-md border border-border bg-bg-elevated py-16 text-center">
          <p className="text-sm text-fg-muted">No triggers configured.</p>
          <p className="text-xs text-fg-muted">Add an SQS queue or DynamoDB stream as a trigger.</p>
        </div>
      ) : (
        // Seven columns of mono, one of them an ARN: let the table scroll
        // rather than push the tab panel wide.
        <div className="w-full overflow-x-auto">
          <table className="w-full font-mono text-xs">
            <thead>
              <tr className={cn(fieldLabel, "border-b border-border text-left text-fg-subtle")}>
                <th className="pr-4 pb-2">Source</th>
                <th className="pr-4 pb-2">Batch size</th>
                <th className="pr-4 pb-2">Concurrency</th>
                <th className="pr-4 pb-2">Filter</th>
                <th className="pr-4 pb-2">State</th>
                <th className="pr-4 pb-2">Last result</th>
                <th className="pb-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {esms.map((esm: EventSourceMapping) => {
                const filterDesc = describeFilterCriteria(esm.FilterCriteria)
                const filterRaw = rawFilterJson(esm.FilterCriteria)
                return (
                  <tr key={esm.UUID}>
                    <td className="py-2 pr-4 font-mono text-xs text-fg">
                      <ArnLink arn={esm.EventSourceArn ?? ""} />
                    </td>
                    <td className="py-2 pr-4 text-fg">{esm.BatchSize}</td>
                    <td className="py-2 pr-4 text-fg">
                      {esm.ScalingConfig?.MaximumConcurrency ? (
                        esm.ScalingConfig.MaximumConcurrency
                      ) : (
                        <span className="text-fg-muted">—</span>
                      )}
                    </td>
                    <td className="max-w-[220px] py-2 pr-4">
                      {filterDesc ? (
                        <span
                          className="block truncate font-mono text-xs text-fg-muted"
                          title={filterRaw}
                        >
                          {filterDesc}
                        </span>
                      ) : (
                        <span className="text-fg-muted">—</span>
                      )}
                    </td>
                    <td className="py-2 pr-4">
                      <Badge variant={esm.State === "Enabled" ? "success" : "default"}>
                        {esm.State}
                      </Badge>
                    </td>
                    <td className="py-2 pr-4">
                      {esm.LastProcessingResult === "Throttled" ? (
                        <Badge variant="warning">Throttled</Badge>
                      ) : (
                        <span className="text-xs text-fg-muted">
                          {esm.LastProcessingResult ?? "—"}
                        </span>
                      )}
                    </td>
                    <td className="py-2 text-right">
                      <button
                        className="text-xs text-fg-muted hover:text-danger"
                        disabled={deleteMut.isPending}
                        onClick={() => deleteMut.mutate(esm.UUID ?? "")}
                        title="Remove trigger"
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
