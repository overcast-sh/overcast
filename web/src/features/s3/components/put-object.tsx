/**
 * PutObject — /s3/$bucket/upload
 *
 * Full-page upload screen inspired by the AWS console upload flow.
 * Files arrive from uploadStore (set by drag-drop or the Upload button in
 * bucket-detail), and can also be added inline from this screen.
 * Nothing is uploaded until the user clicks "Upload".
 */
import { useState, useRef } from "react"
import { fieldLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { useNavigate } from "@tanstack/react-router"
import { Upload, X, Plus, Trash2, FolderOpen } from "lucide-react"
import { Route } from "@/routes/s3/$bucket/upload"
import { uploadStore } from "@/lib/upload-store"
import { useQueryClient } from "@tanstack/react-query"
import { s3Keys } from "@/features/s3/data"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { FormField } from "@/components/ui/form"
import { PageHeader, Spinner } from "@/components/ui/primitives"
import { useToast } from "@/components/ui/toast"
import { formatBytes, formatQuantity } from "@/lib/format"

// ─── Storage classes ──────────────────────────────────────────────────────────

const STORAGE_CLASSES = [
  { value: "STANDARD", label: "Standard" },
  { value: "INTELLIGENT_TIERING", label: "Intelligent-Tiering" },
  { value: "STANDARD_IA", label: "Standard-IA" },
  { value: "ONEZONE_IA", label: "One Zone-IA" },
  { value: "REDUCED_REDUNDANCY", label: "Reduced Redundancy" },
  { value: "GLACIER_IR", label: "Glacier Instant Retrieval" },
  { value: "GLACIER", label: "Glacier Flexible Retrieval" },
  { value: "DEEP_ARCHIVE", label: "Glacier Deep Archive" },
]

// ─── Types ────────────────────────────────────────────────────────────────────

type UploadStatus = "pending" | "uploading" | "done" | "error"

interface FileRow {
  id: number
  file: File
  key: string
  contentType: string
  status: UploadStatus
  progress: number // 0-100, only meaningful when status === "uploading"
  errorMsg?: string
}

let nextId = 0

function makeRow(file: File, prefix: string): FileRow {
  return {
    id: nextId++,
    file,
    key: `${prefix}${file.name}`,
    contentType: file.type || "application/octet-stream",
    status: "pending",
    progress: 0,
  }
}

// ─── Component ────────────────────────────────────────────────────────────────

export function PutObject() {
  const { bucket } = Route.useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast } = useToast()

  // Pull files from store (set by bucket-detail on drag/click); clear after reading.
  const [{ prefix, rows: initialRows }] = useState(() => {
    const pending = uploadStore.take()
    return {
      prefix: pending?.prefix ?? "",
      rows: (pending?.files ?? []).map((f) => makeRow(f, pending?.prefix ?? "")),
    }
  })

  const [rows, setRows] = useState<FileRow[]>(initialRows)

  const [storageClass, setStorageClass] = useState("STANDARD")
  const [metadata, setMetadata] = useState<{ key: string; value: string }[]>([])
  const [responseHeaders, setResponseHeaders] = useState({
    contentDisposition: "",
    contentEncoding: "",
    contentLanguage: "",
    cacheControl: "",
    expires: "",
  })
  const [uploading, setUploading] = useState(false)

  const fileInputRef = useRef<HTMLInputElement>(null)
  const dropRef = useRef<HTMLDivElement>(null)
  const dragCounterRef = useRef(0)
  const [dropActive, setDropActive] = useState(false)

  // ── File list helpers ────────────────────────────────────────────────────

  function addFiles(files: File[]) {
    setRows((prev) => [...prev, ...files.map((f) => makeRow(f, prefix))])
  }

  function updateRow(id: number, patch: Partial<FileRow>) {
    setRows((prev) => prev.map((r) => (r.id === id ? { ...r, ...patch } : r)))
  }

  function removeRow(id: number) {
    setRows((prev) => prev.filter((r) => r.id !== id))
  }

  // ── Drag & drop on the add-files zone ───────────────────────────────────

  function onDragEnter(e: React.DragEvent) {
    e.preventDefault()
    dragCounterRef.current++
    if (e.dataTransfer.types.includes("Files")) setDropActive(true)
  }
  function onDragLeave(e: React.DragEvent) {
    e.preventDefault()
    if (--dragCounterRef.current === 0) setDropActive(false)
  }
  function onDragOver(e: React.DragEvent) {
    e.preventDefault()
  }
  function onDrop(e: React.DragEvent) {
    e.preventDefault()
    dragCounterRef.current = 0
    setDropActive(false)
    addFiles(Array.from(e.dataTransfer.files))
  }

  // ── Metadata helpers ─────────────────────────────────────────────────────

  function addMetaRow() {
    setMetadata((prev) => [...prev, { key: "", value: "" }])
  }
  function updateMeta(i: number, patch: Partial<{ key: string; value: string }>) {
    setMetadata((prev) => prev.map((m, j) => (j === i ? { ...m, ...patch } : m)))
  }
  function removeMeta(i: number) {
    setMetadata((prev) => prev.filter((_, j) => j !== i))
  }

  // ── Upload ───────────────────────────────────────────────────────────────

  async function handleUpload() {
    if (rows.length === 0) return
    setUploading(true)

    // Build the shared headers for all files.
    const sharedHeaders: Record<string, string> = {
      "x-amz-storage-class": storageClass,
    }
    for (const { key, value } of metadata) {
      if (key.trim()) sharedHeaders[`x-amz-meta-${key.trim()}`] = value
    }
    if (responseHeaders.contentDisposition)
      sharedHeaders["x-object-content-disposition"] = responseHeaders.contentDisposition
    if (responseHeaders.contentEncoding)
      sharedHeaders["x-object-content-encoding"] = responseHeaders.contentEncoding
    if (responseHeaders.contentLanguage)
      sharedHeaders["x-object-content-language"] = responseHeaders.contentLanguage
    if (responseHeaders.cacheControl)
      sharedHeaders["x-object-cache-control"] = responseHeaders.cacheControl
    if (responseHeaders.expires) sharedHeaders["x-object-expires"] = responseHeaders.expires

    let errorCount = 0
    for (const row of rows) {
      if (row.status === "done") continue
      updateRow(row.id, { status: "uploading", progress: 0, errorMsg: undefined })
      try {
        const url = `/api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(row.key)}`
        await new Promise<void>((resolve, reject) => {
          const xhr = new XMLHttpRequest()
          xhr.open("PUT", url)
          xhr.setRequestHeader("Content-Type", row.contentType)
          for (const [k, v] of Object.entries(sharedHeaders)) xhr.setRequestHeader(k, v)
          xhr.upload.onprogress = (e) => {
            if (e.lengthComputable) {
              updateRow(row.id, { progress: Math.round((e.loaded / e.total) * 100) })
            }
          }
          xhr.onload = () => {
            if (xhr.status >= 200 && xhr.status < 300) {
              updateRow(row.id, { progress: 100 })
              resolve()
            } else {
              let msg = `HTTP ${xhr.status}`
              try {
                msg = JSON.parse(xhr.responseText)?.message ?? msg
              } catch {
                /* ignore JSON parse errors */
              }
              reject(new Error(msg))
            }
          }
          xhr.onerror = () => reject(new Error("Network error"))
          xhr.send(row.file)
        })
        updateRow(row.id, { status: "done" })
      } catch (err) {
        errorCount++
        console.error("[put-object] upload error", row.key, err)
        updateRow(row.id, { status: "error", errorMsg: String(err) })
      }
    }

    setUploading(false)
    void qc.invalidateQueries({ queryKey: s3Keys.objectList(bucket, prefix) })

    if (errorCount === 0) {
      toast({
        title: `Uploaded ${formatQuantity(rows.length, "file")}`,
        variant: "success",
      })
      // Back to the folder the files landed in, not the bucket root — the
      // upload is only verifiable where it happened.
      void navigate({
        to: "/s3/$bucket/objects/$",
        params: { bucket, _splat: prefix },
        search: {},
      })
    } else {
      toast({
        title: `${formatQuantity(errorCount, "file")} failed to upload`,
        variant: "danger",
      })
    }
  }

  // ── Derived state ─────────────────────────────────────────────────────────

  const pendingCount = rows.filter((r) => r.status === "pending" || r.status === "error").length
  const doneCount = rows.filter((r) => r.status === "done").length
  const allDone = rows.length > 0 && doneCount === rows.length

  // ── Render ────────────────────────────────────────────────────────────────

  return (
    <div className="flex max-w-4xl flex-col gap-6">
      <PageHeader title="Upload" />

      {/* ── Files section ──────────────────────────────────────────────── */}
      <Section title="Files" count={rows.length}>
        {/* Drop zone / add files */}
        <div
          ref={dropRef}
          onDragEnter={onDragEnter}
          onDragLeave={onDragLeave}
          onDragOver={onDragOver}
          onDrop={onDrop}
          className={[
            "flex items-center justify-center rounded-lg border-2 border-dashed px-6 py-8 transition-colors",
            dropActive ? "border-accent bg-accent-muted" : "border-border hover:border-accent",
          ].join(" ")}
        >
          <div className="flex flex-col items-center gap-2 text-center">
            <FolderOpen className="h-8 w-8 text-fg-muted" />
            <p className="text-sm text-fg-muted">
              Drag files here, or{" "}
              <button
                className="text-accent underline underline-offset-2 hover:no-underline"
                onClick={() => fileInputRef.current?.click()}
              >
                browse
              </button>
            </p>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              className="sr-only"
              onChange={(e) => {
                addFiles(Array.from(e.target.files ?? []))
                e.target.value = ""
              }}
            />
          </div>
        </div>

        {/* File table */}
        {rows.length > 0 && (
          <div className="overflow-hidden rounded-lg border border-border">
            <table className="w-full font-mono text-xs">
              <thead>
                <tr
                  className={cn(
                    fieldLabel,
                    "border-b border-border bg-bg text-left text-fg-subtle",
                  )}
                >
                  <th className="px-3 py-2">Object key</th>
                  <th className="px-3 py-2">Content-Type</th>
                  <th className="px-3 py-2">Size</th>
                  <th className="w-48 px-3 py-2">Status</th>
                  <th className="w-8 px-3 py-2" />
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {rows.map((row) => (
                  <tr key={row.id} className="group">
                    <td className="px-3 py-2">
                      <Input
                        value={row.key}
                        onChange={(e) => updateRow(row.id, { key: e.target.value })}
                        disabled={uploading || row.status === "done"}
                        className="h-7"
                      />
                    </td>
                    <td className="px-3 py-2">
                      <Input
                        value={row.contentType}
                        onChange={(e) => updateRow(row.id, { contentType: e.target.value })}
                        disabled={uploading || row.status === "done"}
                        className="h-7"
                      />
                    </td>
                    <td className="px-3 py-2 whitespace-nowrap text-fg-muted">
                      {formatBytes(row.file.size)}
                    </td>
                    <td className="px-3 py-2">
                      <StatusBadge row={row} />
                    </td>
                    <td className="px-3 py-2">
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        disabled={uploading}
                        onClick={() => removeRow(row.id)}
                        title="Remove"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      {/* ── Destination ────────────────────────────────────────────────── */}
      <Section title="Destination">
        <p className="font-mono text-sm text-fg-muted">
          s3://
          <span className="text-fg">{bucket}</span>
          {prefix && <span className="text-fg">/{prefix}</span>}
        </p>
      </Section>

      {/* ── Properties ─────────────────────────────────────────────────── */}
      <Section title="Properties">
        <FormField label="Storage class" htmlFor="storage-class">
          <select
            id="storage-class"
            value={storageClass}
            onChange={(e) => setStorageClass(e.target.value)}
            disabled={uploading}
            className="flex h-8 w-full rounded-md border border-border bg-bg px-3 py-1 text-sm text-fg transition-colors focus-visible:border-accent focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50"
          >
            {STORAGE_CLASSES.map((sc) => (
              <option key={sc.value} value={sc.value}>
                {sc.label}
              </option>
            ))}
          </select>
        </FormField>
      </Section>

      {/* ── Response headers ────────────────────────────────────────────── */}
      <Section title="Response headers" optional>
        <p className="text-sm text-fg-muted">
          Stored with the object and returned as HTTP headers on every GetObject response.
        </p>
        <div className="grid grid-cols-2 gap-4">
          <FormField label="Content-Disposition" htmlFor="rh-cd">
            <Input
              id="rh-cd"
              value={responseHeaders.contentDisposition}
              placeholder="attachment; filename=example.txt"
              onChange={(e) =>
                setResponseHeaders((h) => ({ ...h, contentDisposition: e.target.value }))
              }
              disabled={uploading}
              className="text-sm"
            />
          </FormField>
          <FormField label="Content-Encoding" htmlFor="rh-ce">
            <Input
              id="rh-ce"
              value={responseHeaders.contentEncoding}
              placeholder="gzip"
              onChange={(e) =>
                setResponseHeaders((h) => ({ ...h, contentEncoding: e.target.value }))
              }
              disabled={uploading}
              className="text-sm"
            />
          </FormField>
          <FormField label="Content-Language" htmlFor="rh-cl">
            <Input
              id="rh-cl"
              value={responseHeaders.contentLanguage}
              placeholder="en-US"
              onChange={(e) =>
                setResponseHeaders((h) => ({ ...h, contentLanguage: e.target.value }))
              }
              disabled={uploading}
              className="text-sm"
            />
          </FormField>
          <FormField label="Cache-Control" htmlFor="rh-cc">
            <Input
              id="rh-cc"
              value={responseHeaders.cacheControl}
              placeholder="max-age=3600, public"
              onChange={(e) => setResponseHeaders((h) => ({ ...h, cacheControl: e.target.value }))}
              disabled={uploading}
              className="text-sm"
            />
          </FormField>
          <FormField label="Expires" htmlFor="rh-exp">
            <Input
              id="rh-exp"
              value={responseHeaders.expires}
              placeholder="Thu, 01 Jan 2026 00:00:00 GMT"
              onChange={(e) => setResponseHeaders((h) => ({ ...h, expires: e.target.value }))}
              disabled={uploading}
              className="text-sm"
            />
          </FormField>
        </div>
      </Section>

      {/* ── Metadata ───────────────────────────────────────────────────── */}
      <Section title="Additional metadata" optional>
        {metadata.length > 0 && (
          <div className="flex flex-col gap-2">
            <div className="grid grid-cols-[1fr_1fr_auto] gap-2">
              <Label>Key</Label>
              <Label>Value</Label>
              <span />
            </div>
            {metadata.map((m, i) => (
              <div key={i} className="grid grid-cols-[1fr_1fr_auto] items-center gap-2">
                <Input
                  value={m.key}
                  placeholder="key"
                  onChange={(e) => updateMeta(i, { key: e.target.value })}
                  disabled={uploading}
                  className="font-mono text-sm"
                />
                <Input
                  value={m.value}
                  placeholder="value"
                  onChange={(e) => updateMeta(i, { value: e.target.value })}
                  disabled={uploading}
                  className="text-sm"
                />
                <Button
                  variant="ghost"
                  size="icon-sm"
                  disabled={uploading}
                  onClick={() => removeMeta(i)}
                >
                  <X className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
          </div>
        )}
        <Button
          variant="secondary"
          size="sm"
          disabled={uploading}
          onClick={addMetaRow}
          className="w-fit"
        >
          <Plus className="h-3.5 w-3.5" /> Add entry
        </Button>
      </Section>

      {/* ── Action bar ──────────────────────────────────────────────────── */}
      <div className="flex items-center justify-between border-t border-border pt-4">
        <div className="text-sm text-fg-muted">
          {rows.length === 0 ? (
            "No files added"
          ) : allDone ? (
            <span className="text-success">All files uploaded</span>
          ) : (
            <>
              {formatQuantity(rows.length, "file")} ·{" "}
              {formatBytes(rows.reduce((a, r) => a + r.file.size, 0))} total
            </>
          )}
        </div>
        <div className="flex gap-2">
          <Button
            variant="secondary"
            onClick={() =>
              navigate({
                to: "/s3/$bucket/objects/$",
                params: { bucket, _splat: prefix },
                search: {},
              })
            }
            disabled={uploading}
          >
            {allDone ? "Back to bucket" : "Cancel"}
          </Button>
          {!allDone && (
            <Button onClick={handleUpload} disabled={uploading || pendingCount === 0}>
              {uploading ? (
                <>
                  <Spinner className="h-4 w-4" /> Uploading…
                </>
              ) : (
                <>
                  <Upload className="h-4 w-4" />
                  Upload {pendingCount > 0 ? formatQuantity(pendingCount, "file") : ""}
                </>
              )}
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}

// ─── Sub-components ───────────────────────────────────────────────────────────

function Section({
  title,
  count,
  optional,
  children,
}: {
  title: string
  count?: number
  optional?: boolean
  children: React.ReactNode
}) {
  return (
    <div className="rounded-lg border border-border bg-bg-elevated">
      <div className="flex items-center gap-2 border-b border-border px-4 py-3">
        <h2 className="font-mono text-sm font-semibold text-fg">{title}</h2>
        {count !== undefined && (
          <span className="rounded-full bg-accent-muted px-2 py-0.5 font-mono text-xs font-medium text-accent">
            {count}
          </span>
        )}
        {optional && <span className="text-sm text-fg-subtle">— optional</span>}
      </div>
      <div className="flex flex-col gap-4 p-4">{children}</div>
    </div>
  )
}

function StatusBadge({ row }: { row: FileRow }) {
  switch (row.status) {
    case "uploading":
      return (
        <div className="flex items-center gap-2">
          <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-border">
            <div
              className="h-full rounded-full bg-accent transition-all duration-150"
              style={{ width: `${row.progress}%` }}
            />
          </div>
          <span className="w-8 text-right font-mono text-sm text-fg-muted tabular-nums">
            {row.progress}%
          </span>
        </div>
      )
    case "done":
      return <span className="font-mono text-sm font-medium text-success">Done</span>
    case "error":
      return (
        <span className="text-sm font-medium text-danger" title={row.errorMsg}>
          Error
        </span>
      )
    default:
      return <span className="font-mono text-sm text-fg-subtle">Pending</span>
  }
}
