import { Advisory } from "@/components/ui/advisory"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { IcebergSummaryCard } from "@/components/iceberg/iceberg-summary-card"
import type { MetadataFile } from "@/components/iceberg/data"
import { formatDate } from "@/lib/format"
import type { Table } from "@/services/api/s3tables"

/**
 * The table's S3 Tables record — ARN, format, version token, where its
 * metadata and data live — and, once it has metadata, the Iceberg summary.
 */
export function TableOverview({ table, metadata }: { table: Table; metadata?: MetadataFile }) {
  return (
    <div className="flex flex-col gap-4">
      <DefinitionCard>
        <Definition label="Table ARN" value={table.tableARN} copyable full />
        <Definition label="Namespace" value={table.namespace?.join(".")} />
        <Definition label="Format" value={table.format} />
        <Definition label="Version token" value={table.versionToken} copyable />
        <Definition label="Created" value={formatDate(table.createdAt)} />
        <Definition label="Modified" value={formatDate(table.modifiedAt)} />
        <Definition label="Owner account" value={table.ownerAccountId} />
        <Definition
          full
          label="Metadata location"
          value={table.metadataLocation && <S3UriLink uri={table.metadataLocation} />}
          copyable={table.metadataLocation}
        />
        <Definition
          full
          label="Warehouse"
          value={table.warehouseLocation && <S3UriLink uri={table.warehouseLocation} />}
          copyable={table.warehouseLocation}
        />
      </DefinitionCard>
      <Advisory
        tone="info"
        title="The warehouse is a bucket S3 Tables manages"
        docsPath="services/s3tables/limitations.md#warehouse-buckets"
      >
        Overcast keeps the table's metadata and data files in an ordinary S3 bucket you can browse.
        On AWS it is hidden behind the table; write to it only through an Iceberg client.
      </Advisory>
      {!table.metadataLocation ? (
        <Advisory tone="info" title="No metadata yet">
          The table was created without a schema, so it has no <code>metadata.json</code>. The first
          commit from an Iceberg client — PyIceberg's <code>create_table</code>, a Spark{" "}
          <code>CREATE TABLE</code> — writes one.
        </Advisory>
      ) : (
        metadata?.metadata && <IcebergSummaryCard metadata={metadata.metadata} />
      )}
    </div>
  )
}
