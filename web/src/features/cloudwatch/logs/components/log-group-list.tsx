import { useState } from "react"
import { useForm } from "@tanstack/react-form"
import { z } from "zod"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { Eye, FileText, Plus, Trash2 } from "lucide-react"
import {
  logsGroupsQueryOptions,
  logsKeys,
  createLogGroupMutationOptions,
  deleteLogGroupMutationOptions,
} from "@/features/cloudwatch/logs/data"
import { LOG_RETENTION_DAYS, retentionLabel } from "@/features/cloudwatch/logs/retention"
import { logs } from "@/services/api"
import type { CreateLogGroupInput } from "@/services/api/logs"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select } from "@/components/ui/select"
import { FormField, FormRow, fieldError } from "@/components/ui/form"
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Spinner } from "@/components/ui/primitives"
import {
  CreateAction,
  RefreshAction,
  ResourceListPage,
  ResourceName,
  RowAction,
  SelectCheckbox,
} from "@/components/ui/resource-list-page"
import { ResourceTable, type ResourceTableSort } from "@/components/ui/resource-table"
import { useToast } from "@/components/ui/toast"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { formatLogDate } from "@/lib/log-format"
import { formatQuantity } from "@/lib/format"

interface LogGroupListProps {
  /** Current table sort — owned by the route's `sort` search param, see `useSortSearchParam`. */
  sort?: ResourceTableSort
  onSortChange?: (next: ResourceTableSort | undefined) => void
}

