import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { MetadataGate, ParsedMetadataGate } from "@/components/iceberg/metadata-gate"
import {
  IcebergMetadataFiles,
  type MetadataSelection,
} from "@/components/iceberg/metadata-file-viewer"
import { IcebergSchemaView } from "@/components/iceberg/schema-view"
import { IcebergSnapshotList } from "@/components/iceberg/snapshot-list"
import { QueryAsOfSnapshot } from "@/components/iceberg/query-as-of"
import { useIcebergMetadataFile } from "@/components/iceberg/use-metadata-file"
import { PageHeader } from "@/components/ui/primitives"
import { RefreshAction } from "@/components/ui/resource-list-page"
import { Tab, TabList, TabPanel, Tabs } from "@/components/ui/tabs"
import { RawStateLink } from "@/features/debug/raw-state-link"
import { s3tablesTableRef } from "../athena-sql"
import { tableArn, tableBucketsQueryOptions, tableQueryOptions, type ConfigTarget } from "../data"
import type { TableSearch, TableTab } from "../search"
import { DetailLayout, DetailLoading, DetailMissing } from "./detail-layout"
import { MaintenanceTab } from "./maintenance-tab"
import { PolicyTab } from "./policy-tab"
import { TableOverview } from "./table-overview"

interface TableDetailProps {
  bucketName: string
  tableId: string
  search: TableSearch
  onSearchChange: (patch: Partial<TableSearch>) => void
}

/**
 * `/s3tables/$bucket/$tableId`: one Iceberg table. The table record follows
 * `s3tables:TableCommitted`, so every tab that reads the metadata moves to the
 * new `metadata.json` the moment a client commits.
 */
export function TableDetail({ bucketName, tableId, search, onSearchChange }: TableDetailProps) {
  const bucket = useQuery({
    ...tableBucketsQueryOptions(),
    select: (buckets) => buckets.find((b) => b.name === bucketName),
  })
  const bucketArn = bucket.data?.arn ?? ""
  const arn = bucketArn ? tableArn(bucketArn, tableId) : ""
  const table = useQuery(tableQueryOptions(arn))
  const metadata = useIcebergMetadataFile(table.data?.metadataLocation)

  if (bucket.isLoading || table.isLoading) return <DetailLoading title={tableId} />
  if (!bucket.isLoading && !bucket.data) {
    return (
      <DetailMissing
        title={tableId}
        heading={bucket.error ? "Could not load the table bucket" : "No such table bucket"}
        description={bucket.error?.message ?? `There is no table bucket called ${bucketName}.`}
      />
    )
  }
  if (!table.data) {
    return (
      <DetailMissing
        title={tableId}
        heading="No such table"
        description={
          table.error?.message ??
          `Table bucket ${bucketName} has no table with this id; it may have been deleted.`
        }
      />
    )
  }
  const t = table.data
  const namespace = t.namespace?.join(".") ?? ""
  const name = t.name ?? tableId
  const target: ConfigTarget = {
    resource: { kind: "table", tableBucketARN: bucketArn, namespace, name },
    arn,
  }
  const tab = search.tab ?? "overview"
  const athenaRef = s3tablesTableRef(bucketName, namespace, name)
  const gate = {
    location: t.metadataLocation,
    query: metadata,
    missingDescription:
      "The table was created without a schema. The first commit from an Iceberg client writes its metadata.json.",
  }

  return (
    <DetailLayout
      connect={{ warehouseArn: bucketArn, table: { namespace, name } }}
      header={
        <PageHeader
          title={`${namespace}.${name}`}
          meta={
            <>
              Iceberg table in{" "}
              <Link
                to="/s3tables/$bucket"
                params={{ bucket: bucketName }}
                className="text-accent hover:underline"
              >
                {bucketName}
              </Link>
              {" · updates live as clients commit"}
            </>
          }
          actions={
            <>
              <RawStateLink service="s3tables" />
              <RefreshAction isFetching={table.isFetching} onClick={() => void table.refetch()} />
            </>
          }
        />
      }
    >
      <Tabs selectedKey={tab} onSelectionChange={(key) => onSearchChange({ tab: key as TableTab })}>
        <TabList aria-label="Table views">
          <Tab id="overview">Overview</Tab>
          <Tab id="schema">Schema</Tab>
          <Tab id="snapshots">Snapshots</Tab>
          <Tab id="metadata">Metadata</Tab>
          <Tab id="policy">Policy</Tab>
          <Tab id="maintenance">Maintenance</Tab>
        </TabList>
        <TabPanel id="overview" className="pt-4">
          <TableOverview table={t} metadata={metadata.data} />
        </TabPanel>
        <TabPanel id="schema" className="pt-4">
          <ParsedMetadataGate {...gate}>
            {(m) => <IcebergSchemaView metadata={m} />}
          </ParsedMetadataGate>
        </TabPanel>
        <TabPanel id="snapshots" className="pt-4">
          <ParsedMetadataGate {...gate}>
            {(m) => (
              <IcebergSnapshotList
                metadata={m}
                openSnapshotId={search.snapshot}
                snapshotActions={(s) => <QueryAsOfSnapshot table={athenaRef} snapshot={s} />}
              />
            )}
          </ParsedMetadataGate>
        </TabPanel>
        <TabPanel id="metadata" className="pt-4">
          <MetadataGate {...gate}>
            {(file) => (
              <IcebergMetadataFiles
                current={file}
                selection={{ version: search.version, compare: search.compare }}
                onSelectionChange={(next: MetadataSelection) => onSearchChange(next)}
              />
            )}
          </MetadataGate>
        </TabPanel>
        <TabPanel id="policy" className="pt-4">
          <PolicyTab target={target} />
        </TabPanel>
        <TabPanel id="maintenance" className="pt-4">
          <MaintenanceTab target={target} />
        </TabPanel>
      </Tabs>
    </DetailLayout>
  )
}
