import type { Table } from "@aws-sdk/client-glue"
import { IcebergSummaryCard } from "@/components/iceberg/iceberg-summary-card"
import { MetadataGate } from "@/components/iceberg/metadata-gate"
import { IcebergMetadataFiles } from "@/components/iceberg/metadata-file-viewer"
import { QueryAsOfSnapshot } from "@/components/iceberg/query-as-of"
import { IcebergSnapshotList } from "@/components/iceberg/snapshot-list"
import type { IcebergTableRef } from "@/components/iceberg/snapshot-sql"
import { useIcebergMetadataFile } from "@/components/iceberg/use-metadata-file"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import { SectionLabel } from "@/components/ui/primitives"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { ATHENA_CATALOG } from "../../athena-link"
import { metadataLocation, tableParameter } from "../../table-format"
import type { GlueTableSearch } from "../../views"

export type IcebergSelection = Pick<GlueTableSearch, "snapshot" | "version" | "compare">

interface IcebergTabProps {
  database: string
  table: Table
  selection: IcebergSelection
  onSelectionChange: (next: IcebergSelection) => void
}

/**
 * An Iceberg table registered in Glue: its pointer into its own metadata,
 * then the shared Iceberg viewer over the file it points at — the snapshots,
 * each with *Query as of this snapshot* in Athena, and every metadata version
 * the metadata log keeps, with a diff between any two.
 *
 * A commit through Glue (`UpdateTable`) is a `glue:TableChanged` event, which
 * re-reads the table and so moves this tab to the new `metadata_location`.
 */
export function IcebergTab({ database, table, selection, onSelectionChange }: IcebergTabProps) {
  const current = metadataLocation(table)
  const previous = tableParameter(table, "previous_metadata_location")
  const metadata = useIcebergMetadataFile(current)
  const ref: IcebergTableRef = { catalog: ATHENA_CATALOG, database, table: table.Name ?? "" }
  return (
    <div className="flex flex-col gap-4">
      <DefinitionCard>
        <Definition label="Table type" value={tableParameter(table, "table_type")} />
        <Definition
          label="Metadata location"
          value={current && <S3UriLink uri={current} />}
          copyable={current}
          full
        />
        <Definition
          label="Previous metadata"
          value={previous && <S3UriLink uri={previous} />}
          full
        />
      </DefinitionCard>
      <MetadataGate
        location={current}
        query={metadata}
        missingDescription="The table's parameters name no metadata_location. An Iceberg client writes one when it creates or commits to the table."
      >
        {(file) => (
          <>
            {file.metadata && (
              <>
                <IcebergSummaryCard metadata={file.metadata} />
                <section className="flex flex-col gap-2">
                  <SectionLabel>Snapshots</SectionLabel>
                  <IcebergSnapshotList
                    metadata={file.metadata}
                    openSnapshotId={selection.snapshot}
                    snapshotActions={(s) => <QueryAsOfSnapshot table={ref} snapshot={s} />}
                  />
                </section>
              </>
            )}
            <section className="flex flex-col gap-2">
              <SectionLabel>Metadata files</SectionLabel>
              <IcebergMetadataFiles
                current={file}
                selection={{ version: selection.version, compare: selection.compare }}
                onSelectionChange={(next) => onSelectionChange({ ...selection, ...next })}
              />
            </section>
          </>
        )}
      </MetadataGate>
    </div>
  )
}
