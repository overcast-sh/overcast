import type { Table } from "@aws-sdk/client-glue"
import { Advisory } from "@/components/ui/advisory"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { metadataLocation, tableParameter } from "../../table-format"

/**
 * An Iceberg table's pointer into its own metadata. Snapshots and the
 * metadata history are the shared Iceberg viewer's (#2087), which this tab
 * will embed; until then it links to the metadata file, which the S3
 * preview summarises.
 */
export function IcebergTab({ table }: { table: Table }) {
  const current = metadataLocation(table)
  const previous = tableParameter(table, "previous_metadata_location")
  return (
    <div className="flex flex-col gap-3">
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
      <Advisory tone="info" title="Snapshots are not shown here yet">
        The snapshot list and metadata history arrive with the shared Iceberg viewer (#2087). Until
        then, open the metadata location: the S3 preview summarises its snapshots.
      </Advisory>
    </div>
  )
}