export function LogGroupList({ sort, onSortChange }: LogGroupListProps = {}) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast } = useToast()

  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string>()
  const [selectedGroups, setSelectedGroups] = useState<Set<string>>(new Set())
  const [showBulkDelete, setShowBulkDelete] = useState(false)
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()

  const {
    data: groups = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(logsGroupsQueryOptions())

  const createMut = useMutation({
    ...createLogGroupMutationOptions(),
    onSuccess: (_, input) => {
      void qc.invalidateQueries({ queryKey: logsKeys.groups() })
      setShowCreate(false)
      toast({ title: "Log group created", description: input.name, variant: "success" })
    },
    onError: (err: Error) =>
      toast({ title: "Create failed", description: err.message, variant: "danger" }),
  })

  const deleteMut = useMutation({
    ...deleteLogGroupMutationOptions(),
    onSuccess: (_, name) => {
      void qc.invalidateQueries({ queryKey: logsKeys.groups() })
      setDeleteTarget(undefined)
      toast({ title: "Log group deleted", description: name })
    },
    onError: (err: Error) =>
      toast({ title: "Delete failed", description: err.message, variant: "danger" }),
  })

  const bulkDeleteMut = useMutation({
    mutationFn: async (names: string[]) => {
      await Promise.all(names.map((n) => logs.deleteGroup(n)))
    },
    onSuccess: (_, names) => {
      void qc.invalidateQueries({ queryKey: logsKeys.groups() })
      setSelectedGroups(new Set())
      setShowBulkDelete(false)
      toast({
        title: `${formatQuantity(names.length, "log group")} deleted`,
        variant: "success",
      })
    },
    onError: (err: Error) =>
      toast({ title: "Bulk delete failed", description: err.message, variant: "danger" }),
  })

  return (
    <ResourceListPage
      title="CloudWatch Log Groups"
      count={groups.length}
      actions={
        <>
          <ServiceDocsButton
            service="cloudwatch-logs"
            label="CloudWatch Logs"
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create log group</CreateAction>
        </>
      }
    >
      {selectedGroups.size > 0 && (
        <div className="flex items-center gap-3 rounded-card border border-border bg-bg-muted px-3 py-2">
          <span className="text-sm font-medium">
            {formatQuantity(selectedGroups.size, "group")} selected
          </span>
          <Button size="sm" variant="danger" onClick={() => setShowBulkDelete(true)}>
            <Trash2 className="h-3.5 w-3.5" />
            Delete selected
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setSelectedGroups(new Set())}>
            Clear
          </Button>
        </div>
      )}

      <ResourceTable
        query={{ data: groups, isLoading, error }}
        noun="log groups"
        emptyIcon={FileText}
        emptyTitle="No log groups yet"
        emptyDescription="Create a log group to start collecting logs."
        emptyAction={
          <CreateAction onClick={() => setShowCreate(true)}>Create log group</CreateAction>
        }
        errorTitle="Failed to load log groups"
        sort={sort}
        onSortChange={onSortChange}
        rowKey={(g) => g.logGroupName ?? ""}
        onRowClick={(g) =>
          navigate({ to: "/cloudwatch/logs/group", search: { groupName: g.logGroupName ?? "" } })
        }
        columns={[
          {
            // Selection is not a TanStack feature here — `rowSelectionFeature`
            // is deliberately unregistered (see `resource-table.tsx`) — so the
            // checkbox is an ordinary leading column whose cell stops the click
            // from reaching the row's navigate handler. Being column 0 already
            // makes it unhideable.
            id: "select",
            headerClassName: "w-10 pr-0",
            cellClassName: "pr-0",
            header: (
              <SelectCheckbox
                label="Select all log groups"
                checked={selectedGroups.size === groups.length}
                indeterminate={selectedGroups.size > 0 && selectedGroups.size < groups.length}
                onCheckedChange={(checked) =>
                  setSelectedGroups(
                    checked ? new Set(groups.map((g) => g.logGroupName ?? "")) : new Set(),
                  )
                }
              />
            ),
            cell: (g) => {
              const groupName = g.logGroupName ?? ""
              return (
                <span className="contents" onClick={(e) => e.stopPropagation()}>
                  <SelectCheckbox
                    label={`Select ${groupName}`}
                    checked={selectedGroups.has(groupName)}
                    onCheckedChange={(checked) => {
                      const next = new Set(selectedGroups)
                      if (checked) next.add(groupName)
                      else next.delete(groupName)
                      setSelectedGroups(next)
                    }}
                  />
                </span>
              )
            },
          },
          {
            id: "name",
            header: "Log group name",
            // The identity column, even though the checkbox pushes it off
            // index 0 — hiding it would leave rows with nothing to read them by.
            hideable: false,
            sortValue: (g) => g.logGroupName,
            // The `title` sits on the name itself rather than the cell: it is
            // the truncating text a hover tooltip is for, and `ResourceTable`
            // owns the `<td>`.
            cell: (g) => (
              <ResourceName
                icon={FileText}
                name={<span title={g.logGroupName}>{g.logGroupName}</span>}
              />
            ),
          },
          {
            id: "created",
            header: "Created",
            cellClassName: "text-fg-muted",
            sortValue: (g) => g.creationTime,
            cell: (g) => formatLogDate(g.creationTime),
          },
          {
            id: "retention",
            header: "Retention",
            cellClassName: "text-fg-muted",
            cell: (g) => (g.retentionInDays ? retentionLabel(g.retentionInDays) : "Never expire"),
          },
          {
            id: "arn",
            header: "ARN",
            cellClassName: "max-w-xs truncate text-fg-muted",
            cell: (g) => <span title={g.arn}>{g.arn}</span>,
          },
        ]}
        rowActions={(g) => {
          const groupName = g.logGroupName ?? ""
          return (
            <>
              <RowAction
                label={`View ${groupName}`}
                onClick={() => navigate({ to: "/cloudwatch/logs/group", search: { groupName } })}
              >
                <Eye className="h-3.5 w-3.5" />
              </RowAction>
              <RowAction
                label={`Delete ${groupName}`}
                tone="danger"
                onClick={() => setDeleteTarget(groupName)}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </RowAction>
            </>
          )
        }}
      />

      {/* ── Create log group dialog ── */}
      <CreateLogGroupDialog
        open={showCreate}
        onClose={() => setShowCreate(false)}
        isPending={createMut.isPending}
        onSubmit={(input) => createMut.mutate(input)}
      />

      {/* ── Delete confirm dialog ── */}
      <Dialog open={!!deleteTarget} onOpenChange={(v) => !v && setDeleteTarget(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Log Group</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-fg-muted">
            Permanently delete <strong>{deleteTarget}</strong> and all its log streams and events?
          </p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteTarget(undefined)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() => deleteTarget && deleteMut.mutate(deleteTarget)}
              disabled={deleteMut.isPending}
            >
              {deleteMut.isPending && <Spinner className="mr-2" />}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── Bulk delete confirm dialog ── */}
      <Dialog open={showBulkDelete} onOpenChange={(v) => !v && setShowBulkDelete(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {formatQuantity(selectedGroups.size, "Log Group")}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <p className="text-sm text-fg-muted">
              Permanently delete these log groups and all their streams and events?
            </p>
            <ul className="mt-3 max-h-40 overflow-y-auto rounded border border-border bg-bg-muted p-2 font-mono text-xs">
              {[...selectedGroups].map((name) => (
                <li key={name} className="truncate py-0.5">
                  {name}
                </li>
              ))}
            </ul>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setShowBulkDelete(false)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() => bulkDeleteMut.mutate([...selectedGroups])}
              disabled={bulkDeleteMut.isPending}
            >
              {bulkDeleteMut.isPending && <Spinner className="mr-2" />}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </ResourceListPage>
  )
}

