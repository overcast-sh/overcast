import { useQuery } from "@tanstack/react-query"
import type { Table, TableVersion } from "@aws-sdk/client-glue"
import { History } from "lucide-react"
import { DiffViewer } from "@/components/ui/diff-viewer"
import { FormField } from "@/components/ui/form"
import { ResourceTable } from "@/components/ui/resource-table"
import { Select } from "@/components/ui/select"
import { formatDate } from "@/lib/format"
import { glueTableVersionsQueryOptions } from "../../data"
import { tableInputOf } from "../../table-input"

interface VersionsTabProps {
  table: Table
  /** The versions compared, from the route's `from` and `to` search params. */
  from?: string
  to?: string
  onCompare: (from: string, to: string) => void
}

/** Newest first, by version number: GetTableVersions promises no order. */
function newestFirst(versions: readonly TableVersion[]): TableVersion[] {
  return [...versions].sort((a, b) => Number(b.VersionId) - Number(a.VersionId))
}

function inputText(version: TableVersion | undefined): string {
  return version?.Table ? JSON.stringify(tableInputOf(version.Table), null, 2) : ""
}

/**
 * Every version Glue keeps of the table, and a side-by-side diff of any
 * two — the quickest way to see what a CDK deploy or a PyIceberg commit
 * changed. The diff compares the table input, the part a writer sets.
 */
export function VersionsTab({ table, from, to, onCompare }: VersionsTabProps) {
  const query = useQuery(glueTableVersionsQueryOptions(table.DatabaseName ?? "", table.Name ?? ""))
  const versions = newestFirst(query.data ?? [])
  const ids = versions.map((v) => v.VersionId ?? "")
  const toId = to && ids.includes(to) ? to : ids[0]
  const fromId = from && ids.includes(from) ? from : (ids[ids.indexOf(toId) + 1] ?? toId)
  const byId = (id?: string) => versions.find((v) => v.VersionId === id)
  const pick = (label: string, value: string | undefined, onChange: (id: string) => void) => (
    <FormField label={label} className="w-40">
      <Select value={value} onChange={(e) => onChange(e.target.value)}>
        {ids.map((id) => (
          <option key={id} value={id}>
            Version {id}
          </option>
        ))}
      </Select>
    </FormField>
  )

  return (
    <div className="flex flex-col gap-4">
      <ResourceTable<TableVersion>
        query={{ data: versions, isLoading: query.isLoading, error: query.error }}
        noun="versions"
        rowKey={(v) => v.VersionId ?? ""}
        onRowClick={(v) => {
          const id = v.VersionId ?? ""
          onCompare(ids[ids.indexOf(id) + 1] ?? id, id)
        }}
        rowClassName={(v) => (v.VersionId === toId ? "bg-accent-muted" : undefined)}
        emptyIcon={History}
        emptyTitle="No versions"
        emptyDescription="Glue keeps a version each time the table is updated."
        columns={[
          { id: "version", header: "Version", cell: (v) => v.VersionId },
          {
            id: "updated",
            header: "Updated",
            cell: (v) => formatDate(v.Table?.UpdateTime ?? v.Table?.CreateTime),
          },
          {
            id: "columns",
            header: "Columns",
            headerClassName: "text-right",
            cellClassName: "text-right tabular-nums",
            cell: (v) => v.Table?.StorageDescriptor?.Columns?.length ?? 0,
          },
        ]}
      />
      {versions.length > 1 && fromId && toId && (
        <>
          <div className="flex flex-wrap items-end gap-3">
            {pick("Compare", fromId, (id) => onCompare(id, toId))}
            {pick("With", toId, (id) => onCompare(fromId, id))}
          </div>
          <DiffViewer
            original={inputText(byId(fromId))}
            modified={inputText(byId(toId))}
            originalLabel={`Version ${fromId}`}
            modifiedLabel={`Version ${toId}`}
          />
        </>
      )}
    </div>
  )
}
