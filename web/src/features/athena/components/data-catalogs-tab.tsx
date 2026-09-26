import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import type { DataCatalogSummary } from "@aws-sdk/client-athena"
import { Library } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { FormField } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { ResourceFormDialog } from "@/components/ui/resource-form-dialog"
import {
  CreateAction,
  RefreshAction,
  ResourceListFilter,
  ResourceName,
} from "@/components/ui/resource-list-page"
import { ResourceListSection } from "@/components/ui/resource-list-section"
import { ResourceTable, type ResourceTableSort } from "@/components/ui/resource-table"
import { Select } from "@/components/ui/select"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import {
  athenaKeys,
  createDataCatalogMutationOptions,
  dataCatalogsQueryOptions,
  deleteDataCatalogMutationOptions,
} from "../data"
import { DEFAULT_CATALOG } from "../query-tabs"

export interface DataCatalogsTabProps {
  filter: string
  onFilterChange: (value: string) => void
  sort?: ResourceTableSort
  onSortChange: (sort: ResourceTableSort | undefined) => void
}

/**
 * The data catalogs queries can name: the built-in `AwsDataCatalog`, and
 * any registered ones. Only this account's Glue catalogs are readable in
 * Overcast, which the list says against each other kind.
 */
export function DataCatalogsTab({
  filter,
  onFilterChange,
  sort,
  onSortChange,
}: DataCatalogsTabProps) {
  const { data, isLoading, isFetching, error, refetch } = useQuery(dataCatalogsQueryOptions())
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<DataCatalogSummary>()
  const remove = useResourceMutation({
    options: deleteDataCatalogMutationOptions(),
    invalidateKeys: [athenaKeys.dataCatalogs()],
    successTitle: "Data catalog deleted",
    errorTitle: "Could not delete the data catalog",
    onSuccess: () => setDeleting(undefined),
  })
  const needle = filter.trim().toLowerCase()
  const shown = needle
    ? (data ?? []).filter((c) => c.CatalogName?.toLowerCase().includes(needle))
    : data
  const create = <CreateAction onClick={() => setCreating(true)}>Register catalog</CreateAction>

  return (
    <ResourceListSection
      className="pt-4"
      actions={
        <>
          <ResourceListFilter
            value={filter}
            onChange={onFilterChange}
            placeholder="Filter data catalogs…"
            className="flex-1"
          />
          <RefreshAction isFetching={isFetching} onClick={() => void refetch()} />
          {create}
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: shown, isLoading, error }}
        noun="data catalogs"
        emptyIcon={Library}
        emptyTitle="No data catalogs"
        emptyAction={create}
        isFiltered={needle !== ""}
        onClearFilter={() => onFilterChange("")}
        filteredEmptyTitle="No matching data catalogs"
        rowKey={(c) => c.CatalogName ?? ""}
        sort={sort}
        onSortChange={onSortChange}
        defaultSort={{ id: "name", desc: false }}
        columns={[
          {
            id: "name",
            header: "Name",
            sortValue: (c) => c.CatalogName,
            cell: (c) => <ResourceName icon={Library} name={c.CatalogName} />,
          },
          {
            id: "type",
            header: "Type",
            sortValue: (c) => c.Type,
            cell: (c) => <Badge variant="outline">{c.Type}</Badge>,
          },
          {
            id: "queryable",
            header: "In Overcast",
            prose: true,
            cell: (c) =>
              c.CatalogName === DEFAULT_CATALOG
                ? "Queryable"
                : c.Type === "GLUE"
                  ? "Queries read this account's catalog"
                  : "Registered only: its connector is not emulated",
          },
        ]}
        onDelete={{
          target: deleting,
          onRequest: setDeleting,
          onOpenChange: (open) => !open && setDeleting(undefined),
          mutation: remove,
          getVars: (c) => c.CatalogName ?? "",
          canDelete: (c) => c.CatalogName !== DEFAULT_CATALOG,
          label: (c) => c.CatalogName ?? "",
          noun: "data catalog",
        }}
      />
      {creating && <DataCatalogDialog onClose={() => setCreating(false)} />}
    </ResourceListSection>
  )
}

/** The parameter each catalog type needs, as `CreateDataCatalog` names it. */
const PARAMETER: Record<"GLUE" | "LAMBDA" | "HIVE", { key: string; hint: string }> = {
  GLUE: { key: "catalog-id", hint: "The account whose Glue Data Catalog this is." },
  LAMBDA: { key: "function", hint: "The ARN of the connector's Lambda function." },
  HIVE: { key: "metadata-function", hint: "The ARN of the Hive metastore's Lambda function." },
}

function DataCatalogDialog({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("")
  const [type, setType] = useState<keyof typeof PARAMETER>("GLUE")
  const [value, setValue] = useState("")
  const create = useResourceMutation({
    options: createDataCatalogMutationOptions(),
    invalidateKeys: [athenaKeys.dataCatalogs()],
    successTitle: "Data catalog registered",
    errorTitle: "Could not register the data catalog",
    onSuccess: onClose,
  })
  const parameter = PARAMETER[type]
  return (
    <ResourceFormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      icon={<Library />}
      title="Register data catalog"
      action="register"
      submitLabel="Register"
      busyLabel="Registering"
      pending={create.isPending}
      error={create.error}
      canSubmit={name.trim() !== "" && value.trim() !== ""}
      onSubmit={() =>
        create.mutate({
          Name: name.trim(),
          Type: type,
          Parameters: { [parameter.key]: value.trim() },
        })
      }
    >
      <FormField label="Name" required>
        <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label="Type">
        <Select value={type} onChange={(e) => setType(e.target.value as keyof typeof PARAMETER)}>
          {Object.keys(PARAMETER).map((t) => (
            <option key={t}>{t}</option>
          ))}
        </Select>
      </FormField>
      <FormField label={parameter.key} hint={parameter.hint} required>
        <Input value={value} onChange={(e) => setValue(e.target.value)} />
      </FormField>
    </ResourceFormDialog>
  )
}
