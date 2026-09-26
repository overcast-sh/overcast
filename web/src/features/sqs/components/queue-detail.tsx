import { useState, useEffect, useRef, useCallback } from "react"
import { useForm } from "@tanstack/react-form"
import { z } from "zod"
import { Card, CardContent } from "@/components/ui/card"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import {
  MessagesSquare,
  Plus,
  Trash2,
  RefreshCw,
  Send,
  Flame,
  ChevronDown,
  ChevronRight,
  Bell,
  Link,
  Inbox,
  Settings,
  Undo2,
  Activity,
} from "lucide-react"
import {
  sqsQueueQueryOptions,
  sqsMessagesQueryOptions,
  sqsMetricsQueryOptions,
  sqsKeys,
  deleteQueueMutationOptions,
  purgeQueueMutationOptions,
  deleteMessageMutationOptions,
  receiveMessagesMutationOptions,
  updateQueueAttributesMutationOptions,
  deadLetterSourceQueuesQueryOptions,
  redriveMutationOptions,
  redriveMessageMutationOptions,
} from "@/features/sqs/data"
import {
  MonitorPanel,
  type MonitorCardConfig,
} from "@/features/monitoring/components/monitor-panel"
import { DEFAULT_CHART_RANGE, type ChartRangeToken } from "@/features/monitoring/types"
import {
  snsQueueSubscriptionsQueryOptions,
  snsTopicsQueryOptions,
  snsKeys,
  subscribeMutationOptions,
  unsubscribeMutationOptions,
  createTopicMutationOptions,
} from "@/features/sns/data"
import { useEventStream } from "@/hooks/use-event-stream"
import { useCountdown } from "@/hooks/use-countdown"
import { EventType } from "@/services/event-types"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Badge } from "@/components/ui/badge"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
// eslint-disable-next-line local/prefer-resource-table -- the message stream's ghost rows are deliberately not in `query.data`
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { RowAction } from "@/components/ui/resource-list-page"
import { ResourceTable } from "@/components/ui/resource-table"
import { PageHeader, Spinner, EmptyState, CodeBlock } from "@/components/ui/primitives"
import { ApplicationOwnershipBanner } from "@/components/application-ownership-banner"
import { ResourceArnCombobox } from "@/components/ui/resource-arn-combobox"
import { ArnLink, ArnText } from "@/components/ui/arn-link"
import { RawStateLink } from "@/features/debug/raw-state-link"
import { useToast } from "@/components/ui/toast"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { SendMessageDialog } from "./send-message"
import type { SQSMessage, SQSQueueDetail, SNSSubscription, SNSTopic } from "@/types"
import { cn } from "@/lib/utils"
import { formatQuantity } from "@/lib/format"
import { SegmentedControl, type SegmentedOption } from "@/components/ui/segmented-control"

/** Payload shape for sqs:MessageSent / sqs:MessageInflight / sqs:MessageVisible / sqs:MessageDeleted events. */
interface SQSMessageEventPayload {
  queueName: string
  messageId: string
  visibleAfter?: number
}

interface Props {
  queueName: string
}

// The AWS/SQS metrics catalogue, grouped into cards (docs/plans/service-metrics-platform.md
// phase 2's per-metric disposition table). Visible/NotVisible share a card
// (both queue-depth gauges, same unit); sent/received/deleted/empty-receive
// counts share a card; the oldest-message age gets its own since it is only
// ever recorded for a non-empty queue.
const SQS_MONITOR_CARDS: MonitorCardConfig[] = [
  {
    title: "Messages Sent, Received & Deleted",
    unit: "Count",
    series: [
      { metric: "NumberOfMessagesSent", statistic: "Sum", label: "Sent" },
      { metric: "NumberOfMessagesReceived", statistic: "Sum", label: "Received" },
      { metric: "NumberOfMessagesDeleted", statistic: "Sum", label: "Deleted" },
      { metric: "NumberOfEmptyReceives", statistic: "Sum", label: "Empty receives" },
    ],
  },
  {
    title: "Queue Depth",
    unit: "Count",
    series: [
      {
        metric: "ApproximateNumberOfMessagesVisible",
        statistic: "Average",
        label: "Visible",
      },
      {
        metric: "ApproximateNumberOfMessagesNotVisible",
        statistic: "Average",
        label: "In flight",
      },
    ],
  },
  {
    title: "Age of Oldest Message",
    unit: "Seconds",
    series: [
      { metric: "ApproximateAgeOfOldestMessage", statistic: "Maximum", label: "Oldest message" },
    ],
  },
]

/** Why a message left the queue, for the 30s ghost row it leaves behind. */
type TombstoneReason = "deleted" | "redriven"

