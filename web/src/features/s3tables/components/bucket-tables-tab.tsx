import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { FolderTree, Plus, Table2, Trash2 } from "lucide-react"
import type { TableSummary } from "@aws-sdk/client-s3tables"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { CopyButton } from "@/components/ui/copy-button"
import { EmptyState, QueryListState, SectionLabel } from "@/components/ui/primitives"
import {
  CreateAction,
  ResourceListCard,
  ResourceListFilter,
  ResourceName,
} from "@/components/ui/resource-list-page"
import { ResourceListSection } from "@/components/ui/resource-list-section"
import { ResourceTable } from "@/components/ui/resource-table"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { formatDate, formatQuantity } from "@/lib/format"
import {
  bucketTablesQueryOptions,
  createNamespaceMutationOptions,
  deleteNamespaceMutationOptions,
  deleteTableMutationOptions,
  namespacesQueryOptions,
  s3tablesKeys,
  tableIdOf,
} from "../data"
import { IDENTIFIER_RULE, identifierProblem } from "../names"
import { CreateNameDialog } from "./create-name-dialog"
import { CreateTableDialog } from "./create-table-dialog"
import { groupByNamespace, type NamespaceGroup } from "../namespace-groups"

interface BucketTablesTabProps {
  bucketName: string
  tableBucketARN: string
  filter: string
  onFilterChange: (next: string) => void
}

/**
 * The bucket's tables, grouped under their namespaces — the way every Iceberg
 * client addresses them (`sales.orders`). Empty namespaces are listed too, so
 * one just created has somewhere to put its first table.
 */
export function BucketTablesTab({
  bucketName,
  tableBucketARN,
  filter,
  onFilterChange,
}: BucketTablesTabProps) {
  const namespaces = useQuery(namespacesQueryOptions(tableBucketARN))
  const tables = useQuery(bucketTablesQueryOptions(tableBucketARN))
  const [creatingNamespace, setCreatingNamespace] = useState(false)
  /** The namespace a new table goes in, or null while the dialog is closed. */
  const [creatingTableIn, setCreatingTableIn] = useState<string | null>(null)

  const createNamespace = useResourceMutation({
    options: createNamespaceMutationOptions(tableBucketARN),
    invalidateKeys: [s3tablesKeys.namespaces()],
    successTitle: "Namespace created",
    successDescription: (name) => name,
    onSuccess: () => setCreatingNamespace(false),
  })

  const loading = namespaces.isLoading || tables.isLoading
  const groups = groupByNamespace(
    (namespaces.data ?? []).map((n) => n.namespace?.join(".") ?? ""),
    tables.data ?? [],
    filter.trim().toLowerCase(),
  )
  const names = (namespaces.data ?? []).map((n) => n.namespace?.join(".") ?? "")
  const openCreateNamespace = () => {
    createNamespace.reset()
    setCreatingNamespace(true)
  }
  const openCreateTable = (namespace = names[0] ?? "") => setCreatingTableIn(namespace)

  return (
    <ResourceListSection
      actions={
        <>
          <ResourceListFilter
            value={filter}
            onChange={onFilterChange}
            placeholder="Filter tables and namespaces"
            className="flex-1"
          />
          <Button size="sm" variant="outline" onClick={openCreateNamespace}>
            <Plus className="size-3.5" />
            Create namespace
          </Button>
          <CreateAction onClick={() => openCreateTable()} disabled={names.length === 0}>
            Create table
          </CreateAction>
        </>
      }
    >
      {(loading || groups.length === 0) && (
        <ResourceListCard>
          <QueryListState
            isLoading={loading}
            error={namespaces.error ?? tables.error}
            errorTitle="Failed to load the bucket's tables"
            isEmpty={groups.length === 0}
            loadingNoun="tables"
            emptyIcon={<FolderTree className="size-8" />}
            emptyTitle="No namespaces yet"
            emptyDescription="Tables live in namespaces. Create one — sales, events, raw — then add a table to it here or from PyIceberg."
            emptyAction={
              <CreateAction onClick={openCreateNamespace}>Create namespace</CreateAction>
            }
            isFiltered={filter.trim() !== ""}
            onClearFilter={() => onFilterChange("")}
            filteredEmptyTitle="No matching tables or namespaces"
          />
        </ResourceListCard>
      )}
      {groups.map((group) => (
        <NamespaceSection
          key={group.namespace}
          bucketName={bucketName}
          tableBucketARN={tableBucketARN}
          group={group}
          onCreateTable={() => openCreateTable(group.namespace)}
        />
      ))}
      <CreateNameDialog
        open={creatingNamespace}
        onOpenChange={setCreatingNamespace}
        icon={<FolderTree />}
        title="Create namespace"
        description={`In table bucket ${bucketName}.`}
        label="Namespace"
        placeholder="sales"
        rule={IDENTIFIER_RULE}
        problem={(name) => identifierProblem(name, "namespace")}
        pending={createNamespace.isPending}
        error={createNamespace.error}
        onSubmit={(name) => createNamespace.mutate(name)}
      />
      <CreateTableDialog
        bucketName={bucketName}
        tableBucketARN={tableBucketARN}
        namespaces={names}
        namespace={creatingTableIn}
        onClose={() => setCreatingTableIn(null)}
      />
    </ResourceListSection>
  )
}

