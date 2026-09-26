import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { ArnText } from "@/components/ui/arn-link"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import {
  CreateAction,
  RefreshAction,
  ResourceListFilter,
  ResourceListPage,
} from "@/components/ui/resource-list-page"
import type { ResourceTableSort } from "@/components/ui/resource-table"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { SkeletonRows } from "@/components/ui/skeleton"
import { useEndpoint } from "@/hooks/use-endpoint"
import { formatDate } from "@/lib/format"
import { databaseArn } from "../arns"
import type { CreateTableWizardState } from "../create-table-param"
import { glueDatabaseQueryOptions, glueTablesQueryOptions } from "../data"
import { CreateTableWizard } from "./create-table/create-table-wizard"
import { LoadErrorPage } from "./load-error-page"
import { QueryInAthenaButton } from "./query-in-athena"
import { TablesTable } from "./tables-table"

interface DatabaseDetailProps {
  name: string
  filter: string
  onFilterChange: (value: string) => void
  sort?: ResourceTableSort
  onSortChange: (next: ResourceTableSort | undefined) => void
  wizard: CreateTableWizardState
}

/** One database: its fields, then its tables — the list a developer came here for. */
export function DatabaseDetail({
  name,
  filter,
  onFilterChange,
  sort,
  onSortChange,
  wizard,
}: DatabaseDetailProps) {
  const navigate = useNavigate()
  const { region } = useEndpoint()
  const database = useQuery(glueDatabaseQueryOptions(name))
  const tables = useQuery(glueTablesQueryOptions(name))
  const needle = filter.trim().toLowerCase()
  const shown = needle
    ? tables.data?.filter((t) => t.Name?.toLowerCase().includes(needle))
    : tables.data

  if (database.error) {
    return <LoadErrorPage title={name} meta="Database" noun="database" error={database.error} />
  }

  const db = database.data
  return (
    <ResourceListPage
      title={name}
      meta="Database"
      count={tables.data?.length}
      actions={
        <>
          <QueryInAthenaButton database={name} />
          <RefreshAction
            isFetching={database.isFetching || tables.isFetching}
            onClick={() => void Promise.all([database.refetch(), tables.refetch()])}
          />
          <CreateAction onClick={() => wizard.onOpen()}>Create table from S3</CreateAction>
        </>
      }
    >
      {db ? (
        <DefinitionCard>
          <Definition label="Name" value={db.Name} copyable />
          <Definition
            label="ARN"
            value={<ArnText arn={databaseArn(db, region)} />}
            copyable={databaseArn(db, region)}
          />
          <Definition label="Created" value={formatDate(db.CreateTime)} />
          <Definition
            label="Location"
            value={db.LocationUri && <S3UriLink uri={db.LocationUri} />}
          />
          <Definition label="Catalog" value={db.CatalogId} />
          {db.Description && (
            <Definition label="Description" value={db.Description} variant="prose" full />
          )}
        </DefinitionCard>
      ) : (
        <SkeletonRows rows={2} noun="database" />
      )}
      <ResourceListFilter value={filter} onChange={onFilterChange} placeholder="Filter tables…" />
      <TablesTable
        database={name}
        query={{ data: shown, isLoading: tables.isLoading, error: tables.error }}
        sort={sort}
        onSortChange={onSortChange}
        isFiltered={needle !== ""}
        onClearFilter={() => onFilterChange("")}
        onRowClick={(t) =>
          void navigate({
            to: "/glue/$database/$table",
            params: { database: name, table: t.Name ?? "" },
          })
        }
        emptyAction={
          <CreateAction onClick={() => wizard.onOpen()}>Create a table from S3 data</CreateAction>
        }
      />
      <CreateTableWizard state={wizard} database={name} />
    </ResourceListPage>
  )
}
