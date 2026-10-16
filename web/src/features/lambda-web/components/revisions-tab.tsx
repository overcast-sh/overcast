import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Layers } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { CopyButton } from "@/components/ui/copy-button"
import { CreateAction, RefreshAction } from "@/components/ui/resource-list-page"
import { ResourceListSection } from "@/components/ui/resource-list-section"
import { ResourceTable } from "@/components/ui/resource-table"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { formatDate } from "@/lib/format"
import type { WebRevision } from "@/services/api/lambda-web"
import {
  createRevisionMutationOptions,
  deleteRevisionMutationOptions,
  lambdaWebKeys,
  webEndpointsQueryOptions,
  webRevisionsQueryOptions,
} from "../data"
import { endpointsServing } from "../models"
import { CreateRevisionDialog } from "./create-revision-dialog"
import { RevisionDetailDialog } from "./revision-detail-dialog"
import { StateBadge } from "./state-badge"

/** A function's revisions: create the next, read one's configuration, delete one nothing serves. */
export function RevisionsTab({ functionName }: { functionName: string }) {
  const [creating, setCreating] = useState(false)
  const [viewing, setViewing] = useState<string>()
  const [deleteTarget, setDeleteTarget] = useState<WebRevision>()
  const {
    data: revisions,
    isLoading,
    isFetching,
    error,
    refetch,
  } = useQuery(webRevisionsQueryOptions(functionName))
  const { data: endpoints = [] } = useQuery(webEndpointsQueryOptions(functionName))

  const create = useResourceMutation({
    options: createRevisionMutationOptions(),
    invalidateKeys: [lambdaWebKeys.revisions(), lambdaWebKeys.endpoints()],
    successTitle: "Revision created",
    successDescription: (_, id) => id,
    errorToast: false,
    onSuccess: () => setCreating(false),
  })
  // The service refuses to delete a revision an endpoint serves; the dialog stays
  // open and says so, beside the button that asked.
  const remove = useResourceMutation({
    options: deleteRevisionMutationOptions(functionName),
    invalidateKeys: [lambdaWebKeys.revisions(), lambdaWebKeys.endpoints()],
    successTitle: "Revision deleted",
    successVariant: "default",
    errorToast: false,
    onSuccess: () => setDeleteTarget(undefined),
  })
  const openCreate = () => {
    create.reset()
    setCreating(true)
  }
  const requestDelete = (revision: WebRevision) => {
    remove.reset()
    setDeleteTarget(revision)
  }

  return (
    <ResourceListSection
      actions={
        <>
          <span className="flex-1" />
          <RefreshAction isFetching={isFetching} onClick={() => void refetch()} />
          <CreateAction onClick={openCreate}>Create revision</CreateAction>
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: revisions, isLoading, error }}
        noun="revisions"
        emptyIcon={Layers}
        emptyTitle="No revisions yet"
        emptyDescription="A revision is a package in S3 plus its configuration. An endpoint serves revisions, so create one first."
        emptyAction={<CreateAction onClick={openCreate}>Create revision</CreateAction>}
        errorTitle="Failed to load revisions"
        defaultSort={{ id: "created", desc: true }}
        rowKey={(r) => r.revisionId ?? ""}
        onRowClick={(r) => setViewing(r.revisionId)}
        columns={[
          {
            id: "revision",
            header: "Revision",
            sortValue: (r) => r.revisionId,
            interactive: true,
            cell: (r) => (
              <span className="inline-flex items-center gap-1.5 font-mono font-medium">
                {r.revisionId}
                <CopyButton value={r.revisionId ?? ""} noun="revision ID" tone="inline" />
              </span>
            ),
          },
          { header: "State", cell: (r) => <StateBadge state={r.state} /> },
          {
            header: "Serving",
            cell: (r) => {
              const serving = endpointsServing(r.revisionId ?? "", endpoints)
              return serving.length === 0 ? (
                <span className="text-fg-subtle">—</span>
              ) : (
                <span className="flex flex-wrap gap-1">
                  {serving.map((name) => (
                    <Badge key={name} variant="accent">
                      {name}
                    </Badge>
                  ))}
                </span>
              )
            },
          },
          {
            header: "Description",
            prose: true,
            cellClassName: "text-fg-muted",
            cell: (r) => r.description || "—",
          },
          {
            id: "created",
            header: "Created",
            cellClassName: "text-fg-muted",
            sortValue: (r) => r.createdAt,
            cell: (r) => formatDate(r.createdAt),
          },
        ]}
        onDelete={{
          target: deleteTarget,
          onRequest: requestDelete,
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: remove,
          getVars: (r) => r.revisionId ?? "",
          label: (r) => r.revisionId ?? "",
          noun: "revision",
          description: (r) => {
            const serving = endpointsServing(r.revisionId ?? "", endpoints)
            return (
              <>
                <span>
                  Delete revision <strong>{r.revisionId}</strong>? Revisions are immutable and
                  cannot be recovered.
                </span>
                {serving.length > 0 && (
                  <span className="mt-2 block text-warning">
                    It is serving traffic on {serving.join(", ")}. Lambda refuses to delete a
                    revision an endpoint serves; route the endpoint elsewhere first.
                  </span>
                )}
                {remove.error && (
                  <span role="alert" className="mt-2 block text-danger">
                    {remove.error.message}
                  </span>
                )}
              </>
            )
          },
        }}
      />
      <CreateRevisionDialog
        open={creating}
        onOpenChange={setCreating}
        functionName={functionName}
        pending={create.isPending}
        error={create.error}
        onSubmit={(input) => create.mutate(input)}
      />
      <RevisionDetailDialog
        functionName={functionName}
        revisionId={viewing}
        onClose={() => setViewing(undefined)}
      />
    </ResourceListSection>
  )
}
