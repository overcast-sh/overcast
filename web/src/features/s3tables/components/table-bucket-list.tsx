import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { Database } from "lucide-react"
import type { TableBucketSummary } from "@aws-sdk/client-s3tables"
import { ArnText } from "@/components/ui/arn-link"
import { CopyButton } from "@/components/ui/copy-button"
import {
  CreateAction,
  RefreshAction,
  ResourceListFilter,
  ResourceListPage,
  ResourceName,
} from "@/components/ui/resource-list-page"
import { ResourceTable, type ResourceTableSort } from "@/components/ui/resource-table"
import { RawStateLink } from "@/features/debug/raw-state-link"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { formatDate } from "@/lib/format"
import {
  createTableBucketMutationOptions,
  deleteTableBucketMutationOptions,
  s3tablesKeys,
  tableBucketsQueryOptions,
} from "../data"
import { BUCKET_NAME_RULE, bucketNameProblem } from "../names"
import { CreateNameDialog } from "./create-name-dialog"

interface TableBucketListProps {
  filter: string
  onFilterChange: (next: string) => void
  sort?: ResourceTableSort
  onSortChange: (next: ResourceTableSort | undefined) => void
}

/** `/s3tables`: the table buckets, each opening on its tables. */
export function TableBucketList({
  filter,
  onFilterChange,
  sort,
  onSortChange,
}: TableBucketListProps) {
  const navigate = useNavigate()
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()
  const [creating, setCreating] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<TableBucketSummary>()
  const {
    data: buckets = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(tableBucketsQueryOptions())

  const create = useResourceMutation({
    options: createTableBucketMutationOptions(),
    invalidateKeys: [s3tablesKeys.buckets()],
    successTitle: "Table bucket created",
    successDescription: (name) => name,
    onSuccess: (_, name) => {
      setCreating(false)
      void navigate({ to: "/s3tables/$bucket", params: { bucket: name } })
    },
  })
  const remove = useResourceMutation({
    options: deleteTableBucketMutationOptions(),
    invalidateKeys: [s3tablesKeys.buckets()],
    successTitle: "Table bucket deleted",
    successVariant: "default",
    errorTitle: "Delete failed",
    onSuccess: () => setDeleteTarget(undefined),
  })

  const needle = filter.trim().toLowerCase()
  const shown = needle ? buckets.filter((b) => b.name?.includes(needle)) : buckets
  const openCreate = () => {
    create.reset()
    setCreating(true)
  }

  return (
    <ResourceListPage
      title="Table buckets"
      count={buckets.length}
      actions={
        <>
          <ServiceDocsButton
            service="s3tables"
            label="S3 Tables"
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
          <RawStateLink service="s3tables" />
          <RefreshAction isFetching={isFetching} onClick={() => void refetch()} />
          <CreateAction onClick={openCreate}>Create table bucket</CreateAction>
        </>
      }
    >
      <ResourceListFilter
        value={filter}
        onChange={onFilterChange}
        placeholder="Filter table buckets"
      />
      <ResourceTable
        query={{ data: shown, isLoading, error }}
        noun="table buckets"
        emptyIcon={Database}
        emptyTitle="No table buckets yet"
        emptyDescription="A table bucket holds Iceberg tables in namespaces. Create one, then connect PyIceberg, Spark or Trino to it."
        emptyAction={<CreateAction onClick={openCreate}>Create table bucket</CreateAction>}
        errorTitle="Failed to load table buckets"
        isFiltered={needle !== ""}
        onClearFilter={() => onFilterChange("")}
        filteredEmptyTitle="No matching table buckets"
        defaultSort={{ id: "name", desc: false }}
        sort={sort}
        onSortChange={onSortChange}
        rowKey={(b) => b.arn ?? ""}
        onRowClick={(b) =>
          void navigate({ to: "/s3tables/$bucket", params: { bucket: b.name ?? "" } })
        }
        columns={[
          {
            id: "name",
            header: "Name",
            sortValue: (b) => b.name,
            cell: (b) => <ResourceName icon={Database} name={b.name} />,
          },
          {
            header: "ARN",
            interactive: true,
            cell: (b) => (
              <span className="inline-flex items-center gap-1.5">
                <ArnText arn={b.arn ?? ""} />
                <CopyButton value={b.arn ?? ""} noun="ARN" tone="inline" />
              </span>
            ),
          },
          {
            id: "created",
            header: "Created",
            cellClassName: "text-fg-muted",
            sortValue: (b) => b.createdAt,
            cell: (b) => formatDate(b.createdAt),
          },
        ]}
        onDelete={{
          target: deleteTarget,
          onRequest: setDeleteTarget,
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: remove,
          getVars: (b) => b.arn ?? "",
          label: (b) => b.name ?? "",
          noun: "table bucket",
          description: (b) => (
            <>
              Delete <strong>{b.name}</strong>? S3 Tables refuses while the bucket still holds
              namespaces — delete its tables and namespaces first.
            </>
          ),
        }}
      />
      <CreateNameDialog
        open={creating}
        onOpenChange={setCreating}
        icon={<Database />}
        title="Create table bucket"
        description="Iceberg tables live in a table bucket, grouped by namespace."
        label="Name"
        placeholder="analytics"
        rule={BUCKET_NAME_RULE}
        problem={bucketNameProblem}
        pending={create.isPending}
        error={create.error}
        onSubmit={(name) => create.mutate(name)}
      />
    </ResourceListPage>
  )
}
