import { keepPreviousData, useQuery, type UseQueryResult } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { FileJson, TextSearch } from "lucide-react"
import type { ReactNode } from "react"
import { icebergMetadataFileQueryOptions, type MetadataFile } from "@/components/iceberg/data"
import type { IcebergMetadata, IcebergSnapshot } from "@/components/iceberg/metadata"
import {
  IcebergMetadataFiles,
  type MetadataSelection,
} from "@/components/iceberg/metadata-file-viewer"
import { IcebergSchemaView } from "@/components/iceberg/schema-view"
import { IcebergSnapshotList } from "@/components/iceberg/snapshot-list"
import { EmptyState, PageHeader } from "@/components/ui/primitives"
import { RefreshAction, RowAction } from "@/components/ui/resource-list-page"
import { SkeletonRows } from "@/components/ui/skeleton"
import { Tab, TabList, TabPanel, Tabs } from "@/components/ui/tabs"
import { athenaEditorLink } from "@/features/athena/links"
import { RawStateLink } from "@/features/debug/raw-state-link"
import type { Table } from "@/services/api/s3tables"
import { s3tablesCatalog, snapshotQuerySql } from "../athena-sql"
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
  // A commit moves the table to a new metadata file, and so to a new query.
  // The last file stays on screen while the next one loads, so the tabs do
  // not flash to a skeleton and lose their expanded rows on every commit.
  const metadata = useQuery({
    ...icebergMetadataFileQueryOptions(table.data?.metadataLocation ?? ""),
    placeholderData: keepPreviousData,
  })

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
          <ParsedMetadataGate table={t} query={metadata}>
            {(m) => <IcebergSchemaView metadata={m} />}
          </ParsedMetadataGate>
        </TabPanel>
        <TabPanel id="snapshots" className="pt-4">
          <ParsedMetadataGate table={t} query={metadata}>
            {(m) => (
              <IcebergSnapshotList
                metadata={m}
                openSnapshotId={search.snapshot}
                snapshotActions={(s) => (
                  <QueryAsOf bucket={bucketName} namespace={namespace} table={name} snapshot={s} />
                )}
              />
            )}
          </ParsedMetadataGate>
        </TabPanel>
        <TabPanel id="metadata" className="pt-4">
          <MetadataGate table={t} query={metadata}>
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

/** *Query as of this snapshot*: the table at that snapshot, in a new Athena query tab. */
function QueryAsOf({
  bucket,
  namespace,
  table,
  snapshot,
}: {
  bucket: string
  namespace: string
  table: string
  snapshot: IcebergSnapshot
}) {
  const link = athenaEditorLink({
    catalog: s3tablesCatalog(bucket),
    database: namespace,
    sql: snapshotQuerySql(bucket, namespace, table, snapshot.snapshotId),
  })
  return (
    <RowAction label="Query as of this snapshot" asChild>
      <Link {...link}>
        <TextSearch className="size-3.5" />
      </Link>
    </RowAction>
  )
}

const unreadable = (reason?: string) => (
  <EmptyState
    icon={<FileJson className="size-8" />}
    title="Could not read the table's metadata"
    description={
      reason ??
      "The metadata file is not Iceberg table metadata, or is larger than the console reads."
    }
  />
)

/**
 * The metadata file a tab reads, once it is there: a skeleton while it loads,
 * and a plain reason when the table has none or it could not be read.
 */
function MetadataGate({
  table,
  query,
  children,
}: {
  table: Table
  query: UseQueryResult<MetadataFile>
  children: (file: MetadataFile) => ReactNode
}) {
  if (!table.metadataLocation) {
    return (
      <EmptyState
        icon={<FileJson className="size-8" />}
        title="No metadata yet"
        description="The table was created without a schema. The first commit from an Iceberg client writes its metadata.json."
      />
    )
  }
  if (query.isLoading) return <SkeletonRows rows={6} noun="metadata" />
  return query.data ? children(query.data) : unreadable(query.error?.message)
}

/** The same gate, for a tab that needs the file to read as Iceberg table metadata. */
function ParsedMetadataGate({
  children,
  ...gate
}: {
  table: Table
  query: UseQueryResult<MetadataFile>
  children: (metadata: IcebergMetadata) => ReactNode
}) {
  return (
    <MetadataGate {...gate}>
      {(file) => (file.metadata ? children(file.metadata) : unreadable())}
    </MetadataGate>
  )
}
