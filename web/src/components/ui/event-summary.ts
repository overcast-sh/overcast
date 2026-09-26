import type { StreamEvent } from "@/hooks/use-event-stream"
import { formatQuantity } from "@/lib/format"

/** Default payload summary: key fields for known sources, truncated JSON otherwise. */
export function defaultEventSummary(event: StreamEvent): string {
  const p = event.payload as Record<string, unknown> | null
  if (!p) return ""

  if (event.type === "service:Error") {
    const service = String(p.service ?? event.source)
    const op = p.operation ? String(p.operation) + ": " : ""
    const msg = String(p.message ?? "")
    return `${service}: ${op}${msg}`
  }

  if (event.source === "request") {
    const method = String(p.method ?? "")
    const path = String(p.path ?? "")
    const status = p.status != null ? String(p.status) : ""
    const dur = p.durationUs != null ? formatDuration(Number(p.durationUs)) : ""
    return `${method} ${path} ${status} ${dur}`.trim()
  }

  if (event.source === "s3") {
    const bucket = String(p.Bucket ?? p.bucket ?? p.name ?? p.Name ?? "")
    const key = String(p.Key ?? p.key ?? "")
    const size = p.Size != null ? ` (${formatBytes(Number(p.Size))})` : ""
    if (!bucket) return JSON.stringify(p)
    return key ? `s3://${bucket}/${key}${size}` : `s3://${bucket}${size}`
  }

  // ESM delivery events — produced by the emulator's esmDeliveryManager.
  if (event.type === "lambda:ESMInvoked") {
    const fn = String(p.functionName ?? "")
    const src = String(p.eventSource ?? "")
    const count = Number(p.recordCount ?? 1)
    const name = String(p.eventName ?? "")
    const nameStr = name ? ` [${name}]` : ""
    return `${src} → ${fn}${nameStr} · ${formatQuantity(count, "record")}`
  }

  if (event.type === "lambda:ESMRecordFiltered") {
    const fn = String(p.functionName ?? "")
    const src = String(p.eventSource ?? "")
    const count = Number(p.recordCount ?? 1)
    const patterns = p.filterPatterns as string[] | undefined
    const name = String(p.eventName ?? "")
    const nameStr = name ? ` [${name}]` : ""
    const reasonStr =
      patterns && patterns.length > 0
        ? ` · no match: ${patterns[0].length > 60 ? patterns[0].slice(0, 60) + "…" : patterns[0]}`
        : ""
    return `${src} → ${fn}${nameStr} filtered ${formatQuantity(count, "record")}${reasonStr}`
  }

  if (event.type === "lambda:ESMRecordMatched") {
    const fn = String(p.functionName ?? "")
    const src = String(p.eventSource ?? "")
    const name = String(p.eventName ?? "")
    const nameStr = name ? ` [${name}]` : ""
    return `${src} → ${fn}${nameStr} matched filter`
  }

  if (event.type === "lambda:ImagePulling") {
    const image = String(p.image ?? "")
    return `Pulling image: ${image}`
  }

  if (event.type === "lambda:ImagePullComplete") {
    const image = String(p.image ?? "")
    const errMsg = p.error ? String(p.error) : ""
    const elapsedMs = Number(p.elapsedMs ?? 0)
    const elapsed = elapsedMs >= 1000 ? `${(elapsedMs / 1000).toFixed(1)}s` : `${elapsedMs}ms`
    if (errMsg) return `Image pull failed: ${image} — ${errMsg}`
    return `Image ready: ${image} (${elapsed})`
  }

  if (event.type === "dynamodb:StreamRecord") {
    const table = String(p.table ?? "")
    const name = String(p.eventName ?? "")
    const ddb = p.dynamodb as Record<string, unknown> | undefined
    const image = (ddb?.NewImage ?? ddb?.OldImage ?? ddb?.Keys) as
      | Record<string, unknown>
      | undefined
    const firstKey = image ? Object.keys(image)[0] : undefined
    const firstVal = firstKey
      ? ((image![firstKey] as Record<string, string> | undefined)?.S ??
        (image![firstKey] as Record<string, string> | undefined)?.N)
      : undefined
    const hint = firstKey && firstVal ? ` · ${firstKey}=${firstVal}` : ""
    return `${table} ${name}${hint}`
  }

  const raw = JSON.stringify(p)
  return raw.length > 120 ? raw.slice(0, 120) + "…" : raw
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

/** Format microsecond duration for human readability. */
function formatDuration(us: number): string {
  if (us < 1000) return `${us}µs`
  if (us < 1_000_000) return `${(us / 1000).toFixed(1)}ms`
  return `${(us / 1_000_000).toFixed(2)}s`
}
