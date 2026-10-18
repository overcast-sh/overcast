import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Globe, Pencil, Send } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { CreateAction, RefreshAction, RowAction } from "@/components/ui/resource-list-page"
import { ResourceListSection } from "@/components/ui/resource-list-section"
import { ResourceTable } from "@/components/ui/resource-table"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { cn } from "@/lib/utils"
import type { WebEndpoint } from "@/services/api/lambda-web"
import {
  createEndpointMutationOptions,
  deleteEndpointMutationOptions,
  lambdaWebKeys,
  updateEndpointMutationOptions,
  webEndpointsQueryOptions,
  webRevisionsQueryOptions,
} from "../data"
import { describeServes, toCreateEndpoint, toUpdateEndpoint, type EndpointDraft } from "../models"
import { useEndpointEnvironments } from "../use-endpoint-environments"
import { EndpointDetailDialog } from "./endpoint-detail-dialog"
import { EndpointDialog } from "./endpoint-dialog"
import { EndpointDomain } from "./endpoint-domain"
import { StateBadge } from "./state-badge"
import { TrafficSpark } from "./traffic"

interface EndpointsTabProps {
  functionName: string
  homeRegion: string
}

/** The text a limit reads as in the table: `≤ 20 envs · 500 req/s`. */
function limitsText(e: WebEndpoint): string {
  const parts = []
  if (e.scalingConfig?.maxEnvironments !== undefined) {
    parts.push(`≤ ${e.scalingConfig.maxEnvironments} envs`)
  }
  if (e.throttleConfig?.rateLimit !== undefined) parts.push(`${e.throttleConfig.rateLimit} req/s`)
  return parts.join(" · ")
}

/**
 * A function's endpoints: where each is reached, what it serves, how many
 * environments it has, and the settings an update can change. A row opens the
 * endpoint's page of detail, with traffic and a form to send it a request.
 */
