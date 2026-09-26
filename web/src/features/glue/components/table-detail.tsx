import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import type { Table } from "@aws-sdk/client-glue"
import { ArnText, LINK_CLASS } from "@/components/ui/arn-link"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import { RefreshAction, ResourceListPage } from "@/components/ui/resource-list-page"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { SkeletonRows } from "@/components/ui/skeleton"
import { Tab, TabList, TabPanel, Tabs } from "@/components/ui/tabs"
import { useEndpoint } from "@/hooks/use-endpoint"
import { formatDate } from "@/lib/format"
import { tableArn } from "../arns"
import { glueTableQueryOptions } from "../data"
import { isIcebergTable, tableLocation } from "../table-format"
import type { GlueTableSearch, TableTab } from "../views"
import { FormatBadge } from "./format-badge"
import { LoadErrorPage } from "./load-error-page"
import { QueryInAthenaButton } from "./query-in-athena"
import { DataTab } from "./table-tabs/data-tab"
import { IcebergTab } from "./table-tabs/iceberg-tab"
import { PartitionsTab } from "./table-tabs/partitions-tab"
import { PropertiesTab } from "./table-tabs/properties-tab"
import { SchemaTab } from "./table-tabs/schema-tab"
import { VersionsTab } from "./table-tabs/versions-tab"

interface TableDetailProps {
  database: string
  name: string
  search: GlueTableSearch
  onSearchChange: (patch: Partial<GlueTableSearch>) => void
}

const TAB_LABELS: Record<TableTab, string> = {
  schema: "Schema",
  partitions: "Partitions",
  data: "Data",
  iceberg: "Iceberg",
  versions: "Versions",
  properties: "Properties",
}

/**
 * An Iceberg table keeps its partition spec in its own metadata, not as Glue
 * partitions, so it gets the Iceberg tab in place of Partitions.
 */
function tabsFor(table: Table): TableTab[] {
  const skip: TableTab = isIcebergTable(table) ? "partitions" : "iceberg"
  return (Object.keys(TAB_LABELS) as TableTab[]).filter((t) => t !== skip)
}

function Overview({ table }: { table: Table }) {
  const { region } = useEndpoint()
  const arn = tableArn(table, region)
  const location = tableLocation(table)
  return (
    <DefinitionCard>
      <Definition label="ARN" value={<ArnText arn={arn} />} copyable={arn} full />
      <Definition
        label="Database"
        value={
          <Link
            to="/glue/$database"
            params={{ database: table.DatabaseName ?? "" }}
            className={LINK_CLASS}
          >
            {table.DatabaseName}
          </Link>
        }
      />
      <Definition label="Format" value={<FormatBadge table={table} />} />
      <Definition label="Table type" value={table.TableType} />
      <Definition label="Location" value={location && <S3UriLink uri={location} />} full />
      <Definition label="Created" value={formatDate(table.CreateTime)} />
      <Definition label="Updated" value={formatDate(table.UpdateTime)} />
      <Definition label="Version" value={table.VersionId} />
      {table.Description && (
        <Definition label="Description" value={table.Description} variant="prose" full />
      )}
    </DefinitionCard>
  )
}

/** One table: its fields, then a tab for each question a developer asks of it. */
export function TableDetail({ database, name, search, onSearchChange }: TableDetailProps) {
  const query = useQuery(glueTableQueryOptions(database, name))
  const table = query.data

  if (query.error) {
    return (
      <LoadErrorPage title={name} meta={`Table in ${database}`} noun="table" error={query.error} />
    )
  }

  const tabs = table ? tabsFor(table) : []
  const tab = search.tab && tabs.includes(search.tab) ? search.tab : "schema"
  return (
    <ResourceListPage
      title={name}
      meta={`Table in ${database}`}
      actions={
        <>
          <QueryInAthenaButton database={database} table={name} />
          <RefreshAction isFetching={query.isFetching} onClick={() => void query.refetch()} />
        </>
      }
    >
      {table ? <Overview table={table} /> : <SkeletonRows rows={3} noun="table" />}
      {table && (
        <Tabs
          selectedKey={tab}
          onSelectionChange={(next) => onSearchChange({ tab: next as TableTab })}
        >
          <TabList aria-label="Table views">
            {tabs.map((t) => (
              <Tab key={t} id={t}>
                {TAB_LABELS[t]}
              </Tab>
            ))}
          </TabList>
          <div className="pt-4">
            <TabPanel id="schema">
              <SchemaTab table={table} />
            </TabPanel>
            <TabPanel id="partitions">
              <PartitionsTab
                table={table}
                expression={search.q ?? ""}
                onExpressionChange={(q) => onSearchChange({ q: q || undefined })}
              />
            </TabPanel>
            <TabPanel id="data">
              <DataTab table={table} />
            </TabPanel>
            <TabPanel id="iceberg">
              <IcebergTab table={table} />
            </TabPanel>
            <TabPanel id="versions">
              <VersionsTab
                table={table}
                from={search.from}
                to={search.to}
                onCompare={(from, to) => onSearchChange({ from, to })}
              />
            </TabPanel>
            <TabPanel id="properties">
              <PropertiesTab table={table} />
            </TabPanel>
          </div>
        </Tabs>
      )}
    </ResourceListPage>
  )
}
