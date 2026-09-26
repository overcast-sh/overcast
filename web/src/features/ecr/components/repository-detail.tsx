import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { AlertTriangle, Boxes, RefreshCw } from "lucide-react"
import { ecrRepositoryQueryOptions } from "@/features/ecr/data"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { ApplicationOwnershipBanner } from "@/components/application-ownership-banner"
import { Button } from "@/components/ui/button"
import { ResourceTable } from "@/components/ui/resource-table"
import { Badge } from "@/components/ui/badge"
import { CodeBlock, EmptyState, PageHeader, Spinner } from "@/components/ui/primitives"
import { formatDate, formatQuantity } from "@/lib/format"
import { cn } from "@/lib/utils"

export function RepositoryDetail({ repositoryName }: { repositoryName: string }) {
  const navigate = useNavigate()
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()
  const { data, isLoading, isFetching, refetch, error } = useQuery(
    ecrRepositoryQueryOptions(repositoryName),
  )

  const loginCommand = data?.login
    ? `echo '${data.login.password}' | docker login ${data.login.proxyEndpoint} --username ${data.login.username} --password-stdin`
    : undefined

  const pushCommand = data?.uri
    ? [`docker tag registry:2 ${data.uri}:latest`, `docker push ${data.uri}:latest`].join("\n")
    : undefined

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-32">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (!data) {
    return (
      <EmptyState
        icon={<AlertTriangle className="h-8 w-8" />}
        title="Repository not found"
        description={
          error instanceof Error ? error.message : `No repository named ${repositoryName} exists.`
        }
        action={<Button onClick={() => navigate({ to: "/ecr" })}>Back to repositories</Button>}
      />
    )
  }

  return (
    <div className="flex w-full flex-col gap-4">
      <PageHeader
        title={data.name}
        description={data.uri}
        actions={
          <>
            <ServiceDocsButton
              service="ecr"
              label="ECR"
              open={docsOpen}
              onOpen={openDocs}
              onClose={closeDocs}
            />
            <Button variant="ghost" size="icon" onClick={() => refetch()} disabled={isFetching}>
              <RefreshCw className={cn("h-4 w-4", isFetching && "animate-spin")} />
            </Button>
          </>
        }
      />

      <ApplicationOwnershipBanner candidates={[data.arn, data.uri, data.name]} />

      <div className="grid gap-3 md:grid-cols-3">
        <div className="rounded-lg border bg-bg-elevated p-4">
          <div className="font-mono text-xs text-fg-muted">Registry ID</div>
          <div className="mt-1 font-mono text-sm text-fg">{data.registryId}</div>
        </div>
        <div className="rounded-lg border bg-bg-elevated p-4">
          <div className="font-mono text-xs text-fg-muted">Created</div>
          <div className="mt-1 text-sm text-fg">{formatDate(data.createdAt)}</div>
        </div>
        <div className="rounded-lg border bg-bg-elevated p-4">
          <div className="font-mono text-xs text-fg-muted">Tag mutability</div>
          <div className="mt-1">
            <Badge variant="default">{data.imageTagMutability ?? "MUTABLE"}</Badge>
          </div>
        </div>
      </div>

      <section className="rounded-lg border bg-bg-elevated p-4">
        <div className="mb-3 flex items-center gap-2 text-sm font-medium text-fg">
          <Boxes className="h-4 w-4 text-fg-muted" />
          Local registry usage
        </div>
        <p className="mb-3 text-sm text-fg-muted">
          Your Docker daemon must allow this hostname as an insecure HTTP registry before local push
          and pull will work.
        </p>
        {loginCommand && <CodeBlock>{loginCommand}</CodeBlock>}
        {pushCommand && <CodeBlock className="mt-3">{pushCommand}</CodeBlock>}
      </section>

      <section className="rounded-lg border bg-bg-elevated p-4">
        <div className="mb-3 flex items-center justify-between gap-3">
          <div>
            <h2 className="font-mono text-sm font-medium text-fg">Images</h2>
            <p className="text-sm text-fg-muted">
              {formatQuantity(data.images.length, "image entry", "image entries")} tracked for this
              repository.
            </p>
          </div>
        </div>
        <ResourceTable
          variant="embedded"
          query={{ data: data.images, isLoading: false }}
          noun="images"
          emptyIcon={Boxes}
          emptyTitle="No images yet"
          emptyDescription="Push a tag into this repository and refresh to reconcile digest metadata from the local registry."
          rowKey={(image) => `${image.digest}:${image.tags.join(",")}`}
          columns={[
            {
              id: "tags",
              header: "Tags",
              sortValue: (image) => image.tags.join(", "),
              cell: (image) => image.tags.join(", ") || "—",
            },
            { header: "Digest", cellClassName: "text-fg-muted", cell: (image) => image.digest },
            {
              header: "Media type",
              cellClassName: "text-fg-muted",
              cell: (image) => image.mediaType ?? "—",
            },
          ]}
        />
      </section>
    </div>
  )
}
