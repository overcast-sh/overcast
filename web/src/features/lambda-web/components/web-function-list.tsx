import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { AppWindow } from "lucide-react"
import type { WebFunction } from "@/services/api/lambda-web"
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
  createWebFunctionMutationOptions,
  deleteWebFunctionMutationOptions,
  lambdaWebKeys,
  webEndpointsQueryOptions,
  webFunctionsQueryOptions,
  webRevisionsQueryOptions,
} from "../data"
import { CreateFunctionDialog } from "./create-function-dialog"
import { StateBadge } from "./state-badge"

interface WebFunctionListProps {
  filter: string
  onFilterChange: (next: string) => void
  sort?: ResourceTableSort
  onSortChange: (next: ResourceTableSort | undefined) => void
}

/** `/lambda-web`: the web functions, each opening on its revisions and endpoints. */
export function WebFunctionList({
  filter,
  onFilterChange,
  sort,
  onSortChange,
}: WebFunctionListProps) {
  const navigate = useNavigate()
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()
  const [creating, setCreating] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<WebFunction>()
  const {
    data: functions = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(webFunctionsQueryOptions())

  const create = useResourceMutation({
    options: createWebFunctionMutationOptions(),
    invalidateKeys: [lambdaWebKeys.all()],
    successTitle: "Web function created",
    successDescription: (input) => input.functionName ?? "",
    errorToast: false,
    onSuccess: (_, input) => {
      setCreating(false)
      void navigate({ to: "/lambda-web/$name", params: { name: input.functionName ?? "" } })
    },
  })
  const remove = useResourceMutation({
    options: deleteWebFunctionMutationOptions(),
    invalidateKeys: [lambdaWebKeys.all()],
    successTitle: "Web function deleted",
    successVariant: "default",
    errorTitle: "Delete failed",
    onSuccess: () => setDeleteTarget(undefined),
  })

  const needle = filter.trim().toLowerCase()
  const shown = needle
    ? functions.filter((f) => f.functionName?.toLowerCase().includes(needle))
    : functions
  const openCreate = () => {
    create.reset()
    setCreating(true)
  }

  return (
    <ResourceListPage
      title="Web functions"
      count={functions.length}
      actions={
        <>
          <ServiceDocsButton
            service="lambda-web"
            label="Lambda Web Functions"
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
          <RawStateLink service="lambda-web" />
          <RefreshAction isFetching={isFetching} onClick={() => void refetch()} />
          <CreateAction onClick={openCreate}>Create web function</CreateAction>
        </>
      }
    >
      <ResourceListFilter
        value={filter}
        onChange={onFilterChange}
        placeholder="Filter web functions"
      />
      <ResourceTable
        query={{ data: shown, isLoading, error }}
        noun="web functions"
        emptyIcon={AppWindow}
        emptyTitle="No web functions yet"
        emptyDescription="A web function is an HTTP server with immutable revisions and the endpoints that serve them. Create one, then add a revision and an endpoint."
        emptyAction={<CreateAction onClick={openCreate}>Create web function</CreateAction>}
        errorTitle="Failed to load web functions"
        isFiltered={needle !== ""}
        onClearFilter={() => onFilterChange("")}
        filteredEmptyTitle="No matching web functions"
        defaultSort={{ id: "name", desc: false }}
        sort={sort}
        onSortChange={onSortChange}
        rowKey={(f) => f.functionArn ?? f.functionName ?? ""}
        onRowClick={(f) =>
          void navigate({ to: "/lambda-web/$name", params: { name: f.functionName ?? "" } })
        }
        columns={[
          {
            id: "name",
            header: "Name",
            sortValue: (f) => f.functionName,
            cell: (f) => <ResourceName icon={AppWindow} name={f.functionName} />,
          },
          { header: "State", cell: (f) => <StateBadge state={f.state} /> },
          {
            header: "Endpoints",
            cellClassName: "text-fg-muted tabular-nums",
            cell: (f) => <EndpointCount name={f.functionName ?? ""} />,
          },
          {
            header: "Revisions",
            cellClassName: "text-fg-muted tabular-nums",
            cell: (f) => <RevisionCount name={f.functionName ?? ""} />,
          },
          {
            header: "ARN",
            interactive: true,
            cell: (f) => (
              <span className="inline-flex items-center gap-1.5">
                <ArnText arn={f.functionArn ?? ""} />
                <CopyButton value={f.functionArn ?? ""} noun="ARN" tone="inline" />
              </span>
            ),
          },
          {
            id: "created",
            header: "Created",
            cellClassName: "text-fg-muted",
            sortValue: (f) => f.createdAt,
            cell: (f) => formatDate(f.createdAt),
          },
        ]}
        onDelete={{
          target: deleteTarget,
          onRequest: setDeleteTarget,
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: remove,
          getVars: (f) => f.functionName ?? "",
          label: (f) => f.functionName ?? "",
          noun: "web function",
          description: (f) => (
            <>
              Delete <strong>{f.functionName}</strong> with all its revisions and endpoints?
            </>
          ),
        }}
      />
      <CreateFunctionDialog
        open={creating}
        onOpenChange={setCreating}
        pending={create.isPending}
        error={create.error}
        onSubmit={(input) => create.mutate(input)}
      />
    </ResourceListPage>
  )
}

function EndpointCount({ name }: { name: string }) {
  const { data } = useQuery(webEndpointsQueryOptions(name))
  return <>{data?.length ?? "—"}</>
}

function RevisionCount({ name }: { name: string }) {
  const { data } = useQuery(webRevisionsQueryOptions(name))
  return <>{data?.length ?? "—"}</>
}