export function QueueDetail({ queueName }: Props) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast } = useToast()

  const [showSend, setShowSend] = useState(false)
  const [showEditConfig, setShowEditConfig] = useState(false)
  const [deleteQueueConfirm, setDeleteQueueConfirm] = useState(false)
  const [purgeConfirm, setPurgeConfirm] = useState(false)
  const [expandedMsg, setExpandedMsg] = useState<string>()
  const [deleteTarget, setDeleteTarget] = useState<SQSMessage>()
  const [showSubscribe, setShowSubscribe] = useState(false)
  const [unsubscribeTarget, setUnsubscribeTarget] = useState<SNSSubscription>()
  const [activeTab, setActiveTab] = useState<"messages" | "subscriptions" | "monitor">("messages")
  const [monitorRange, setMonitorRange] = useState<ChartRangeToken>(DEFAULT_CHART_RANGE)
  const [monitorRefreshMs, setMonitorRefreshMs] = useState<number | false>(30_000)

  // Track recently-removed messages: show them crossed-out for 30s before hiding.
  // A redrive leaves the DLQ via DeleteMessage too, so the reason is recorded
  // to stop a message the user just sent home from reading as "deleted".
  const [deletedMessages, setDeletedMessages] = useState<
    Map<string, { message: SQSMessage; deletedAt: number; reason: TombstoneReason }>
  >(new Map())

  // Message IDs this page redrove, so the MessageDeleted event that follows is
  // attributed correctly even when it lands before the mutation resolves.
  const redrivenIdsRef = useRef<Set<string>>(new Set())

  // Stable ref for invalidating queries from within SSE event callbacks.
  const invalidateMessages = useCallback(() => {
    void qc.invalidateQueries({ queryKey: sqsKeys.messageList(queueName) })
    void qc.invalidateQueries({ queryKey: sqsKeys.queueDetail(queueName) })
  }, [qc, queueName])

  // SSE: subscribe to sqs events for real-time status updates.
  const { events: sqsEvents } = useEventStream({ source: "sqs" })
  const seenEventRef = useRef<Set<string>>(new Set())

  useEffect(() => {
    if (!sqsEvents.length) return
    const latest = sqsEvents[sqsEvents.length - 1]
    // Deduplicate by time+type+stream index. Including the array index breaks
    // ties when two events of the same type arrive in the same millisecond.
    const key = `${latest.type}:${latest.time}:${sqsEvents.length - 1}`
    if (seenEventRef.current.has(key)) return
    seenEventRef.current.add(key)
    // Trim the dedup set to avoid unbounded growth.
    if (seenEventRef.current.size > 500) {
      const iter = seenEventRef.current.values()
      seenEventRef.current.delete(iter.next().value as string)
    }

    const payload = latest.payload as SQSMessageEventPayload | undefined
    if (!payload || payload.queueName !== queueName) return

    // MessageSent/Inflight/Visible invalidation is handled globally by
    // useQuerySync (mounted in AppShell). Only the tombstone behaviour for
    // MessageDeleted needs to live here.
    if (latest.type === EventType.sqs.MessageDeleted) {
      // Message left the queue — record a tombstone locally too.
      setDeletedMessages((prev) => {
        const next = new Map(prev)
        // Find the message in the current messages list to capture its data.
        // (The messages query will be stale after the next refetch, but we
        // may already have the message object available in usages further up.)
        // We set a tombstone with just the messageId so the UI can show it.
        if (!next.has(payload.messageId)) {
          next.set(payload.messageId, {
            // Placeholder message — body will be filled if the message was visible in the list.
            message: {
              messageId: payload.messageId,
              receiptHandle: "",
              body: "",
              md5OfBody: "",
              attributes: {},
              messageAttributes: {},
              inflight: false,
              delayed: false,
              visibleAfter: 0,
              approximateReceiveCount: 0,
            },
            deletedAt: Date.now(),
            reason: redrivenIdsRef.current.has(payload.messageId) ? "redriven" : "deleted",
          })
        }
        return next
      })
      invalidateMessages()
    }
  }, [sqsEvents, queueName, invalidateMessages])

  // Sweep expired deleted-message tombstones every second.
  useEffect(() => {
    const id = setInterval(() => {
      setDeletedMessages((prev) => {
        if (prev.size === 0) return prev
        const now = Date.now()
        const next = new Map(prev)
        for (const [id, entry] of next) {
          if (now - entry.deletedAt >= 30_000) {
            next.delete(id)
            redrivenIdsRef.current.delete(id)
          }
        }
        return next.size === prev.size ? prev : next
      })
    }, 1000)
    return () => clearInterval(id)
  }, [])

  const {
    data: queue,
    isLoading: qLoading,
    isFetching: qFetching,
    refetch: refetchQueue,
  } = useQuery(sqsQueueQueryOptions(queueName))

  const {
    data: messages = [],
    isLoading: mLoading,
    isFetching: mFetching,
    refetch: refetchMessages,
  } = useQuery(sqsMessagesQueryOptions(queueName))

  const metricsQuery = useQuery(sqsMetricsQueryOptions(queueName, monitorRange, monitorRefreshMs))

  // Whether redrive is offered at all follows AWS's own rule: StartMessageMoveTask
  // "is currently limited to supporting message redrive from queues that are
  // configured as dead-letter queues (DLQs) of other Amazon SQS queues only"
  // (API_StartMessageMoveTask). ListDeadLetterSourceQueues answers exactly that
  // question — a non-empty result means at least one SQS queue names this queue
  // in its RedrivePolicy — and it is one of the calls AWS lists under the
  // minimum IAM permissions for a redrive.
  //
  // Deliberately *not* gated on the queue being non-empty: AWS lets you start a
  // redrive on an idle DLQ, and the peek only sees the messages it can read.
  const { data: dlqSourceUrls = [] } = useQuery(deadLetterSourceQueuesQueryOptions(queueName))
  const isDLQ = dlqSourceUrls.length > 0

  const redriveMut = useResourceMutation({
    options: redriveMutationOptions(queueName),
    invalidateKeys: [sqsKeys.messageList(queueName), sqsKeys.queueDetail(queueName)],
    successTitle: "Redrive started",
    successDescription: () =>
      dlqSourceUrls.length === 1
        ? `Messages moved back to ${queueNameFromUrl(dlqSourceUrls[0])}`
        : "Messages moved back to their source queues",
    successVariant: "success",
    errorTitle: "Redrive failed",
  })

  // Single-message redrive. SQS has no API for this — see sqs.redriveMessage.
  const redriveMsgMut = useResourceMutation({
    options: redriveMessageMutationOptions(queueName),
    invalidateKeys: [sqsKeys.messageList(queueName), sqsKeys.queueDetail(queueName)],
    successTitle: "Message returned to source",
    successDescription: (msg: SQSMessage) =>
      `Sent back to ${queueNameFromArn(msg.attributes.DeadLetterSourceQueueArn ?? "")}`,
    successVariant: "success",
    errorTitle: "Redrive failed",
    onSuccess: (_data, msg) => {
      setDeletedMessages((prev) => {
        const next = new Map(prev)
        next.set(msg.messageId, { message: msg, deletedAt: Date.now(), reason: "redriven" })
        return next
      })
    },
  })

  function handleRedriveMessage(msg: SQSMessage) {
    redrivenIdsRef.current.add(msg.messageId)
    redriveMsgMut.mutate(msg)
  }

  const deleteQueueMut = useResourceMutation({
    options: deleteQueueMutationOptions(),
    invalidateKeys: [sqsKeys.queues()],
    successTitle: "Queue deleted",
    successDescription: () => queueName,
    errorTitle: "Delete failed",
    onSuccess: () => {
      setDeleteQueueConfirm(false)
      void navigate({ to: "/sqs" })
    },
  })

  const purge = useResourceMutation({
    options: purgeQueueMutationOptions(queueName),
    invalidateKeys: [sqsKeys.queueDetail(queueName), sqsKeys.messageList(queueName)],
    successTitle: "Queue purged",
    successDescription: () => `All messages deleted from ${queueName}`,
    successVariant: "success",
    errorTitle: "Purge failed",
    onSuccess: () => {
      setDeletedMessages(new Map())
      setPurgeConfirm(false)
    },
  })

  const deleteMsg = useMutation({
    ...deleteMessageMutationOptions(queueName),
    onSuccess: (_, receiptHandle) => {
      // Record the deleted message for the 30s ghost view.
      const msg = messages.find((m) => m.receiptHandle === receiptHandle)
      if (msg) {
        setDeletedMessages((prev) => {
          const next = new Map(prev)
          next.set(msg.messageId, { message: msg, deletedAt: Date.now(), reason: "deleted" })
          return next
        })
      }
      void qc.invalidateQueries({ queryKey: sqsKeys.messageList(queueName) })
      void qc.invalidateQueries({ queryKey: sqsKeys.queueDetail(queueName) })
      setDeleteTarget(undefined)
      toast({ title: "Message deleted" })
    },
    onError: (err: Error) =>
      toast({ title: "Delete failed", description: err.message, variant: "danger" }),
  })

  const receiveMut = useMutation({
    ...receiveMessagesMutationOptions(queueName),
    onSuccess: (data) => {
      void qc.invalidateQueries({ queryKey: sqsKeys.messageList(queueName) })
      void qc.invalidateQueries({ queryKey: sqsKeys.queueDetail(queueName) })
      toast({
        title:
          data.count > 0
            ? `Received ${formatQuantity(data.count, "message")}`
            : "No visible messages",
        description: data.count > 0 ? "Messages are now in-flight." : undefined,
      })
    },
    onError: (err: Error) =>
      toast({ title: "Receive failed", description: err.message, variant: "danger" }),
  })

  const updateConfigMut = useResourceMutation({
    options: updateQueueAttributesMutationOptions(queueName),
    invalidateKeys: [sqsKeys.queueDetail(queueName)],
    successTitle: "Queue configuration updated",
    successVariant: "success",
    errorTitle: "Update failed",
    onSuccess: () => setShowEditConfig(false),
  })

  // SNS subscriptions for this queue (set after queue ARN is available)
  const queueArn = queue?.arn ?? ""
  const {
    data: subscriptions = [],
    isLoading: sLoading,
    error: sError,
    refetch: refetchSubscriptions,
  } = useQuery(snsQueueSubscriptionsQueryOptions(queueArn))

  const { data: allTopics = [] } = useQuery(snsTopicsQueryOptions())

  const createTopicMut = useMutation({
    ...createTopicMutationOptions(),
  })

  const subscribeMut = useResourceMutation({
    options: subscribeMutationOptions(),
    invalidateKeys: [snsKeys.queueSubscriptions(queueArn)],
    successTitle: "Subscribed",
    successVariant: "success",
    errorTitle: "Subscribe failed",
    onSuccess: () => setShowSubscribe(false),
  })

  async function handleSubscribe(topicName: string, isNew: boolean) {
    try {
      if (isNew) await createTopicMut.mutateAsync(topicName)
      await subscribeMut.mutateAsync({ topicName, protocol: "sqs", endpoint: queueArn })
    } catch {
      // errors surfaced by the mutation's onError
    }
  }

  const unsubscribeMut = useResourceMutation({
    options: unsubscribeMutationOptions(),
    invalidateKeys: [snsKeys.queueSubscriptions(queueArn)],
    successTitle: "Unsubscribed",
    errorTitle: "Unsubscribe failed",
    onSuccess: () => setUnsubscribeTarget(undefined),
  })

  function handleRefresh() {
    void refetchQueue()
    void refetchMessages()
    void refetchSubscriptions()
  }

  if (qLoading) {
    return (
      <div className="flex items-center justify-center py-32">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (!queue) return null

  // Always derive counts from the peek result so stat cards and table are
  // always in sync. While the messages query is loading/refetching, show "…"
  // instead of potentially-stale queue attribute values.
  const visibleCount = messages.filter((m) => !m.inflight).length
  const inFlightCount = messages.filter((m) => m.inflight).length
  const countOrLoading = (n: number) => (mLoading || mFetching ? "…" : n)

  return (
    <div className="flex w-full flex-col gap-6">
      <PageHeader
        title={queueName}
        description={queue.arn}
        actions={
          <>
            <Button
              size="sm"
              variant="ghost"
              onClick={handleRefresh}
              disabled={qFetching || mFetching}
            >
              <RefreshCw
                className={cn("mr-1.5 h-3.5 w-3.5", (qFetching || mFetching) && "animate-spin")}
              />
              Refresh
            </Button>
            <RawStateLink namespace="sqs:queues" stateKey={queueName} />
            <Button size="sm" variant="ghost" onClick={() => setShowSend(true)}>
              <Send className="mr-1.5 h-3.5 w-3.5" />
              Send Message
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="text-warning hover:text-warning"
              onClick={() => setPurgeConfirm(true)}
            >
              <Flame className="mr-1.5 h-3.5 w-3.5" />
              Purge
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setShowEditConfig(true)}>
              <Settings className="mr-1.5 h-3.5 w-3.5" />
              Edit Config
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="text-danger hover:text-danger"
              onClick={() => setDeleteQueueConfirm(true)}
            >
              <Trash2 className="mr-1.5 h-3.5 w-3.5" />
              Delete Queue
            </Button>
          </>
        }
      />

      <ApplicationOwnershipBanner candidates={[queue.arn, queueName]} />

      {/* ── Attributes panel ── */}
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        <StatCard
          label="Visible Messages"
          value={countOrLoading(visibleCount)}
          variant={!mLoading && !mFetching && visibleCount > 0 ? "accent" : "default"}
        />
        <StatCard label="In-flight" value={countOrLoading(inFlightCount)} />
        <StatCard label="Visibility Timeout" value={`${queue.visibilityTimeout}s`} />
        <StatCard label="Retention" value={formatRetention(queue.messageRetentionPeriod)} />
      </div>

      <DefinitionList>
        <Definition label="Delay" value={`${queue.delaySeconds}s`} />
        <Definition label="Max Message Size" value={formatBytes(queue.maximumMessageSize)} />
        <Definition
          label="Long Poll Wait"
          value={
            queue.receiveMessageWaitTimeSeconds > 0
              ? `${queue.receiveMessageWaitTimeSeconds}s`
              : "Disabled"
          }
        />
        <Definition label="ARN" value={<ArnText arn={queue.arn} />} full />
      </DefinitionList>

      {/* ── Dead Letter Queue ── */}
      {queue.redrivePolicy && (
        <div className="rounded-lg border border-border bg-bg-muted p-4">
          <p className="mb-2 font-mono text-xs font-medium text-fg-muted">Dead Letter Queue</p>
          <DefinitionList className="gap-y-2">
            <Definition
              label="DLQ ARN"
              value={<ArnLink arn={queue.redrivePolicy.deadLetterTargetArn} />}
            />
            <Definition
              label="Max Receive Count"
              value={String(queue.redrivePolicy.maxReceiveCount)}
            />
          </DefinitionList>
        </div>
      )}

      {/* ── Redrive (this queue IS a DLQ) ── */}
      {isDLQ && (
        <div className="rounded-lg border border-warning/30 bg-warning-muted p-4">
          <div className="flex items-center justify-between gap-4">
            <div>
              <p className="font-mono text-sm font-medium text-fg">Dead Letter Queue</p>
              <p className="text-xs text-fg-muted">
                Receives failed messages from{" "}
                {dlqSourceUrls.length === 1
                  ? queueNameFromUrl(dlqSourceUrls[0])
                  : `${dlqSourceUrls.length} source queues`}
                . Redrive returns every message to the queue it failed on.
              </p>
            </div>
            <Button
              size="sm"
              variant="outline"
              className="shrink-0"
              onClick={() => redriveMut.mutate(queue.arn)}
              disabled={redriveMut.isPending}
            >
              <Undo2 className="mr-1.5 h-3.5 w-3.5" />
              {redriveMut.isPending ? "Redriving…" : "Redrive Messages"}
            </Button>
          </div>
        </div>
      )}

      {/* ── Tabbed card: Messages + SNS Subscriptions ── */}
      <Card>
        {/* Tab bar */}
        <div className="flex items-center gap-1 border-b border-border px-4 pt-3">
          <button
            onClick={() => setActiveTab("messages")}
            className={cn(
              "flex items-center gap-1.5 rounded-t px-3 py-2 text-sm font-medium transition-colors",
              activeTab === "messages"
                ? "border-b-2 border-accent text-fg"
                : "text-fg-muted hover:text-fg",
            )}
          >
            <MessagesSquare className="h-3.5 w-3.5" />
            Messages
            {!mLoading && (
              <Badge variant={visibleCount > 0 ? "accent" : "default"} className="ml-0.5">
                {messages.length}
              </Badge>
            )}
          </button>
          <button
            onClick={() => setActiveTab("subscriptions")}
            className={cn(
              "flex items-center gap-1.5 rounded-t px-3 py-2 text-sm font-medium transition-colors",
              activeTab === "subscriptions"
                ? "border-b-2 border-accent text-fg"
                : "text-fg-muted hover:text-fg",
            )}
          >
            <Bell className="h-3.5 w-3.5" />
            SNS Subscriptions
            {!sLoading && subscriptions.length > 0 && (
              <Badge variant="default" className="ml-0.5">
                {subscriptions.length}
              </Badge>
            )}
          </button>
          <button
            onClick={() => setActiveTab("monitor")}
            className={cn(
              "flex items-center gap-1.5 rounded-t px-3 py-2 text-sm font-medium transition-colors",
              activeTab === "monitor"
                ? "border-b-2 border-accent text-fg"
                : "text-fg-muted hover:text-fg",
            )}
          >
            <Activity className="h-3.5 w-3.5" />
            Monitor
          </button>
        </div>

        {/* Messages tab */}
        {activeTab === "messages" && (
          <CardContent className="p-0">
            <div className="flex items-center justify-between border-b border-border px-4 py-2">
              <span className="text-xs text-fg-muted">
                Dimmed rows are in-flight · strikethrough rows recently left the queue
              </span>
              <div className="flex items-center gap-2">
                <Button size="sm" variant="ghost" onClick={() => setShowSend(true)}>
                  <Send className="mr-1.5 h-3.5 w-3.5" />
                  Send
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => receiveMut.mutate(10)}
                  disabled={receiveMut.isPending || mFetching}
                >
                  {receiveMut.isPending ? (
                    <Spinner className="mr-1.5 h-3.5 w-3.5" />
                  ) : (
                    <Inbox className="mr-1.5 h-3.5 w-3.5" />
                  )}
                  Receive
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => refetchMessages()}
                  disabled={mFetching}
                >
                  <RefreshCw className={cn("mr-1.5 h-3.5 w-3.5", mFetching && "animate-spin")} />
                  Refresh
                </Button>
              </div>
            </div>

            {mLoading ? (
              <div className="flex justify-center py-8">
                <Spinner />
              </div>
            ) : messages.length === 0 && deletedMessages.size === 0 ? (
              <div className="py-4">
                <EmptyState
                  icon={<MessagesSquare className="h-8 w-8" />}
                  title="No messages"
                  description="The queue is empty."
                  action={
                    <Button size="sm" onClick={() => setShowSend(true)}>
                      <Plus className="mr-1.5 h-3.5 w-3.5" />
                      Send Message
                    </Button>
                  }
                />
              </div>
            ) : (
              // ResourceTable didn't fit because of the ghost rows: the 30s
              // tombstones are synthetic rows deliberately *not* in
              // `query.data`, which is what the Archetype-E ghost-tracker kernel
              // exists to show. Folding them into the data array would make them
              // sortable and countable as if they were still in the queue, so
              // this table's row set is not the query's — which is the one thing
              // `ResourceTable` cannot be told. The other two reasons wave C
              // recorded are gone: `rowClassName` carries the in-flight dim and
              // the tombstone strike-through, and `expandedContent` renders the
              // per-message panel (#1327). `virtualize` would carry the volume,
              // but the row set is not a volume problem. The subscriptions
              // sub-table on this same page *is* a resource list and is
              // converted.
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-8" />
                    <TableHead>Message ID</TableHead>
                    <TableHead>Body</TableHead>
                    <TableHead>Receive Count</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {messages.map((msg) => (
                    <MessageRow
                      key={msg.receiptHandle || msg.messageId}
                      msg={msg}
                      expanded={expandedMsg === msg.messageId}
                      onToggle={() =>
                        setExpandedMsg((prev) =>
                          prev === msg.messageId ? undefined : msg.messageId,
                        )
                      }
                      onDelete={() => setDeleteTarget(msg)}
                      onRedrive={() => handleRedriveMessage(msg)}
                      redriving={
                        redriveMsgMut.isPending &&
                        redriveMsgMut.variables.messageId === msg.messageId
                      }
                      onInflightExpired={invalidateMessages}
                    />
                  ))}
                  {/* Ghost rows: recently deleted messages, shown crossed-out for 30s */}
                  {Array.from(deletedMessages.values())
                    .filter((e) => !messages.some((m) => m.messageId === e.message.messageId))
                    .map((e) => (
                      <MessageRow
                        key={`gone-${e.message.messageId}`}
                        msg={e.message}
                        tombstone={e.reason}
                        expanded={false}
                        onToggle={() => {}}
                        onDelete={() => {}}
                        onRedrive={() => {}}
                        redriving={false}
                        onInflightExpired={() => {}}
                      />
                    ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        )}

        {/* Subscriptions tab */}
        {activeTab === "subscriptions" && (
          <CardContent className="p-0">
            <div className="flex items-center justify-between border-b border-border px-4 py-2">
              <span className="text-xs text-fg-muted">Topics that deliver to this queue</span>
              <Button size="sm" variant="ghost" onClick={() => setShowSubscribe(true)}>
                <Link className="mr-1.5 h-3.5 w-3.5" />
                Subscribe to Topic
              </Button>
            </div>

            <ResourceTable
              variant="embedded"
              query={{ data: subscriptions, isLoading: sLoading, error: sError }}
              noun="subscriptions"
              emptyIcon={Bell}
              emptyTitle="No subscriptions"
              emptyDescription="This queue is not subscribed to any SNS topic."
              emptyAction={
                <Button size="sm" onClick={() => setShowSubscribe(true)}>
                  <Plus className="mr-1.5 h-3.5 w-3.5" />
                  Subscribe to Topic
                </Button>
              }
              errorTitle="Failed to load subscriptions"
              // The topic is what identifies a row here — the queue is fixed.
              defaultSort={{ id: "topic-arn", desc: false }}
              rowKey={(sub) => sub.SubscriptionArn ?? ""}
              columns={[
                {
                  id: "topic-arn",
                  header: "Topic ARN",
                  sortValue: (sub) => sub.TopicArn,
                  cell: (sub) => <ArnLink arn={sub.TopicArn ?? ""} />,
                },
                {
                  header: "Protocol",
                  sortValue: (sub) => sub.Protocol,
                  cell: (sub) => <Badge variant="default">{sub.Protocol}</Badge>,
                },
                {
                  header: "Subscription ARN",
                  cellClassName: "max-w-xs truncate text-fg-muted",
                  cell: (sub) => (
                    <ArnLink arn={sub.SubscriptionArn ?? ""} className="text-fg-muted" />
                  ),
                },
              ]}
              rowActions={(sub) => (
                <RowAction
                  label={`Unsubscribe from ${sub.TopicArn?.split(":").pop() ?? "topic"}`}
                  tone="danger"
                  onClick={() => setUnsubscribeTarget(sub)}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </RowAction>
              )}
            />
          </CardContent>
        )}

        {/* Monitor tab */}
        {activeTab === "monitor" && (
          <CardContent className="p-4">
            <MonitorPanel
              range={monitorRange}
              onRangeChange={setMonitorRange}
              refreshIntervalMs={monitorRefreshMs}
              onRefreshIntervalChange={setMonitorRefreshMs}
              isLoading={metricsQuery.isLoading}
              isFetching={metricsQuery.isFetching}
              error={metricsQuery.error}
              data={metricsQuery.data}
              cards={SQS_MONITOR_CARDS}
              onRefresh={() => void metricsQuery.refetch()}
            />
          </CardContent>
        )}
      </Card>

      {/* ── Send Message dialog ── */}
      <SendMessageDialog
        queueName={queueName}
        isFifo={queueName.endsWith(".fifo")}
        open={showSend}
        onClose={() => setShowSend(false)}
      />

      {/* ── Delete message confirm ── */}
      <Dialog open={!!deleteTarget} onOpenChange={(v) => !v && setDeleteTarget(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Message</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-fg-muted">
            Permanently delete message{" "}
            <span className="font-mono text-xs">{deleteTarget?.messageId}</span>? This cannot be
            undone.
          </p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteTarget(undefined)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() => deleteTarget && deleteMsg.mutate(deleteTarget.receiptHandle)}
              disabled={deleteMsg.isPending}
            >
              {deleteMsg.isPending && <Spinner className="mr-2" />}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── Purge confirm ── */}
      <Dialog open={purgeConfirm} onOpenChange={(v) => !v && setPurgeConfirm(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Purge Queue</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-fg-muted">
            Delete <strong>all messages</strong> from <strong>{queueName}</strong>? This cannot be
            undone.
          </p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setPurgeConfirm(false)}>
              Cancel
            </Button>
            <Button variant="danger" onClick={() => purge.mutate()} disabled={purge.isPending}>
              {purge.isPending && <Spinner className="mr-2" />}
              Purge Queue
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── Delete queue confirm ── */}
      <Dialog open={deleteQueueConfirm} onOpenChange={(v) => !v && setDeleteQueueConfirm(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Queue</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-fg-muted">
            Permanently delete queue <strong>{queueName}</strong> and all its messages? This cannot
            be undone.
          </p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteQueueConfirm(false)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() => deleteQueueMut.mutate(queueName)}
              disabled={deleteQueueMut.isPending}
            >
              {deleteQueueMut.isPending && <Spinner className="mr-2" />}
              Delete Queue
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── Edit Config dialog ── */}
      <EditConfigDialog
        queue={queue}
        open={showEditConfig}
        onClose={() => setShowEditConfig(false)}
        onSave={(attrs) => updateConfigMut.mutate(attrs)}
        isPending={updateConfigMut.isPending}
      />

      {/* ── Subscribe to Topic dialog ── */}
      <SubscribeDialog
        open={showSubscribe}
        onClose={() => setShowSubscribe(false)}
        queueArn={queueArn}
        topics={allTopics}
        onSubscribe={handleSubscribe}
        isPending={createTopicMut.isPending || subscribeMut.isPending}
      />

      {/* ── Unsubscribe confirm ── */}
      <Dialog
        open={!!unsubscribeTarget}
        onOpenChange={(v) => !v && setUnsubscribeTarget(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Unsubscribe</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-fg-muted">
            Remove subscription{" "}
            <span className="font-mono text-xs">{unsubscribeTarget?.SubscriptionArn}</span>? The
            queue will stop receiving messages from this topic.
          </p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setUnsubscribeTarget(undefined)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() =>
                unsubscribeTarget && unsubscribeMut.mutate(unsubscribeTarget.SubscriptionArn ?? "")
              }
              disabled={unsubscribeMut.isPending}
            >
              {unsubscribeMut.isPending && <Spinner className="mr-2" />}
              Unsubscribe
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// ─── MessageRow ──────────────────────────────────────────────────────────────

function InflightCountdown({
  visibleAfter,
  onExpired,
}: {
  visibleAfter: number
  onExpired?: () => void
}) {
  const secondsLeft = useCountdown(visibleAfter, onExpired)

  if (secondsLeft <= 0) return <Badge variant="warning">in-flight · visible soon</Badge>
  return <Badge variant="warning">in-flight · {secondsLeft}s</Badge>
}

function DelayedCountdown({
  visibleAfter,
  onExpired,
}: {
  visibleAfter: number
  onExpired?: () => void
}) {
  const secondsLeft = useCountdown(visibleAfter, onExpired)

  if (secondsLeft <= 0) return <Badge variant="info">delayed · visible soon</Badge>
  return <Badge variant="info">delayed · {secondsLeft}s</Badge>
}

function MessageRow({
  msg,
  tombstone,
  expanded,
  onToggle,
  onDelete,
  onRedrive,
  redriving,
  onInflightExpired,
}: {
  msg: SQSMessage
  /** Set on a ghost row for a message that has just left the queue. */
  tombstone?: TombstoneReason
  expanded: boolean
  onToggle: () => void
  onDelete: () => void
  onRedrive: () => void
  redriving: boolean
  onInflightExpired: () => void
}) {
  const deleted = tombstone !== undefined
  const hasAttrs = Object.keys(msg.messageAttributes).length > 0
  const receiveCount = parseInt(msg.attributes.ApproximateReceiveCount ?? "0", 10)

  // Only messages that arrived here by dead-lettering carry
  // DeadLetterSourceQueueArn, and it is the only record of where they came
  // from — so it is exactly the set that can be sent back. A message produced
  // straight onto this queue has no source to return to.
  const sourceQueueArn = msg.attributes.DeadLetterSourceQueueArn
  const canRedrive = !deleted && !!sourceQueueArn

  return (
    <>
      <TableRow
        className={cn(msg.inflight && "opacity-50", deleted && "opacity-40")}
        onClick={deleted ? undefined : onToggle}
      >
        <TableCell>
          {tombstone === "redriven" ? (
            <Undo2 className="h-3.5 w-3.5 text-fg-muted" />
          ) : tombstone === "deleted" ? (
            <Trash2 className="h-3.5 w-3.5 text-fg-muted" />
          ) : expanded ? (
            <ChevronDown className="h-3.5 w-3.5 text-fg-muted" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 text-fg-muted" />
          )}
        </TableCell>
        <TableCell
          className={cn("w-72 font-mono text-xs", deleted && "text-fg-muted line-through")}
        >
          {msg.messageId}
        </TableCell>
        <TableCell
          className={cn("max-w-xs truncate text-sm", deleted && "text-fg-muted line-through")}
        >
          {msg.body.length > 120 ? msg.body.slice(0, 120) + "…" : msg.body}
        </TableCell>
        <TableCell>
          <Badge variant={receiveCount > 1 ? "warning" : "default"}>{receiveCount}</Badge>
        </TableCell>
        <TableCell>
          {tombstone === "redriven" ? (
            <Badge variant="info">returned to source</Badge>
          ) : tombstone === "deleted" ? (
            <Badge variant="danger">deleted</Badge>
          ) : msg.delayed && msg.visibleAfter > 0 ? (
            <DelayedCountdown visibleAfter={msg.visibleAfter} onExpired={onInflightExpired} />
          ) : msg.inflight && msg.visibleAfter > 0 ? (
            <InflightCountdown visibleAfter={msg.visibleAfter} onExpired={onInflightExpired} />
          ) : (
            <Badge variant="default">visible</Badge>
          )}
        </TableCell>
        <TableCell>
          {!deleted && (
            <div className="flex items-center justify-end">
              {canRedrive && (
                <Button
                  size="icon"
                  variant="ghost"
                  className="text-fg-muted hover:text-accent"
                  title={`Return to ${queueNameFromArn(sourceQueueArn)}`}
                  aria-label={`Return message to ${queueNameFromArn(sourceQueueArn)}`}
                  disabled={redriving}
                  onClick={(e) => {
                    e.stopPropagation()
                    onRedrive()
                  }}
                >
                  {redriving ? (
                    <Spinner className="h-3.5 w-3.5" />
                  ) : (
                    <Undo2 className="h-3.5 w-3.5" />
                  )}
                </Button>
              )}
              <Button
                size="icon"
                variant="ghost"
                className="text-fg-muted hover:text-danger"
                title="Delete message"
                aria-label="Delete message"
                onClick={(e) => {
                  e.stopPropagation()
                  onDelete()
                }}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            </div>
          )}
        </TableCell>
      </TableRow>
      {expanded && (
        <TableRow>
          <TableCell colSpan={6} className="bg-bg-muted/30 px-4 py-3">
            <div className="flex flex-col gap-3">
              <div className="flex flex-col gap-1">
                <span className="font-mono text-xs font-medium text-fg-muted">Message ID</span>
                <span className="font-mono text-xs text-fg">{msg.messageId}</span>
              </div>
              <div className="flex flex-col gap-1">
                <span className="font-mono text-xs font-medium text-fg-muted">Receipt Handle</span>
                <span className="max-w-full truncate font-mono text-xs text-fg-muted">
                  {msg.receiptHandle}
                </span>
              </div>
              <div className="flex flex-col gap-1">
                <span className="font-mono text-xs font-medium text-fg-muted">Body</span>
                <CodeBlock>{msg.body}</CodeBlock>
              </div>
              {sourceQueueArn && (
                <div className="flex flex-col gap-1">
                  <span className="font-mono text-xs font-medium text-fg-muted">Source Queue</span>
                  <ArnLink arn={sourceQueueArn} />
                </div>
              )}
              {msg.attributes.SentTimestamp && (
                <div className="flex flex-col gap-1">
                  <span className="font-mono text-xs font-medium text-fg-muted">Sent</span>
                  <span className="text-xs text-fg">
                    {new Date(parseInt(msg.attributes.SentTimestamp, 10)).toISOString()}
                  </span>
                </div>
              )}
              {hasAttrs && (
                <div className="flex flex-col gap-1.5">
                  <span className="font-mono text-xs font-medium text-fg-muted">
                    Message Attributes
                  </span>
                  <div className="grid grid-cols-[auto_auto_1fr] gap-x-4 gap-y-1 text-xs">
                    <span className="font-medium text-fg-subtle">Name</span>
                    <span className="font-medium text-fg-subtle">Type</span>
                    <span className="font-medium text-fg-subtle">Value</span>
                    {Object.entries(msg.messageAttributes).map(([k, v]) => (
                      <>
                        <span key={`${k}-name`} className="font-mono text-fg">
                          {k}
                        </span>
                        <span key={`${k}-type`} className="text-fg-muted">
                          {v.dataType}
                        </span>
                        <span key={`${k}-val`} className="text-fg">
                          {v.stringValue}
                        </span>
                      </>
                    ))}
                  </div>
                </div>
              )}
            </div>
          </TableCell>
        </TableRow>
      )}
    </>
  )
}

// ─── Small helpers ────────────────────────────────────────────────────────────

function StatCard({
  label,
  value,
  variant = "default",
}: {
  label: string
  value: string | number
  variant?: "default" | "accent"
}) {
  return (
    <div className="rounded-lg border border-border bg-bg-muted p-4">
      <p className="text-xs font-medium text-fg-muted">{label}</p>
      <p
        className={cn(
          "mt-1 font-mono text-2xl font-semibold tabular-nums",
          value === "…"
            ? "animate-pulse text-fg-muted"
            : variant === "accent"
              ? "text-accent"
              : "text-fg",
        )}
      >
        {value}
      </p>
    </div>
  )
}

/** "arn:aws:sqs:us-east-1:000000000000:orders" → "orders". */
function queueNameFromArn(arn: string): string {
  return arn.split(":").pop() || arn
}

/** "http://host/000000000000/orders" → "orders". */
function queueNameFromUrl(url: string): string {
  return url.split("/").pop() || url
}

function formatRetention(seconds: number): string {
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h`
  return `${Math.round(seconds / 86400)}d`
}

function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(0)} MiB`
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(0)} KiB`
  return `${bytes} B`
}

// ─── EditConfigDialog ───────────────────────────────────────────────────────

function EditConfigDialog({
  queue,
  open,
  onClose,
  onSave,
  isPending,
}: {
  queue: SQSQueueDetail
  open: boolean
  onClose: () => void
  onSave: (attrs: {
    visibilityTimeout?: number
    messageRetentionPeriod?: number
    receiveMessageWaitTimeSeconds?: number
    delaySeconds?: number
    redrivePolicy?: { deadLetterTargetArn: string; maxReceiveCount: number } | null
  }) => void
  isPending: boolean
}) {
  const [visibilityTimeout, setVisibilityTimeout] = useState(String(queue.visibilityTimeout))
  const [retentionPeriod, setRetentionPeriod] = useState(String(queue.messageRetentionPeriod))
  const [waitTime, setWaitTime] = useState(String(queue.receiveMessageWaitTimeSeconds))
  const [delay, setDelay] = useState(String(queue.delaySeconds))
  const [dlqArn, setDlqArn] = useState(queue.redrivePolicy?.deadLetterTargetArn ?? "")
  const [maxReceiveCount, setMaxReceiveCount] = useState(
    String(queue.redrivePolicy?.maxReceiveCount ?? 3),
  )

  // Sync form fields when queue data changes (e.g. after a successful save).
  // Uses "adjust state during render" to avoid an extra render pass from useEffect.
  const [prevQueue, setPrevQueue] = useState(queue)
  if (queue !== prevQueue) {
    setPrevQueue(queue)
    setVisibilityTimeout(String(queue.visibilityTimeout))
    setRetentionPeriod(String(queue.messageRetentionPeriod))
    setWaitTime(String(queue.receiveMessageWaitTimeSeconds))
    setDelay(String(queue.delaySeconds))
    setDlqArn(queue.redrivePolicy?.deadLetterTargetArn ?? "")
    setMaxReceiveCount(String(queue.redrivePolicy?.maxReceiveCount ?? 3))
  }

  function handleSave() {
    const trimmedArn = dlqArn.trim()
    const rp =
      trimmedArn.length > 0
        ? { deadLetterTargetArn: trimmedArn, maxReceiveCount: parseInt(maxReceiveCount, 10) || 3 }
        : null
    onSave({
      visibilityTimeout: parseInt(visibilityTimeout, 10),
      messageRetentionPeriod: parseInt(retentionPeriod, 10),
      receiveMessageWaitTimeSeconds: parseInt(waitTime, 10),
      delaySeconds: parseInt(delay, 10),
      redrivePolicy: rp,
    })
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit Queue Configuration</DialogTitle>
        </DialogHeader>

        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="ec-vt">Visibility Timeout (seconds)</Label>
            <Input
              id="ec-vt"
              type="number"
              min={0}
              max={43200}
              value={visibilityTimeout}
              onChange={(e) => setVisibilityTimeout(e.target.value)}
            />
            <p className="text-xs text-fg-muted">
              0–43200 s. How long a received message is hidden.
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="ec-ret">Message Retention (seconds)</Label>
            <Input
              id="ec-ret"
              type="number"
              min={60}
              max={1209600}
              value={retentionPeriod}
              onChange={(e) => setRetentionPeriod(e.target.value)}
            />
            <p className="text-xs text-fg-muted">60–1209600 s (1 min – 14 days).</p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="ec-wait">Receive Message Wait Time (seconds)</Label>
            <Input
              id="ec-wait"
              type="number"
              min={0}
              max={20}
              value={waitTime}
              onChange={(e) => setWaitTime(e.target.value)}
            />
            <p className="text-xs text-fg-muted">0 = short polling, 1–20 = long polling.</p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="ec-delay">Delivery Delay (seconds)</Label>
            <Input
              id="ec-delay"
              type="number"
              min={0}
              max={900}
              value={delay}
              onChange={(e) => setDelay(e.target.value)}
            />
            <p className="text-xs text-fg-muted">0–900 s. Delay before new messages are visible.</p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="ec-dlq">Dead Letter Queue</Label>
            <ResourceArnCombobox
              id="ec-dlq"
              resourceType="sqs"
              value={dlqArn}
              onChange={setDlqArn}
              placeholder="Select a queue or leave blank to remove"
              filterItems={(item) => item.arn !== queue.arn}
            />
            <p className="text-xs text-fg-muted">
              SQS queue to receive messages that exceed the max receive count. Leave blank to
              remove.
            </p>
          </div>

          {dlqArn.trim().length > 0 && (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="ec-mrc">Max Receive Count</Label>
              <Input
                id="ec-mrc"
                type="number"
                min={1}
                max={1000}
                value={maxReceiveCount}
                onChange={(e) => setMaxReceiveCount(e.target.value)}
              />
              <p className="text-xs text-fg-muted">
                1–1000. Move message to DLQ after this many receives.
              </p>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={handleSave} disabled={isPending}>
            {isPending && <Spinner className="mr-2" />}
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ─── SubscribeDialog ──────────────────────────────────────────────────────────

type TopicMode = "existing" | "new"

const TOPIC_MODES = [
  { value: "existing", label: "Existing topic" },
  { value: "new", label: "New topic" },
] as const satisfies readonly SegmentedOption<TopicMode>[]

const subscribeSchema = z.object({
  value: z.string().min(1, "Required"),
})

function SubscribeDialog({
  open,
  onClose,
  queueArn,
  topics,
  onSubscribe,
  isPending,
}: {
  open: boolean
  onClose: () => void
  queueArn: string
  topics: SNSTopic[]
  onSubscribe: (topicName: string, isNew: boolean) => void
  isPending: boolean
}) {
  const [mode, setMode] = useState<TopicMode>("existing")

  const form = useForm({
    validators: { onChange: subscribeSchema },
    defaultValues: { value: "" },
    onSubmit: ({ value }) => onSubscribe(value.value.trim(), mode === "new"),
  })

  function handleModeChange(newMode: TopicMode) {
    setMode(newMode)
    void form.setFieldValue("value", "")
  }

  function handleClose() {
    onClose()
    setTimeout(() => {
      setMode("existing")
      form.reset()
    }, 150)
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Subscribe Queue to SNS Topic</DialogTitle>
        </DialogHeader>

        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            e.stopPropagation()
            void form.handleSubmit()
          }}
        >
          <SegmentedControl
            label="Topic"
            value={mode}
            options={TOPIC_MODES}
            onChange={handleModeChange}
            size="md"
            className="self-start"
          />

          <form.Field name="value" validators={{ onChange: subscribeSchema.shape.value }}>
            {(field) =>
              mode === "existing" ? (
                <div className="flex flex-col gap-1.5">
                  <Label>Topic</Label>
                  {topics.length === 0 ? (
                    <p className="text-xs text-fg-muted">No topics found. Create one first.</p>
                  ) : (
                    <select
                      className="rounded-md border border-border bg-bg px-3 py-1.5 text-sm text-fg focus:ring-2 focus:ring-accent focus:outline-none"
                      value={field.state.value}
                      onChange={(e) => field.handleChange(e.target.value)}
                    >
                      <option value="">— select topic —</option>
                      {topics.map((t) => {
                        const tName = t.TopicArn?.split(":").pop() ?? ""
                        return (
                          <option key={t.TopicArn} value={tName}>
                            {tName}
                          </option>
                        )
                      })}
                    </select>
                  )}
                </div>
              ) : (
                <div className="flex flex-col gap-1.5">
                  <Label>New Topic Name</Label>
                  <Input
                    placeholder="my-topic"
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                    autoFocus
                  />
                  <p className="text-xs text-fg-muted">
                    The topic will be created and this queue subscribed automatically.
                  </p>
                </div>
              )
            }
          </form.Field>

          <div className="flex flex-col gap-0.5">
            <Label className="text-fg-muted">Queue ARN</Label>
            <span className="font-mono text-xs text-fg-muted">{queueArn}</span>
          </div>

          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <form.Subscribe selector={(s) => [s.canSubmit, s.isSubmitting]}>
              {([canSubmit, isSubmitting]) => (
                <Button type="submit" disabled={!canSubmit || isPending}>
                  {(isSubmitting || isPending) && <Spinner className="mr-2" />}
                  Subscribe
                </Button>
              )}
            </form.Subscribe>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
