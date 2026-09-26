/**
 * The stream viewer's download control: exports the CURRENTLY LOADED events —
 * post-clear, post-merge, in the displayed sort order — as CSV (the console's
 * search-results shape) or JSON. Honest about its scope: when more events
 * remain on the server, the tooltip says so rather than implying a
 * full-history export.
 */
import { useEffect, useRef, useState } from "react"
import { Download } from "lucide-react"
import { Button } from "@/components/ui/button"
import type { FilteredLogEvent } from "@/types/logs"
import { buildLogEventsCsv, buildLogEventsJson } from "../export-log-events"
import { formatQuantity } from "@/lib/format"

interface Props {
  /** Exactly what the list holds, in the displayed order. */
  events: readonly FilteredLogEvent[]
  /** True while the server still has events the list has not loaded. */
  hasMore: boolean
  /** Stream or group name, for the download's filename. */
  baseName: string
}

/** Filesystem-safe spelling of a log group or stream name. */
function fileStem(name: string): string {
  return name.replaceAll(/[^\w.-]+/g, "-").replaceAll(/^-+|-+$/g, "") || "log-events"
}

function download(filename: string, content: string, mimeType: string) {
  const blob = new Blob([content], { type: mimeType })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement("a")
  anchor.href = url
  anchor.download = filename
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}

export function ExportMenu({ events, hasMore, baseName }: Props) {
  const [open, setOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const handleClick = (e: MouseEvent) => {
      if (!containerRef.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener("mousedown", handleClick)
    return () => document.removeEventListener("mousedown", handleClick)
  }, [open])

  const exportAs = (kind: "csv" | "json") => {
    const stem = `${fileStem(baseName)}-log-events`
    if (kind === "csv") {
      download(`${stem}.csv`, buildLogEventsCsv(events), "text/csv")
    } else {
      download(`${stem}.json`, buildLogEventsJson(events), "application/json")
    }
    setOpen(false)
  }

  const scope = `the ${formatQuantity(events.length, "loaded event")}`
  return (
    <div ref={containerRef} className="relative">
      <Button
        type="button"
        size="sm"
        variant="ghost"
        disabled={events.length === 0}
        onClick={() => setOpen((v) => !v)}
        className="h-7 px-2 text-2xs uppercase"
        // Loaded events only, and it says so — never implying full history.
        title={
          hasMore
            ? `Export ${scope} — more events exist on the server and are not loaded, so they are not included`
            : `Export ${scope}`
        }
      >
        <Download className="mr-1 h-3 w-3" />
        Export
      </Button>
      {open && (
        <div className="absolute top-full right-0 z-20 mt-1 flex w-44 flex-col overflow-hidden rounded-md border border-border bg-bg-elevated py-1 shadow-lg">
          {hasMore && (
            <span className="px-3 py-1 text-2xs text-fg-muted">
              Newer events are not loaded and will not be included.
            </span>
          )}
          <button
            type="button"
            onClick={() => exportAs("csv")}
            className="px-3 py-1.5 text-left font-mono text-2xs text-fg hover:bg-bg-muted"
          >
            CSV
            <span className="ml-1.5 text-fg-muted">timestamp, stream, message</span>
          </button>
          <button
            type="button"
            onClick={() => exportAs("json")}
            className="px-3 py-1.5 text-left font-mono text-2xs text-fg hover:bg-bg-muted"
          >
            JSON
            <span className="ml-1.5 text-fg-muted">raw fields</span>
          </button>
        </div>
      )}
    </div>
  )
}
