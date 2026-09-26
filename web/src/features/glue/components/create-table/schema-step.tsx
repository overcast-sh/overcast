import { AlertTriangle, FileQuestion } from "lucide-react"
import { Advisory } from "@/components/ui/advisory"
import { Badge } from "@/components/ui/badge"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { EmptyState, SectionLabel } from "@/components/ui/primitives"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { SkeletonRows } from "@/components/ui/skeleton"
import { kindLabel } from "@/features/s3/preview-kind"
import { formatCount, formatQuantity } from "@/lib/format"
import { SCAN_KEY_LIMIT } from "../../scan-prefix"
import { ColumnsEditor } from "./columns-editor"
import type { TableDraftState } from "./use-table-draft"

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

/** Step 2: what the sampled object says the table is, with its columns open for editing. */
export function SchemaStep({ draft }: { draft: TableDraftState }) {
  const { folder, scan, sample } = draft
  const location = `s3://${folder?.bucket ?? ""}/${folder?.prefix ?? ""}`

  if (scan.isLoading || sample.isLoading) return <SkeletonRows rows={6} noun="schema" />
  if (scan.error) {
    return (
      <EmptyState
        icon={<AlertTriangle className="h-8 w-8" />}
        title="Could not list the folder"
        description={errorText(scan.error)}
      />
    )
  }
  const sampled = scan.data?.sample
  if (!sampled) {
    return (
      <EmptyState
        icon={<FileQuestion className="h-8 w-8" />}
        title="No data files here"
        description={`Nothing under ${location} is a data file. Go back and pick the folder that holds them.`}
      />
    )
  }
  const sampleUri = `s3://${folder?.bucket ?? ""}/${sampled.key}`
  const layout = scan.data?.layout

  return (
    <div className="flex flex-col gap-4">
      <DefinitionList columns={2}>
        <Definition label="Sampled object" value={<S3UriLink uri={sampleUri} />} full />
        <Definition
          label="Format"
          value={sample.data && <Badge>{kindLabel(sample.data.format)}</Badge>}
        />
        <Definition
          label="Partitions"
          value={
            layout && layout.keys.length > 0
              ? `${layout.keys.join(", ")} · ${formatQuantity(layout.partitions.length, "partition")}`
              : "Not partitioned"
          }
        />
      </DefinitionList>
      {scan.data?.truncated && (
        <Advisory tone="info" title={`Listed the first ${formatCount(SCAN_KEY_LIMIT)} keys`}>
          Partitions past them are not added here. <em>Discover partitions</em> on the table finds
          the rest.
        </Advisory>
      )}
      {sample.error ? (
        <p role="alert" className="text-xs text-danger">
          {errorText(sample.error)}
        </p>
      ) : (
        <>
          <SectionLabel as="h3">Columns</SectionLabel>
          <ColumnsEditor label="Columns" columns={draft.columns} onChange={draft.setColumns} />
          {draft.partitionKeys.length > 0 && (
            <>
              <SectionLabel as="h3">Partition keys</SectionLabel>
              <ColumnsEditor
                label="Partition keys"
                columns={draft.partitionKeys}
                onChange={draft.setPartitionKeys}
                fixedNames
              />
            </>
          )}
        </>
      )}
    </div>
  )
}
