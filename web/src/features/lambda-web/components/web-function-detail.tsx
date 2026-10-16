import { useState } from "react"
import { useIsFetching, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { AppWindow } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { CopyButton } from "@/components/ui/copy-button"
import { EmptyState, PageHeader } from "@/components/ui/primitives"
import { RefreshAction, ResourceListCard } from "@/components/ui/resource-list-page"
import { SkeletonRows } from "@/components/ui/skeleton"
import { Tab, TabList, TabPanel, Tabs } from "@/components/ui/tabs"
import { RawStateLink } from "@/features/debug/raw-state-link"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { formatDate } from "@/lib/format"
import { isResourceNotFound } from "@/lib/aws-error"
import { deleteWebFunctionMutationOptions, lambdaWebKeys, webFunctionQueryOptions } from "../data"
import type { WebFunctionTab } from "../data"
import { EndpointsTab } from "./endpoints-tab"
import { OverviewTab } from "./overview-tab"
import { PolicyTab } from "./policy-tab"
import { RevisionsTab } from "./revisions-tab"

interface WebFunctionDetailProps {
  name: string
  tab: WebFunctionTab
  onTabChange: (tab: WebFunctionTab) => void
}

const PAGE = "flex w-full flex-col gap-4"

/** `/lambda-web/$name`: a web function's overview, revisions, endpoints and resource policy. */
export function WebFunctionDetail({ name, tab, onTabChange }: WebFunctionDetailProps) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [confirmingDelete, setConfirmingDelete] = useState(false)
  // Refresh covers the whole page — the function, its revisions and endpoints,
  // the policy and tags — so it shows busy while any of them refetches.
  const refreshing = useIsFetching({ queryKey: lambdaWebKeys.all() }) > 0
  const { data: fn, isLoading, error } = useQuery(webFunctionQueryOptions(name))
  const remove = useResourceMutation({
    options: deleteWebFunctionMutationOptions(),
    invalidateKeys: [lambdaWebKeys.all()],
    successTitle: "Web function deleted",
    successVariant: "default",
    errorTitle: "Delete failed",
    onSuccess: () => {
      setConfirmingDelete(false)
      void navigate({ to: "/lambda-web" })
    },
  })

  if (isLoading) {
    return (
      <div className={PAGE}>
        <PageHeader title={name} />
        <ResourceListCard>
          <SkeletonRows rows={6} />
        </ResourceListCard>
      </div>
    )
  }
  if (!fn?.functionArn) {
    return (
      <div className={PAGE}>
        <PageHeader title={name} />
        <ResourceListCard>
          <EmptyState
            icon={<AppWindow className="size-8" />}
            title={
              error && !isResourceNotFound(error)
                ? "Could not load the web function"
                : "No such web function"
            }
            description={
              error && !isResourceNotFound(error)
                ? error.message
                : `There is no web function called ${name} in this region.`
            }
            action={
              <Button size="sm" variant="outline" asChild>
                <Link to="/lambda-web">All web functions</Link>
              </Button>
            }
          />
        </ResourceListCard>
      </div>
    )
  }
  const arn = fn.functionArn
  const region = arn.split(":")[3] ?? ""
  return (
    <div className={PAGE}>
      <PageHeader
        title={name}
        meta={
          <span className="inline-flex flex-wrap items-center gap-x-1.5">
            web function · {arn}
            <CopyButton value={arn} noun="web function ARN" tone="inline" />· created{" "}
            {formatDate(fn.createdAt)}
          </span>
        }
        actions={
          <>
            <RawStateLink service="lambda-web" />
            <RefreshAction
              isFetching={refreshing}
              onClick={() => void queryClient.invalidateQueries({ queryKey: lambdaWebKeys.all() })}
            />
            <Button size="sm" variant="danger-ghost" onClick={() => setConfirmingDelete(true)}>
              Delete
            </Button>
          </>
        }
      />
      <Tabs selectedKey={tab} onSelectionChange={(key) => onTabChange(key as WebFunctionTab)}>
        <TabList aria-label="Web function views">
          <Tab id="overview">Overview</Tab>
          <Tab id="revisions">Revisions</Tab>
          <Tab id="endpoints">Endpoints</Tab>
          <Tab id="policy">Policy</Tab>
        </TabList>
        <TabPanel id="overview" className="pt-4">
          <OverviewTab fn={fn} />
        </TabPanel>
        <TabPanel id="revisions" className="pt-4">
          <RevisionsTab functionName={name} />
        </TabPanel>
        <TabPanel id="endpoints" className="pt-4">
          <EndpointsTab functionName={name} homeRegion={region} />
        </TabPanel>
        <TabPanel id="policy" className="pt-4">
          <PolicyTab functionArn={arn} />
        </TabPanel>
      </Tabs>
      <ConfirmDialog
        open={confirmingDelete}
        onOpenChange={setConfirmingDelete}
        title="Delete web function?"
        description={
          <>
            Delete <strong>{name}</strong> with all its revisions and endpoints?
          </>
        }
        isPending={remove.isPending}
        onConfirm={() => remove.mutate(name)}
      />
    </div>
  )
}
