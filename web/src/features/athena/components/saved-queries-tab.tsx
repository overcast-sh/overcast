import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import type { NamedQuery } from "@aws-sdk/client-athena"
import { BookmarkCheck, Pencil, SquareCode } from "lucide-react"
import { FormField } from "@/components/ui/form"
import { HighlightedCode } from "@/components/ui/highlighted-code"
import { Input } from "@/components/ui/input"
import { ResourceFormDialog } from "@/components/ui/resource-form-dialog"
import { RefreshAction, ResourceListFilter, RowAction } from "@/components/ui/resource-list-page"
import { ResourceListSection } from "@/components/ui/resource-list-section"
import { ResourceTable, type ResourceTableSort } from "@/components/ui/resource-table"
import { Textarea } from "@/components/ui/textarea"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import {
  athenaKeys,
  deleteNamedQueryMutationOptions,
  namedQueriesQueryOptions,
  updateNamedQueryMutationOptions,
} from "../data"
import { athenaEditorLink } from "../links"

export interface SavedQueriesTabProps {
  filter: string
  onFilterChange: (value: string) => void
  sort?: ResourceTableSort
  onSortChange: (sort: ResourceTableSort | undefined) => void
}

/** Every workgroup's named queries: open one in the editor, rename it or delete it. */
export function SavedQueriesTab({
  filter,
  onFilterChange,
  sort,
  onSortChange,
}: SavedQueriesTabProps) {
  const { data, isLoading, isFetching, error, refetch } = useQuery(namedQueriesQueryOptions())
  const [editing, setEditing] = useState<NamedQuery>()
  const [deleting, setDeleting] = useState<NamedQuery>()
  const remove = useResourceMutation({
    options: deleteNamedQueryMutationOptions(),
    invalidateKeys: [athenaKeys.namedQueries()],
    successTitle: "Saved query deleted",
    errorTitle: "Could not delete the saved query",
    onSuccess: () => setDeleting(undefined),
  })
  const needle = filter.trim().toLowerCase()
  const shown = needle
    ? (data ?? []).filter((q) =>
        [q.Name, q.Description, q.Database, q.WorkGroup].some((f) =>
          f?.toLowerCase().includes(needle),
        ),
      )
    : data

  return (
    <ResourceListSection
      className="pt-4"
      actions={
        <>
          <ResourceListFilter
            value={filter}
            onChange={onFilterChange}
            placeholder="Filter saved queries…"
            className="flex-1"
          />
          <RefreshAction isFetching={isFetching} onClick={() => void refetch()} />
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: shown, isLoading, error }}
        noun="saved queries"
        emptyIcon={BookmarkCheck}
        emptyTitle="No saved queries"
        emptyDescription="Save a query from the editor to keep it here, in its workgroup."
        emptyAction={
          <Link
            to="/athena"
            search={{ tab: "editor" }}
            className="text-xs text-accent hover:underline"
          >
            Open the editor
          </Link>
        }
        isFiltered={needle !== ""}
        onClearFilter={() => onFilterChange("")}
        filteredEmptyTitle="No matching saved queries"
        rowKey={(q) => q.NamedQueryId ?? ""}
        sort={sort}
        onSortChange={onSortChange}
        defaultSort={{ id: "name", desc: false }}
        expandedContent={(q) => (
          <HighlightedCode
            text={q.QueryString ?? ""}
            language="sql"
            className="max-h-64 overflow-auto rounded-md border border-border bg-bg-muted p-2 text-xs"
          />
        )}
        columns={[
          { id: "name", header: "Name", sortValue: (q) => q.Name, cell: (q) => q.Name },
          { id: "description", header: "Description", prose: true, cell: (q) => q.Description },
          {
            id: "database",
            header: "Database",
            sortValue: (q) => q.Database,
            cell: (q) => q.Database,
          },
          {
            id: "workgroup",
            header: "Workgroup",
            sortValue: (q) => q.WorkGroup,
            cell: (q) => q.WorkGroup,
          },
        ]}
        rowActions={(q) => (
          <>
            <RowAction asChild label={`Open ${q.Name} in the editor`}>
              <Link
                {...athenaEditorLink({
                  database: q.Database,
                  workGroup: q.WorkGroup,
                  sql: q.QueryString ?? "",
                })}
              >
                <SquareCode aria-hidden className="size-3.5" />
              </Link>
            </RowAction>
            <RowAction label={`Edit ${q.Name}`} onClick={() => setEditing(q)}>
              <Pencil aria-hidden className="size-3.5" />
            </RowAction>
          </>
        )}
        onDelete={{
          target: deleting,
          onRequest: setDeleting,
          onOpenChange: (open) => !open && setDeleting(undefined),
          mutation: remove,
          getVars: (q) => q.NamedQueryId ?? "",
          label: (q) => q.Name ?? "",
          noun: "saved query",
        }}
      />
      {editing && <EditSavedQueryDialog query={editing} onClose={() => setEditing(undefined)} />}
    </ResourceListSection>
  )
}

function EditSavedQueryDialog({ query, onClose }: { query: NamedQuery; onClose: () => void }) {
  const [name, setName] = useState(query.Name ?? "")
  const [description, setDescription] = useState(query.Description ?? "")
  const update = useResourceMutation({
    options: updateNamedQueryMutationOptions(),
    invalidateKeys: [athenaKeys.namedQueries()],
    successTitle: "Saved query updated",
    errorTitle: "Could not update the saved query",
    onSuccess: onClose,
  })
  return (
    <ResourceFormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      icon={<Pencil />}
      title="Edit saved query"
      description={`${query.Database} · ${query.WorkGroup}`}
      action="save"
      submitLabel="Save"
      busyLabel="Saving"
      pending={update.isPending}
      error={update.error}
      canSubmit={name.trim() !== ""}
      onSubmit={() =>
        update.mutate({
          NamedQueryId: query.NamedQueryId,
          Name: name.trim(),
          Description: description.trim() || undefined,
          QueryString: query.QueryString,
        })
      }
    >
      <FormField label="Name" required>
        <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} maxLength={128} />
      </FormField>
      <FormField label="Description">
        <Textarea value={description} onChange={(e) => setDescription(e.target.value)} rows={2} />
      </FormField>
    </ResourceFormDialog>
  )
}