export function EndpointsTab({ functionName, homeRegion }: EndpointsTabProps) {
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<WebEndpoint>()
  const [deleteTarget, setDeleteTarget] = useState<WebEndpoint>()
  const [viewingName, setViewingName] = useState<string>()
  const {
    data: endpoints,
    isLoading,
    isFetching,
    error,
    refetch,
  } = useQuery(webEndpointsQueryOptions(functionName))
  const { data: revisions = [] } = useQuery(webRevisionsQueryOptions(functionName))
  const environments = useEndpointEnvironments(functionName, homeRegion)
  const viewing = endpoints?.find((e) => e.endpointName === viewingName)

  const create = useResourceMutation({
    options: createEndpointMutationOptions(),
    invalidateKeys: [lambdaWebKeys.endpoints()],
    successTitle: "Endpoint created",
    successDescription: (_, name) => name,
    errorToast: false,
    onSuccess: () => setCreating(false),
  })
  const update = useResourceMutation({
    options: updateEndpointMutationOptions(),
    invalidateKeys: [lambdaWebKeys.endpoints()],
    successTitle: "Endpoint updated",
    errorToast: false,
    onSuccess: () => setEditing(undefined),
  })
  const remove = useResourceMutation({
    options: deleteEndpointMutationOptions(functionName),
    invalidateKeys: [lambdaWebKeys.endpoints()],
    successTitle: "Endpoint deleted",
    successVariant: "default",
    errorTitle: "Delete failed",
    onSuccess: () => setDeleteTarget(undefined),
  })
  const openCreate = () => {
    create.reset()
    setCreating(true)
  }
  const openEdit = (endpoint: WebEndpoint) => {
    update.reset()
    setEditing(endpoint)
  }

  const submitCreate = (draft: EndpointDraft) =>
    create.mutate(toCreateEndpoint(functionName, draft))
  const submitUpdate = (draft: EndpointDraft) =>
    editing && update.mutate(toUpdateEndpoint(functionName, editing, draft))

  return (
    <ResourceListSection
      actions={
        <>
          <span className="flex-1" />
          <RefreshAction isFetching={isFetching} onClick={() => void refetch()} />
          <CreateAction onClick={openCreate}>Create endpoint</CreateAction>
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: endpoints, isLoading, error }}
        noun="endpoints"
        emptyIcon={Globe}
        emptyTitle="No endpoints yet"
        emptyDescription="An endpoint exposes the function and routes its traffic to one revision, or splits it between two."
        emptyAction={<CreateAction onClick={openCreate}>Create endpoint</CreateAction>}
        errorTitle="Failed to load endpoints"
        defaultSort={{ id: "name", desc: false }}
        rowKey={(e) => e.endpointName ?? ""}
        onRowClick={(e) => setViewingName(e.endpointName)}
        columns={[
          {
            id: "name",
            header: "Name",
            sortValue: (e) => e.endpointName,
            cell: (e) => <span className="font-mono font-medium">{e.endpointName}</span>,
          },
          {
            header: "State",
            prose: true,
            cell: (e) => (
              <span className="flex flex-col items-start gap-1">
                <StateBadge state={e.state} />
                {e.state === "Failed" && e.stateReason && (
                  <span className="text-xs text-danger">{e.stateReason}</span>
                )}
                {e.updateStatus && e.updateStatus !== "Successful" && (
                  <span className="inline-flex flex-wrap items-center gap-1 text-xs text-fg-muted">
                    update <StateBadge state={e.updateStatus} />
                    {e.updateStatusReason && (
                      <span className={cn(e.updateStatus === "Failed" && "text-danger")}>
                        {e.updateStatusReason}
                      </span>
                    )}
                  </span>
                )}
              </span>
            ),
          },
          {
            header: "Type",
            cell: (e) => <Badge variant="outline">{e.endpointType}</Badge>,
          },
          {
            header: "Auth",
            cell: (e) => (
              <Badge variant={e.authType === "IamAuth" ? "accent" : "default"}>
                {e.authType === "IamAuth" ? "IAM" : "App managed"}
              </Badge>
            ),
          },
          {
            header: "Serves",
            cellClassName: "font-mono text-xs whitespace-nowrap",
            cell: (e) => (
              <>
                {e.autoDeploymentMode === "LatestRevision" && (
                  <span className="mr-1.5 text-accent" title="Follows the newest revision">
                    latest
                  </span>
                )}
                {describeServes(e.revisionWeights)}
                {limitsText(e) && (
                  <span className="block text-2xs text-fg-muted">{limitsText(e)}</span>
                )}
              </>
            ),
          },
          {
            header: "Environments",
            cellClassName: "tabular-nums whitespace-nowrap",
            cell: (e) => <EnvironmentCount count={environments?.get(e.endpointName ?? "")} />,
          },
          {
            header: "Traffic",
            cell: (e) => (
              <TrafficSpark functionName={functionName} endpointName={e.endpointName ?? ""} />
            ),
          },
          {
            header: "Address",
            interactive: true,
            cell: (e) => (
              <EndpointDomain domainName={e.domainName} authType={e.authType} region={homeRegion} />
            ),
          },
        ]}
        rowActions={(e) => (
          <>
            <RowAction
              label={`Test ${e.endpointName}`}
              onClick={() => setViewingName(e.endpointName)}
            >
              <Send className="size-3.5" />
            </RowAction>
            <RowAction label={`Edit ${e.endpointName}`} onClick={() => openEdit(e)}>
              <Pencil className="size-3.5" />
            </RowAction>
          </>
        )}
        onDelete={{
          target: deleteTarget,
          onRequest: setDeleteTarget,
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: remove,
          getVars: (e) => e.endpointName ?? "",
          label: (e) => e.endpointName ?? "",
          noun: "endpoint",
          description: (e) => (
            <>
              Delete <strong>{e.endpointName}</strong>? Its domain name is released; the revisions
              it served stay.
            </>
          ),
        }}
      />
      <EndpointDetailDialog
        functionName={functionName}
        endpoint={viewing}
        region={homeRegion}
        environments={viewing ? environments?.get(viewing.endpointName ?? "") : undefined}
        onClose={() => setViewingName(undefined)}
      />
      <EndpointDialog
        open={creating}
        onOpenChange={setCreating}
        functionName={functionName}
        homeRegion={homeRegion}
        revisions={revisions}
        taken={(endpoints ?? []).map((e) => e.endpointName ?? "")}
        pending={create.isPending}
        error={create.error}
        onSubmit={submitCreate}
      />
      <EndpointDialog
        endpoint={editing}
        open={editing !== undefined}
        onOpenChange={(open) => !open && setEditing(undefined)}
        functionName={functionName}
        homeRegion={homeRegion}
        revisions={revisions}
        taken={[]}
        pending={update.isPending}
        error={update.error}
        onSubmit={submitUpdate}
      />
    </ResourceListSection>
  )
}

/** An endpoint's environments: the count, or `scaled to zero`; blank until the topology says. */
function EnvironmentCount({ count }: { count: number | undefined }) {
  if (count === undefined) return <span className="text-fg-subtle">—</span>
  if (count === 0) return <span className="text-fg-muted italic">scaled to zero</span>
  return <span className="text-success">{count}</span>
}