// ─── CreateLogGroupDialog ─────────────────────────────────────────────────────

const createGroupSchema = z.object({
  name: z
    .string()
    .min(1, "Name is required")
    .max(512, "Max 512 characters")
    .regex(/^[a-zA-Z0-9_./#-]+$/, "Letters, numbers, and . _ / # - only"),
  // Empty string means "never expire" — CloudWatch Logs has no retention value
  // for that, it is the absence of a retention policy.
  retention: z.string(),
})

interface TagDraft {
  key: string
  value: string
}

function CreateLogGroupDialog({
  open,
  onClose,
  onSubmit,
  isPending,
}: {
  open: boolean
  onClose: () => void
  onSubmit: (input: CreateLogGroupInput) => void
  isPending: boolean
}) {
  const [tags, setTags] = useState<TagDraft[]>([])

  const form = useForm({
    validators: { onChange: createGroupSchema },
    defaultValues: { name: "", retention: "" },
    onSubmit: ({ value }) =>
      onSubmit({
        name: value.name,
        retentionInDays: value.retention ? Number(value.retention) : undefined,
        tags: tagDraftsToMap(tags),
      }),
  })

  function handleClose() {
    onClose()
    setTimeout(() => {
      form.reset()
      setTags([])
    }, 150)
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create log group</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            e.stopPropagation()
            void form.handleSubmit()
          }}
        >
          <form.Field name="name" validators={{ onChange: createGroupSchema.shape.name }}>
            {(field) => (
              <FormRow>
                <FormField
                  label="Log Group Name"
                  required
                  error={fieldError(field.state.meta.errors, field.state.meta.isTouched)}
                >
                  <Input
                    placeholder="/aws/lambda/my-function"
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                    autoFocus
                  />
                </FormField>
              </FormRow>
            )}
          </form.Field>

          <form.Field name="retention">
            {(field) => (
              <FormRow>
                <FormField
                  label="Retention"
                  hint="CloudWatch Logs accepts only these retention periods."
                >
                  <Select
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                  >
                    <option value="">Never expire</option>
                    {LOG_RETENTION_DAYS.map((days) => (
                      <option key={days} value={String(days)}>
                        {retentionLabel(days)}
                      </option>
                    ))}
                  </Select>
                </FormField>
              </FormRow>
            )}
          </form.Field>

          <TagsField tags={tags} onChange={setTags} />

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

/** Drops blank rows and collapses the drafts into the API's tag map. */
function tagDraftsToMap(tags: TagDraft[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const { key, value } of tags) {
    if (key.trim() === "") continue
    out[key] = value
  }
  return out
}

/**
 * Key/value rows for create-time tags. Deliberately unvalidated beyond
 * "don't send blank rows": the service applies AWS's tag rules (length, the
 * reserved `aws:` prefix, the 50-tag limit) and its InvalidParameterException
 * is surfaced by the caller's error toast, so the UI does not maintain a
 * second, drifting copy of them.
 */
function TagsField({ tags, onChange }: { tags: TagDraft[]; onChange: (tags: TagDraft[]) => void }) {
  function update(index: number, patch: Partial<TagDraft>) {
    onChange(tags.map((tag, i) => (i === index ? { ...tag, ...patch } : tag)))
  }

  return (
    <FormRow>
      <FormField label="Tags" hint="Optional. Applied when the log group is created.">
        <div className="flex flex-col gap-2">
          {tags.map((tag, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                aria-label={`Tag ${i + 1} key`}
                placeholder="key"
                value={tag.key}
                onChange={(e) => update(i, { key: e.target.value })}
              />
              <Input
                aria-label={`Tag ${i + 1} value`}
                placeholder="value"
                value={tag.value}
                onChange={(e) => update(i, { value: e.target.value })}
              />
              <Button
                type="button"
                size="icon"
                variant="ghost"
                aria-label={`Remove tag ${i + 1}`}
                onClick={() => onChange(tags.filter((_, j) => j !== i))}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            </div>
          ))}
          <Button
            type="button"
            size="sm"
            variant="secondary"
            className="self-start"
            onClick={() => onChange([...tags, { key: "", value: "" }])}
          >
            <Plus className="h-3.5 w-3.5" />
            Add tag
          </Button>
        </div>
      </FormField>
    </FormRow>
  )
}
