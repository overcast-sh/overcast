import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import type { WorkGroupSummary } from "@aws-sdk/client-athena"
import { Users } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import {
  CreateAction,
  RefreshAction,
  ResourceListFilter,
  ResourceName,
} from "@/components/ui/resource-list-page"
import { ResourceListSection } from "@/components/ui/resource-list-section"
import { ResourceTable, type ResourceTableSort } from "@/components/ui/resource-table"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { formatDate } from "@/lib/format"
import { athenaKeys, deleteWorkGroupMutationOptions, workGroupsQueryOptions } from "../data"
import { DEFAULT_WORKGROUP } from "../query-tabs"
import { WorkGroupDetail } from "./workgroup-detail"
import { WorkGroupFormDialog } from "./workgroup-form-dialog"

export interface WorkGroupsTabProps {
  filter: string
  onFilterChange: (value: string) => void
  sort?: ResourceTableSort
  onSortChange: (sort: ResourceTableSort | undefined) => void
  /** The workgroup open in detail, from `?workgroup=`. */
  selected?: string
  onSelect: (name: string | undefined) => void
}

/** The workgroups, and one of them in detail. */
export function WorkGroupsTab(props: WorkGroupsTabProps) {
  const { selected, onSelect } = props
  if (selected) return <WorkGroupDetail name={selected} onBack={() => onSelect(undefined)} />
  return <WorkGroupList {...props} />
}

function WorkGroupList({
  filter,
  onFilterChange,
  sort,
  onSortChange,
  onSelect,
}: WorkGroupsTabProps) {
  const { data, isLoading, isFetching, error, refetch } = useQuery(workGroupsQueryOptions())
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<WorkGroupSummary>()
  const remove = useResourceMutation({
    options: deleteWorkGroupMutationOptions(),
    invalidateKeys: [athenaKeys.workGroups(), athenaKeys.namedQueries(), athenaKeys.executions()],
    successTitle: "Workgroup deleted",
    errorTitle: "Could not delete the workgroup",
    onSuccess: () => setDeleting(undefined),
  })
  const needle = filter.trim().toLowerCase()
  const shown = needle
    ? (data ?? []).filter((w) =>
        [w.Name, w.Description].some((f) => f?.toLowerCase().includes(needle)),
      )
    : data
  const create = <CreateAction onClick={() => setCreating(true)}>Create workgroup</CreateAction>

  return (
    <ResourceListSection
      className="pt-4"
      actions={
        <>
          <ResourceListFilter
            value={filter}
            onChange={onFilterChange}
            placeholder="Filter workgroups…"
            className="flex-1"
          />
          <RefreshAction isFetching={isFetching} onClick={() => void refetch()} />
          {create}
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: shown, isLoading, error }}
        noun="workgroups"
        emptyIcon={Users}
        emptyTitle="No workgroups"
        emptyDescription="Create a workgroup to give queries a result location of their own."
        emptyAction={create}
        isFiltered={needle !== ""}
        onClearFilter={() => onFilterChange("")}
        filteredEmptyTitle="No matching workgroups"
        rowKey={(w) => w.Name ?? ""}
        onRowClick={(w) => onSelect(w.Name)}
        sort={sort}
        onSortChange={onSortChange}
        defaultSort={{ id: "name", desc: false }}
        columns={[
          {
            id: "name",
            header: "Name",
            sortValue: (w) => w.Name,
            cell: (w) => <ResourceName icon={Users} name={w.Name} />,
          },
          {
            id: "state",
            header: "State",
            sortValue: (w) => w.State,
            cell: (w) => (
              <Badge variant={w.State === "ENABLED" ? "success" : "default"}>{w.State}</Badge>
            ),
          },
          {
            id: "engine",
            header: "Engine",
            cell: (w) =>
              w.EngineVersion?.EffectiveEngineVersion ?? w.EngineVersion?.SelectedEngineVersion,
          },
          { id: "description", header: "Description", prose: true, cell: (w) => w.Description },
          {
            id: "created",
            header: "Created",
            cellClassName: "text-fg-muted",
            sortValue: (w) => w.CreationTime,
            cell: (w) => formatDate(w.CreationTime),
          },
        ]}
        onDelete={{
          target: deleting,
          onRequest: setDeleting,
          onOpenChange: (open) => !open && setDeleting(undefined),
          mutation: remove,
          getVars: (w) => w.Name ?? "",
          canDelete: (w) => w.Name !== DEFAULT_WORKGROUP,
          label: (w) => w.Name ?? "",
          noun: "workgroup",
          description: (w) => (
            <>
              Delete <span className="font-mono font-semibold">{w.Name}</span>, with its saved
              queries and query history?
            </>
          ),
        }}
      />
      {creating && <WorkGroupFormDialog onClose={() => setCreating(false)} />}
    </ResourceListSection>
  )
}
