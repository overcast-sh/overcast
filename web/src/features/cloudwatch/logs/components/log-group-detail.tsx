import { useState, useMemo } from "react"
import { Link } from "@tanstack/react-router"
import { useForm } from "@tanstack/react-form"
import { z } from "zod"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import {
  AlertTriangle,
  ArrowLeft,
  Plus,
  Trash2,
  RefreshCw,
  FileText,
  Search,
  X,
  ListFilter,
} from "lucide-react"
import {
  logsGroupsQueryOptions,
  logsGroupTagsQueryOptions,
  logsStreamsQueryOptions,
  logsKeys,
  logsFilterQueryOptions,
  createLogStreamMutationOptions,
  deleteLogStreamMutationOptions,
  deleteLogGroupMutationOptions,
} from "@/features/cloudwatch/logs/data"
import { retentionLabel } from "@/features/cloudwatch/logs/retention"
import { logs } from "@/services/api"
import {
  TimeRangeFilter,
  type TimeRange,
} from "@/features/cloudwatch/logs/components/time-range-filter"
import { LogSearchResults } from "@/features/cloudwatch/logs/components/log-search-results"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { FormField, FormRow, fieldError } from "@/components/ui/form"
import { ResourceTable } from "@/components/ui/resource-table"
import { RowAction, SelectCheckbox } from "@/components/ui/resource-list-page"
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Card, CardContent } from "@/components/ui/card"
import { PageHeader, Spinner, EmptyState } from "@/components/ui/primitives"
import { ApplicationOwnershipBanner } from "@/components/application-ownership-banner"
import { useToast } from "@/components/ui/toast"
import { useEndpoint } from "@/hooks/use-endpoint"
import { isResourceNotFound } from "@/lib/aws-error"
import { formatLogDate } from "@/lib/log-format"
import { cn } from "@/lib/utils"
import { formatQuantity } from "@/lib/format"

interface Props {
  groupName: string
}

