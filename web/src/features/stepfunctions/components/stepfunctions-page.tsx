import { useState, useMemo } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { Shuffle } from "lucide-react"
import {
  sfnStateMachinesQueryOptions,
  sfnKeys,
  deleteStateMachineMutationOptions,
  createStateMachineMutationOptions,
} from "@/features/stepfunctions/data"
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
import { ArnText } from "@/components/ui/arn-link"
import { formatTimestamp } from "@/features/stepfunctions/format"
import { DefinitionEditorDialog } from "./definition-editor-dialog"
import { formatQuantity } from "@/lib/format"

interface StepFunctionsPageProps {
  /** Current filter text — owned by the route's `q` search param, see `useFilterSearchParam`. */
  filter: string
  onFilterChange: (value: string) => void
  /** Current table sort — owned by the route's `sort` search param, see `useSortSearchParam`. */
  sort?: ResourceTableSort
  onSortChange?: (next: ResourceTableSort | undefined) => void
}

export function StepFunctionsPage({
  filter,
  onFilterChange,
  sort,
  onSortChange,
}: StepFunctionsPageProps) {
  const [showCreate, setShowCreate] = useState(false)
  const navigate = useNavigate()
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()

  const {
    data: machines = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(sfnStateMachinesQueryOptions())

  const [deleteTarget, setDeleteTarget] = useState<(typeof machines)[number]>()

  const deleteMut = useResourceMutation({
    options: deleteStateMachineMutationOptions(),
    invalidateKeys: [sfnKeys.stateMachines()],
    successTitle: "State machine deleted",
    onSuccess: () => setDeleteTarget(undefined),
  })

  const createMut = useResourceMutation({
    options: createStateMachineMutationOptions(),
    invalidateKeys: [sfnKeys.stateMachines()],
    successTitle: "State machine created",
    onSuccess: (_, vars) => {
      setShowCreate(false)
      void navigate({
        to: "/stepfunctions/$name",
        params: { name: vars.name },
        search: { tab: "diagram" },
      })
    },
  })

  const filtered = useMemo(
    () =>
      filter
        ? machines.filter((m) => (m.name ?? "").toLowerCase().includes(filter.toLowerCase()))
        : machines,
    [machines, filter],
  )

  return (
    <ResourceListPage
      title="Step Functions"
      description={formatQuantity(machines.length, "state machine")}
      actions={
        <>
          <ServiceDocsButton
            service="stepfunctions"
            label="Step Functions"
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create State Machine</CreateAction>
        </>
      }
    >
      <ResourceListFilter
        value={filter}
        onChange={onFilterChange}
        placeholder="Filter state machines…"
      />

      <ResourceTable
        query={{ data: filtered, isLoading, error }}
        noun="state machines"
        emptyIcon={Shuffle}
        emptyTitle="No state machines"
        emptyDescription="Create a state machine to get started."
        emptyAction={
          <CreateAction onClick={() => setShowCreate(true)}>Create State Machine</CreateAction>
        }
        sort={sort}
        onSortChange={onSortChange}
        isFiltered={!!filter}
        onClearFilter={() => onFilterChange("")}
        rowKey={(sm) => sm.stateMachineArn ?? ""}
        columns={[
          {
            header: "Name",
            sortValue: (sm) => sm.name,
            cell: (sm) => (
              <Link
                className="font-medium text-accent hover:underline"
                to="/stepfunctions/$name"
                params={{ name: sm.name ?? "" }}
              >
                {sm.name}
              </Link>
            ),
          },
          {
            header: "Type",
            sortValue: (sm) => sm.type,
            cell: (sm) => <Badge variant="default">{sm.type}</Badge>,
          },
          {
            id: "created",
            header: "Created",
            cellClassName: "text-fg-muted",
            sortValue: (sm) => sm.creationDate,
            cell: (sm) => formatTimestamp(sm.creationDate),
          },
          {
            header: "ARN",
            cellClassName: "text-fg-muted",
            cell: (sm) => <ArnText arn={sm.stateMachineArn ?? ""} />,
          },
        ]}
        onDelete={{
          target: deleteTarget,
          onRequest: setDeleteTarget,
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: deleteMut,
          getVars: (sm) => sm.stateMachineArn ?? "",
          label: (sm) => sm.name ?? "",
          noun: "state machine",
          title: "Delete State Machine",
          description: (sm) => (
            <>
              Delete <span className="font-mono font-semibold">{sm.name}</span>? This cannot be
              undone.
            </>
          ),
        }}
      />

      <DefinitionEditorDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        mode="create"
        pending={createMut.isPending}
        onSubmit={(result) => {
          if (result.mode === "create") {
            createMut.mutate({
              name: result.name,
              definition: result.definition,
              type: result.type,
            })
          }
        }}
      />
    </ResourceListPage>
  )
}