function NamespaceSection({
  bucketName,
  tableBucketARN,
  group,
  onCreateTable,
}: {
  bucketName: string
  tableBucketARN: string
  group: NamespaceGroup
  onCreateTable: () => void
}) {
  const navigate = useNavigate()
  const [deleteTarget, setDeleteTarget] = useState<TableSummary>()
  const [confirmingDrop, setConfirmingDrop] = useState(false)
  const deleteTable = useResourceMutation({
    options: deleteTableMutationOptions(),
    invalidateKeys: [s3tablesKeys.tables()],
    successTitle: "Table deleted",
    successVariant: "default",
    errorTitle: "Delete failed",
    onSuccess: () => setDeleteTarget(undefined),
  })
  const deleteNamespace = useResourceMutation({
    options: deleteNamespaceMutationOptions(tableBucketARN),
    invalidateKeys: [s3tablesKeys.namespaces()],
    successTitle: "Namespace deleted",
    successVariant: "default",
    errorTitle: "Delete failed",
    onSuccess: () => setConfirmingDrop(false),
  })
  const { namespace, tables } = group
  return (
    <ResourceListCard aria-label={`Namespace ${namespace}`}>
      <div className="flex items-center gap-2 border-b border-border px-4 py-2">
        <FolderTree aria-hidden className="size-3.5 text-fg-subtle" />
        <SectionLabel as="h3" className="text-fg">
          {namespace}
        </SectionLabel>
        <span className="font-mono text-2xs text-fg-subtle">
          {formatQuantity(tables.length, "table")}
        </span>
        <span className="ml-auto flex items-center gap-1">
          <Button size="sm" variant="ghost" onClick={onCreateTable}>
            <Plus className="size-3.5" />
            Table
          </Button>
          {tables.length === 0 && (
            <Button
              size="sm"
              variant="ghost"
              title={`Delete namespace ${namespace}`}
              aria-label={`Delete namespace ${namespace}`}
              onClick={() => setConfirmingDrop(true)}
            >
              <Trash2 className="size-3.5" />
            </Button>
          )}
        </span>
      </div>
      {tables.length === 0 ? (
        <EmptyState
          className="py-6"
          title="No tables in this namespace"
          description="Create one here, or with PyIceberg's create_table."
        />
      ) : (
        <ResourceTable
          variant="embedded"
          query={{ data: tables, isLoading: false }}
          noun="tables"
          rowKey={(t) => t.tableARN ?? ""}
          onRowClick={(t) =>
            void navigate({
              to: "/s3tables/$bucket/$tableId",
              params: { bucket: bucketName, tableId: tableIdOf(t.tableARN) },
            })
          }
          columns={[
            {
              header: "Table",
              cell: (t) => <ResourceName icon={Table2} name={t.name} />,
            },
            {
              header: "Modified",
              // Fixed widths, so the columns line up from one namespace's table to the next.
              headerClassName: "w-48",
              cellClassName: "whitespace-nowrap text-fg-muted",
              cell: (t) => formatDate(t.modifiedAt),
            },
            {
              header: "Created",
              headerClassName: "w-48",
              cellClassName: "whitespace-nowrap text-fg-muted",
              cell: (t) => formatDate(t.createdAt),
            },
          ]}
          rowActions={(t) => (
            <CopyButton value={t.tableARN ?? ""} noun={`${t.name ?? "table"} ARN`} />
          )}
          onDelete={{
            target: deleteTarget,
            onRequest: setDeleteTarget,
            onOpenChange: (open) => !open && setDeleteTarget(undefined),
            mutation: deleteTable,
            getVars: (t) => ({ tableBucketARN, namespace, name: t.name ?? "" }),
            label: (t) => `${namespace}.${t.name ?? ""}`,
            noun: "table",
            description: (t) => (
              <>
                Delete <strong>{`${namespace}.${t.name ?? ""}`}</strong>? Its metadata and data
                files stay in the warehouse bucket; the table itself is gone.
              </>
            ),
          }}
        />
      )}
      <ConfirmDialog
        open={confirmingDrop}
        onOpenChange={setConfirmingDrop}
        title="Delete namespace?"
        description={`Delete the empty namespace ${namespace}?`}
        isPending={deleteNamespace.isPending}
        onConfirm={() => deleteNamespace.mutate(namespace)}
      />
    </ResourceListCard>
  )
}