export function LogGroupDetail({ groupName }: Props) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast } = useToast()
  const { region } = useEndpoint()

  const [showCreateStream, setShowCreateStream] = useState(false)
  const [deleteStreamTarget, setDeleteStreamTarget] = useState<string>()
  const [selectedStreams, setSelectedStreams] = useState<Set<string>>(new Set())
  const [showBulkDelete, setShowBulkDelete] = useState(false)
  const [filterInput, setFilterInput] = useState("")
  const [activeFilter, setActiveFilter] = useState("")
  const [timeRange, setTimeRange] = useState<TimeRange>({})

  // ─── Data ────────────────────────────────────────────────────────────────
  const { data: allGroups = [] } = useQuery(logsGroupsQueryOptions())
  const group = allGroups.find((g) => g.logGroupName === groupName)

  const {
    data: streams = [],
    error: streamsError,
    isLoading,
    isFetching,
    refetch,
  } = useQuery(logsStreamsQueryOptions(groupName))

  const { data: groupTags = {} } = useQuery(logsGroupTagsQueryOptions(groupName))
  const tagEntries = useMemo(
    () => Object.entries(groupTags).sort(([a], [b]) => a.localeCompare(b)),
    [groupTags],
  )

  // The default order is a composite `ResourceTable`'s single-column sort
  // cannot express — most-recently-written first, falling back from ingestion
  // to event time, then by name — so it stays here and is handed to the table
  // as the row order. No `defaultSort` for the same reason: leaving the table
  // unsorted keeps this exact order, and the none → asc → desc → none cycle
  // returns to it.
  const sortedStreams = useMemo(
    () =>
      [...streams].sort((a, b) => {
        const aWritten = a.lastIngestionTime ?? a.lastEventTimestamp ?? 0
        const bWritten = b.lastIngestionTime ?? b.lastEventTimestamp ?? 0
        if (aWritten !== bWritten) return bWritten - aWritten
        return (a.logStreamName ?? "").localeCompare(b.logStreamName ?? "")
      }),
    [streams],
  )

  // Cross-stream search query — only runs when user has an active filter.
  const {
    data: filterResult,
    error: filterError,
    isLoading: isFilterLoading,
    isFetching: isFilterFetching,
  } = useQuery({
    ...logsFilterQueryOptions(groupName, {
      filterPattern: activeFilter || undefined,
      startTime: timeRange.startTime,
      endTime: timeRange.endTime,
    }),
    enabled: !!activeFilter,
  })

  const filteredEvents = useMemo(() => filterResult?.events ?? [], [filterResult])

  // ─── Mutations ───────────────────────────────────────────────────────────
  const deleteGroupMut = useMutation({
    ...deleteLogGroupMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: logsKeys.groups() })
      void navigate({ to: "/cloudwatch/logs" })
      toast({ title: "Log group deleted", description: groupName })
    },
    onError: (err: Error) =>
      toast({ title: "Delete failed", description: err.message, variant: "danger" }),
  })

  const createStreamMut = useMutation({
    ...createLogStreamMutationOptions(groupName),
    onSuccess: (_, name) => {
      void qc.invalidateQueries({ queryKey: logsKeys.streams(groupName) })
      setShowCreateStream(false)
      toast({ title: "Log stream created", description: name, variant: "success" })
    },
    onError: (err: Error) =>
      toast({ title: "Create failed", description: err.message, variant: "danger" }),
  })

  const deleteStreamMut = useMutation({
    ...deleteLogStreamMutationOptions(groupName),
    onSuccess: (_, streamName) => {
      void qc.invalidateQueries({ queryKey: logsKeys.streams(groupName) })
      setDeleteStreamTarget(undefined)
      toast({ title: "Log stream deleted", description: streamName })
    },
    onError: (err: Error) =>
      toast({ title: "Delete failed", description: err.message, variant: "danger" }),
  })

  const bulkDeleteMut = useMutation({
    mutationFn: async (names: string[]) => {
      await Promise.all(names.map((n) => logs.deleteStream(groupName, n)))
    },
    onSuccess: (_, names) => {
      void qc.invalidateQueries({ queryKey: logsKeys.streams(groupName) })
      setSelectedStreams(new Set())
      setShowBulkDelete(false)
      toast({
        title: `${formatQuantity(names.length, "stream")} deleted`,
        variant: "success",
      })
    },
    onError: (err: Error) =>
      toast({ title: "Bulk delete failed", description: err.message, variant: "danger" }),
  })

  // DescribeLogStreams answering ResourceNotFoundException means the group
  // itself is missing from the selected region — say so, rather than render
  // the "No log streams" empty state that reads as an application that never
  // logged. The usual cause is the region selector pointing away from where
  // the group lives.
  if (isResourceNotFound(streamsError)) {
    return (
      <EmptyState
        icon={<AlertTriangle className="h-10 w-10" />}
        title="Log group not found"
        description={`No log group named "${groupName}" exists in ${region} — it may live in a different region. Check the region selector.`}
        action={
          <Button onClick={() => navigate({ to: "/cloudwatch/logs" })}>
            <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
            All Groups
          </Button>
        }
      />
    )
  }

  return (
    <div className="flex w-full flex-col gap-6">
      <PageHeader
        title={groupName}
        description={group?.arn}
        actions={
          <div className="flex items-center gap-2">
            <Button variant="ghost" size="sm" onClick={() => navigate({ to: "/cloudwatch/logs" })}>
              <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
              All Groups
            </Button>
            <Button variant="ghost" size="sm" onClick={() => refetch()} disabled={isFetching}>
              <RefreshCw className={cn("h-4 w-4", isFetching && "animate-spin")} />
            </Button>
            <Button size="sm" variant="secondary" asChild>
              <Link to="/cloudwatch/logs/events" search={{ groupName }}>
                <ListFilter className="mr-1 h-4 w-4" />
                View All Logs
              </Link>
            </Button>
            <Button size="sm" onClick={() => setShowCreateStream(true)}>
              <Plus className="mr-1 h-4 w-4" />
              Create Stream
            </Button>
            <Button
              size="sm"
              variant="danger"
              onClick={() => deleteGroupMut.mutate(groupName)}
              disabled={deleteGroupMut.isPending}
            >
              <Trash2 className="mr-1 h-4 w-4" />
              Delete Group
            </Button>
          </div>
        }
      />

      <ApplicationOwnershipBanner candidates={[group?.arn, groupName]} />

      {/* Group info card */}
      {group && (
        <Card>
          <CardContent className="flex flex-col gap-4 py-4">
            <div className="grid grid-cols-3 gap-4">
              <div>
                <p className="font-mono text-xs font-medium text-fg-muted">Created</p>
                <p className="text-sm">{formatLogDate(group.creationTime)}</p>
              </div>
              <div>
                <p className="font-mono text-xs font-medium text-fg-muted">Retention</p>
                <p className="text-sm">
                  {group.retentionInDays ? retentionLabel(group.retentionInDays) : "Never expire"}
                </p>
              </div>
              <div>
                <p className="font-mono text-xs font-medium text-fg-muted">Log Streams</p>
                <p className="text-sm">{streams.length}</p>
              </div>
            </div>
            <div>
              <p className="font-mono text-xs font-medium text-fg-muted">Tags</p>
              {tagEntries.length === 0 ? (
                <p className="text-sm text-fg-muted">None</p>
              ) : (
                <div className="mt-1 flex flex-wrap gap-1.5">
                  {tagEntries.map(([key, value]) => (
                    <span
                      key={key}
                      className="rounded-control border border-border bg-bg-muted px-2 py-0.5 font-mono text-xs"
                    >
                      {key}: {value || "—"}
                    </span>
                  ))}
                </div>
              )}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Cross-stream search toolbar */}
      <div className="flex items-center gap-2 rounded-md border border-border bg-bg-muted px-3 py-2.5">
        <Search className="h-4 w-4 shrink-0 text-fg-muted" />
        <Input
          // Never a credential field — see the note in log-events-viewer.tsx.
          data-1p-ignore
          data-lpignore="true"
          className="h-7 border-0 bg-transparent px-1 shadow-none focus-visible:ring-0"
          placeholder='Search across all streams — e.g. ERROR, "request failed", ERROR timeout'
          value={filterInput}
          onChange={(e) => setFilterInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") setActiveFilter(filterInput)
            if (e.key === "Escape") {
              setFilterInput("")
              setActiveFilter("")
            }
          }}
        />
        {(filterInput || activeFilter) && (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setFilterInput("")
              setActiveFilter("")
            }}
            className="h-7 px-2"
          >
            <X className="h-3.5 w-3.5" />
          </Button>
        )}
        <div className="mx-0.5 h-5 w-px bg-border" />
        <TimeRangeFilter value={timeRange} onChange={setTimeRange} />
        <Button
          size="sm"
          onClick={() => setActiveFilter(filterInput)}
          disabled={isFilterFetching || !filterInput.trim()}
          className="h-7"
        >
          {isFilterFetching ? <Spinner className="mr-1.5 h-3.5 w-3.5" /> : null}
          Search
        </Button>
        {activeFilter && !isFilterLoading && !filterError && (
          <span className="ml-1 shrink-0 text-xs text-fg-muted">
            {formatQuantity(filteredEvents.length, "result")}
          </span>
        )}
      </div>

      {/* Search results */}
      {activeFilter && (
        <>
          {isFilterLoading ? (
            <div className="flex justify-center py-8">
              <Spinner className="h-5 w-5" />
            </div>
          ) : filterError ? (
            // A failed search is not "no matches" — an invalid filter pattern
            // (InvalidParameterException) used to render exactly that, and the
            // server's message says what to fix.
            <EmptyState
              icon={<AlertTriangle className="h-8 w-8" />}
              title="Search failed"
              description={filterError instanceof Error ? filterError.message : String(filterError)}
            />
          ) : filteredEvents.length === 0 ? (
            <EmptyState
              icon={<Search className="h-8 w-8" />}
              title="No matching events"
              description="Try a different filter pattern."
            />
          ) : (
            <LogSearchResults
              events={filteredEvents}
              filterPattern={activeFilter}
              onOpenEvent={(hit) =>
                void navigate({
                  to: "/cloudwatch/logs/stream",
                  search: {
                    groupName,
                    streamName: hit.streamName,
                    anchorTs: hit.timestamp,
                    anchorSig: hit.signature,
                  },
                })
              }
            />
          )}
        </>
      )}

      {/* Streams table */}
      {selectedStreams.size > 0 && (
        <div className="flex items-center gap-3 rounded-md border border-border bg-bg-muted px-3 py-2">
          <span className="text-sm font-medium">
            {formatQuantity(selectedStreams.size, "stream")} selected
          </span>
          <Button size="sm" variant="danger" onClick={() => setShowBulkDelete(true)}>
            <Trash2 className="mr-1 h-3.5 w-3.5" />
            Delete Selected
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setSelectedStreams(new Set())}>
            Clear
          </Button>
        </div>
      )}
      <ResourceTable
        variant="embedded"
        query={{ data: sortedStreams, isLoading, error: streamsError }}
        noun="log streams"
        emptyIcon={FileText}
        emptyTitle="No log streams"
        emptyDescription="Create a log stream or wait for your application to generate logs."
        emptyAction={
          <Button onClick={() => setShowCreateStream(true)}>
            <Plus className="mr-1.5 h-3.5 w-3.5" />
            Create Stream
          </Button>
        }
        errorTitle="Failed to load log streams"
        rowKey={(s) => s.logStreamName ?? ""}
        onRowClick={(s) =>
          navigate({
            to: "/cloudwatch/logs/stream",
            search: { groupName, streamName: s.logStreamName ?? "" },
          })
        }
        columns={[
          {
            // Selection is an ordinary leading column, not a TanStack feature —
            // `rowSelectionFeature` is deliberately unregistered. The label
            // stops the click reaching the row's navigate handler.
            id: "select",
            headerClassName: "w-10",
            cellClassName: "p-0",
            header: (
              <SelectCheckbox
                label="Select all log streams"
                checked={sortedStreams.length > 0 && selectedStreams.size === sortedStreams.length}
                indeterminate={
                  selectedStreams.size > 0 && selectedStreams.size < sortedStreams.length
                }
                onCheckedChange={(checked) =>
                  setSelectedStreams(
                    checked ? new Set(sortedStreams.map((s) => s.logStreamName ?? "")) : new Set(),
                  )
                }
              />
            ),
            cell: (s) => (
              <label
                className="flex cursor-pointer items-center justify-center p-3"
                onClick={(e) => e.stopPropagation()}
              >
                <SelectCheckbox
                  label={`Select ${s.logStreamName ?? ""}`}
                  checked={selectedStreams.has(s.logStreamName ?? "")}
                  onCheckedChange={(checked) => {
                    const next = new Set(selectedStreams)
                    if (checked) next.add(s.logStreamName ?? "")
                    else next.delete(s.logStreamName ?? "")
                    setSelectedStreams(next)
                  }}
                />
              </label>
            ),
          },
          {
            id: "name",
            header: "Stream Name",
            cellClassName: "font-medium",
            // The identity column; the checkbox in front of it is why this
            // needs saying rather than falling out of "index 0".
            hideable: false,
            sortValue: (s) => s.logStreamName,
            cell: (s) => <span title={s.logStreamName}>{s.logStreamName}</span>,
          },
          {
            id: "created",
            header: "Created",
            cellClassName: "text-fg-muted",
            sortValue: (s) => s.creationTime,
            cell: (s) => formatLogDate(s.creationTime),
          },
          {
            id: "last-event",
            header: "Last Event",
            cellClassName: "text-fg-muted",
            sortValue: (s) => s.lastEventTimestamp,
            cell: (s) => formatLogDate(s.lastEventTimestamp),
          },
          {
            id: "last-ingestion",
            header: "Last Ingestion",
            cellClassName: "text-fg-muted",
            sortValue: (s) => s.lastIngestionTime,
            cell: (s) => formatLogDate(s.lastIngestionTime),
          },
        ]}
        rowActions={(s) => (
          <RowAction
            label={`Delete ${s.logStreamName ?? ""}`}
            tone="danger"
            onClick={() => setDeleteStreamTarget(s.logStreamName)}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </RowAction>
        )}
      />

      {/* ── Create stream dialog ── */}
      <CreateStreamDialog
        open={showCreateStream}
        onClose={() => setShowCreateStream(false)}
        isPending={createStreamMut.isPending}
        onSubmit={(name) => createStreamMut.mutate(name)}
      />

      {/* ── Delete stream confirm ── */}
      <Dialog
        open={!!deleteStreamTarget}
        onOpenChange={(v) => !v && setDeleteStreamTarget(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Log Stream</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-fg-muted">
            Permanently delete <strong>{deleteStreamTarget}</strong> and all its events?
          </p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteStreamTarget(undefined)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() => deleteStreamTarget && deleteStreamMut.mutate(deleteStreamTarget)}
              disabled={deleteStreamMut.isPending}
            >
              {deleteStreamMut.isPending && <Spinner className="mr-2" />}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── Bulk delete confirm ── */}
      <Dialog open={showBulkDelete} onOpenChange={(v) => !v && setShowBulkDelete(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {formatQuantity(selectedStreams.size, "Log Stream")}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <p className="text-sm text-fg-muted">
              Permanently delete the following streams and all their events?
            </p>
            <ul className="mt-3 max-h-40 overflow-y-auto rounded border border-border bg-bg-muted p-2 text-sm">
              {[...selectedStreams].map((name) => (
                <li key={name} className="truncate py-0.5 font-mono text-xs">
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
              onClick={() => bulkDeleteMut.mutate([...selectedStreams])}
              disabled={bulkDeleteMut.isPending}
            >
              {bulkDeleteMut.isPending && <Spinner className="mr-2" />}
              Delete {formatQuantity(selectedStreams.size, "Stream")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// ─── CreateStreamDialog ───────────────────────────────────────────────────────

const createStreamSchema = z.object({
  name: z
    .string()
    .min(1, "Name is required")
    .max(512, "Max 512 characters")
    .regex(/^[a-zA-Z0-9_./#-]+$/, "Letters, numbers, and . _ / # - only"),
})

function CreateStreamDialog({
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
    validators: { onChange: createStreamSchema },
    defaultValues: { name: "" },
    onSubmit: ({ value }) => onSubmit(value.name),
  })

  function handleClose() {
    onClose()
    setTimeout(() => form.reset(), 150)
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create Log Stream</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            e.stopPropagation()
            void form.handleSubmit()
          }}
        >
          <form.Field name="name" validators={{ onChange: createStreamSchema.shape.name }}>
            {(field) => (
              <FormRow>
                <FormField
                  label="Stream Name"
                  required
                  error={fieldError(field.state.meta.errors, field.state.meta.isTouched)}
                >
                  <Input
                    placeholder="my-stream"
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                    autoFocus
                  />
                </FormField>
              </FormRow>
            )}
          </form.Field>
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
