import { useState, useMemo } from "react"
import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { Workflow } from "lucide-react"
import {
  appsyncApisQueryOptions,
  appsyncKeys,
  deleteApiMutationOptions,
  createApiMutationOptions,
} from "@/features/appsync/data"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import {
  CreateAction,
  RefreshAction,
  ResourceListFilter,
  ResourceListPage,
} from "@/components/ui/resource-list-page"
import { ResourceTable, type ResourceTableSort } from "@/components/ui/resource-table"
import { Badge } from "@/components/ui/badge"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { CreateResourceDialog } from "@/components/create-resource-dialog"
import { formatQuantity } from "@/lib/format"

interface AppSyncPageProps {
  /** Current filter text — owned by the route's `q` search param, see `useFilterSearchParam`. */
  filter: string
  onFilterChange: (value: string) => void
  /** Current table sort — owned by the route's `sort` search param, see `useSortSearchParam`. */
  sort?: ResourceTableSort
  onSortChange?: (next: ResourceTableSort | undefined) => void
}

export function AppSyncPage({ filter, onFilterChange, sort, onSortChange }: AppSyncPageProps) {
  const [showCreate, setShowCreate] = useState(false)
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()
  const navigate = useNavigate()

  const {
    data: apis = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(appsyncApisQueryOptions())

  const [deleteTarget, setDeleteTarget] = useState<(typeof apis)[number]>()

  const deleteMut = useResourceMutation({
    options: deleteApiMutationOptions(),
    invalidateKeys: [appsyncKeys.apis()],
    successTitle: "GraphQL API deleted",
    onSuccess: () => setDeleteTarget(undefined),
  })

  const filtered = useMemo(
    () =>
      filter
        ? apis.filter((a) => (a.name ?? "").toLowerCase().includes(filter.toLowerCase()))
        : apis,
    [apis, filter],
  )

  return (
    <ResourceListPage
      title="AppSync"
      description={formatQuantity(apis.length, "GraphQL API")}
      actions={
        <>
          <ServiceDocsButton
            service="appsync"
            label="AppSync"
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create API</CreateAction>
        </>
      }
    >
      <ResourceListFilter value={filter} onChange={onFilterChange} placeholder="Filter APIs…" />

      <ResourceTable
        query={{ data: filtered, isLoading, error }}
        noun="GraphQL APIs"
        emptyIcon={Workflow}
        emptyTitle="No GraphQL APIs"
        emptyDescription="Create a GraphQL API to get started."
        emptyAction={<CreateAction onClick={() => setShowCreate(true)}>Create API</CreateAction>}
        sort={sort}
        onSortChange={onSortChange}
        isFiltered={!!filter}
        onClearFilter={() => onFilterChange("")}
        rowKey={(api) => api.apiId ?? ""}
        onRowClick={(api) =>
          navigate({ to: "/appsync/$apiId", params: { apiId: api.apiId ?? "" } })
        }
        columns={[
          { header: "Name", sortValue: (api) => api.name, cell: (api) => api.name },
          { header: "API ID", cellClassName: "text-fg-muted", cell: (api) => api.apiId },
          {
            header: "Auth Type",
            cell: (api) => <Badge variant="default">{api.authenticationType}</Badge>,
          },
        ]}
        onDelete={{
          target: deleteTarget,
          onRequest: setDeleteTarget,
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: deleteMut,
          getVars: (api) => api.apiId ?? "",
          label: (api) => api.name ?? "",
          noun: "GraphQL API",
          title: "Delete GraphQL API",
          description: (api) => (
            <>
              Delete <span className="font-mono font-semibold">{api.name}</span>? This cannot be
              undone.
            </>
          ),
        }}
      />

      <CreateResourceDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        title="Create GraphQL API"
        label="API Name"
        placeholder="my-graphql-api"
        mutationOptions={createApiMutationOptions}
        invalidateKeys={[appsyncKeys.apis()]}
        successTitle="GraphQL API created"
      />
    </ResourceListPage>
  )
}
