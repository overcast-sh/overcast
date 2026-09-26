import { useState } from "react"
import { useForm } from "@tanstack/react-form"
import { useQuery } from "@tanstack/react-query"
import { Boxes } from "lucide-react"
import { z } from "zod"
import {
  createEksClusterMutationOptions,
  eksClustersQueryOptions,
  eksKeys,
} from "@/features/eks/data"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { FormField, fieldError } from "@/components/ui/form"
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Spinner } from "@/components/ui/primitives"
import { CreateAction, RefreshAction, ResourceListPage } from "@/components/ui/resource-list-page"
import { ResourceTable } from "@/components/ui/resource-table"
import { DockerBanner } from "@/components/docker-banner"
import { Badge } from "@/components/ui/badge"
import { formatQuantity } from "@/lib/format"

export function EksPage() {
  const [showCreate, setShowCreate] = useState(false)
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()
  const {
    data: clusters = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(eksClustersQueryOptions())

  const createMut = useResourceMutation({
    options: createEksClusterMutationOptions(),
    invalidateKeys: [eksKeys.clusters()],
    successTitle: "Cluster created",
    successDescription: (name) => name,
    onSuccess: () => setShowCreate(false),
  })

  return (
    <ResourceListPage
      title="EKS Clusters"
      description={formatQuantity(clusters.length, "cluster")}
      actions={
        <>
          <ServiceDocsButton
            service="eks"
            label="EKS"
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create Cluster</CreateAction>
        </>
      }
    >
      <DockerBanner forService="eks" />

      <ResourceTable
        query={{ data: clusters, isLoading, error }}
        noun="clusters"
        emptyIcon={Boxes}
        emptyTitle="No clusters"
        emptyDescription="Create a cluster to start working with EKS resources."
        emptyAction={
          <CreateAction onClick={() => setShowCreate(true)}>Create Cluster</CreateAction>
        }
        rowKey={(c) => c.arn || c.name || ""}
        columns={[
          {
            header: "Name",
            cellClassName: "font-medium",
            sortValue: (c) => c.name,
            cell: (c) => c.name,
          },
          {
            header: "Status",
            cell: (c) => (
              <Badge
                variant={
                  c.status === "ACTIVE"
                    ? "success"
                    : c.status === "CREATING" || c.status === "UPDATING"
                      ? "warning"
                      : c.status === "FAILED" || c.status === "DELETING"
                        ? "danger"
                        : "default"
                }
              >
                {c.status}
              </Badge>
            ),
          },
          {
            header: "Version",
            cellClassName: "text-fg-muted",
            cell: (c) => c.version || "-",
          },
          {
            header: "Endpoint",
            cellClassName: "text-fg-muted",
            cell: (c) => c.endpoint || "-",
          },
        ]}
      />

      <CreateClusterDialog
        open={showCreate}
        onClose={() => setShowCreate(false)}
        isPending={createMut.isPending}
        onSubmit={(name) => createMut.mutate(name)}
      />
    </ResourceListPage>
  )
}

const createSchema = z.object({
  name: z
    .string()
    .min(1, "Name is required")
    .regex(/^[a-zA-Z0-9_-]+$/, "Only letters, numbers, hyphens, and underscores"),
})

function CreateClusterDialog({
  open,
  onClose,
  onSubmit,
  isPending,
}: {
  open: boolean
  onClose: () => void
  onSubmit: (name: string) => void
  isPending: boolean
}) {
  const form = useForm({
    validators: { onChange: createSchema },
    defaultValues: { name: "" },
    onSubmit: ({ value }) => onSubmit(value.name),
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) {
          onClose()
          form.reset()
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create EKS Cluster</DialogTitle>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void form.handleSubmit()
          }}
        >
          <DialogBody>
            <form.Field name="name">
              {(field) => (
                <FormField
                  label="Cluster Name"
                  error={fieldError(field.state.meta.errors, field.state.meta.isTouched)}
                >
                  <Input
                    placeholder="my-eks-cluster"
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                  />
                </FormField>
              )}
            </form.Field>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={isPending}>
              {isPending && <Spinner className="mr-2" />}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
