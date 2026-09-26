import { useMemo } from "react"
import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import type { Database } from "@aws-sdk/client-glue"
import { FolderInput, LibraryBig } from "lucide-react"
import {
  CreateAction,
  RefreshAction,
  ResourceListFilter,
  ResourceListPage,
  ResourceName,
} from "@/components/ui/resource-list-page"
import { ResourceTable, type ResourceTableSort } from "@/components/ui/resource-table"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { Skeleton } from "@/components/ui/skeleton"
import { RawStateLink } from "@/features/debug/raw-state-link"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { LoadSampleDatasetAction } from "@/features/samples/load-sample-dataset-action"
import { formatDate } from "@/lib/format"
import type { CreateTableWizardState } from "../create-table-param"
import { glueAllTablesQueryOptions, glueDatabasesQueryOptions } from "../data"
import { CreateTableWizard } from "./create-table/create-table-wizard"

interface GluePageProps {
  /** The filter, owned by the route's `q` search param. */
  filter: string
  onFilterChange: (value: string) => void
  sort?: ResourceTableSort
  onSortChange: (next: ResourceTableSort | undefined) => void
  wizard: CreateTableWizardState
}

/** Tables per database, from the catalog-wide listing. */
function useTableCounts(): Map<string, number> | undefined {
  const { data } = useQuery(glueAllTablesQueryOptions())
  return useMemo(() => {
    if (!data) return undefined
    const counts = new Map<string, number>()
    for (const t of data) {
      const db = t.DatabaseName ?? ""
      counts.set(db, (counts.get(db) ?? 0) + 1)
    }
    return counts
  }, [data])
}

/**
 * The Glue Data Catalog's home: its databases. The first thing a developer
 * wants here is a table over data they have already landed in S3, so the
 * create action is that wizard.
 */
export function GluePage({ filter, onFilterChange, sort, onSortChange, wizard }: GluePageProps) {
  const navigate = useNavigate()
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()
  const { data, isLoading, isFetching, error, refetch } = useQuery(glueDatabasesQueryOptions())
  const counts = useTableCounts()

  const needle = filter.trim().toLowerCase()
  const databases = needle
    ? data?.filter((db) => [db.Name, db.Description].some((v) => v?.toLowerCase().includes(needle)))
    : data

  const createFromS3 = (
    <CreateAction onClick={() => wizard.onOpen()}>Create table from S3</CreateAction>
  )

  return (
    <ResourceListPage
      title="Glue Data Catalog"
      count={data?.length}
      description="Databases and the tables Athena and Iceberg clients read."
      actions={
        <>
          <ServiceDocsButton
            service="glue"
            label="Glue"
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
          <RawStateLink service="glue" />
          <RefreshAction isFetching={isFetching} onClick={() => void refetch()} />
          {createFromS3}
        </>
      }
    >
      <ResourceListFilter
        value={filter}
        onChange={onFilterChange}
        placeholder="Filter databases…"
      />
      <ResourceTable<Database>
        query={{ data: databases, isLoading, error }}
        noun="databases"
        rowKey={(db) => db.Name ?? ""}
        onRowClick={(db) =>
          void navigate({ to: "/glue/$database", params: { database: db.Name ?? "" } })
        }
        sort={sort}
        onSortChange={onSortChange}
        defaultSort={{ id: "name", desc: false }}
        isFiltered={needle !== ""}
        onClearFilter={() => onFilterChange("")}
        emptyIcon={FolderInput}
        emptyTitle="No databases yet"
        emptyDescription="Point the wizard at data already in S3: it infers the schema, finds Hive partitions and creates the database and table. Or load a month of sample orders as CSV and Parquet."
        emptyAction={
          <div className="flex flex-wrap justify-center gap-2">
            <CreateAction onClick={() => wizard.onOpen()}>Create a table from S3 data</CreateAction>
            <LoadSampleDatasetAction
              onLoaded={(report) =>
                void navigate({ to: "/glue/$database", params: { database: report.database } })
              }
            />
          </div>
        }
        columns={[
          {
            id: "name",
            header: "Name",
            sortValue: (db) => db.Name,
            cell: (db) => <ResourceName icon={LibraryBig} name={db.Name} />,
          },
          {
            id: "tables",
            header: "Tables",
            headerClassName: "text-right",
            cellClassName: "text-right tabular-nums",
            sortValue: (db) => counts?.get(db.Name ?? ""),
            cell: (db) =>
              counts ? (counts.get(db.Name ?? "") ?? 0) : <Skeleton className="ml-auto h-3 w-4" />,
          },
          {
            id: "description",
            header: "Description",
            prose: true,
            cell: (db) => db.Description || <span className="text-fg-subtle">—</span>,
          },
          {
            id: "location",
            header: "Location",
            interactive: true,
            cell: (db) =>
              db.LocationUri ? (
                <S3UriLink uri={db.LocationUri} className="whitespace-nowrap" />
              ) : (
                <span className="text-fg-subtle">—</span>
              ),
          },
          {
            id: "created",
            header: "Created",
            sortValue: (db) => db.CreateTime,
            cell: (db) => formatDate(db.CreateTime),
          },
        ]}
      />
      <CreateTableWizard state={wizard} />
    </ResourceListPage>
  )
}
