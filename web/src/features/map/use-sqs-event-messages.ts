/**
 * useSqsEventMessages — event-driven SQS message state for the topology map.
 *
 * Replaces the 1-second polling approach with:
 *   1. ONE initial fetch of messages when the queue node mounts
 *   2. SSE events applied locally to keep the list current
 *   3. No periodic refetching — the event stream IS the source of truth
 *
 * Message bodies are NOT loaded here; they are fetched on-demand when
 * the user clicks a message to inspect it.
 */

import { useEffect, useMemo, useRef, useState } from "react"
import { useQuery, queryOptions, useQueryClient } from "@tanstack/react-query"
import { sqs } from "@/services/api"
import { sqsKeys } from "@/features/sqs/data"
import { useEventStream } from "@/hooks/use-event-stream"
import { EventType } from "@/services/event-types"
import type { SQSMessage } from "@/types"

interface SQSMessageEventPayload {
  queueName: string
  messageId: string
  visibleAfter?: number
  approximateReceiveCount?: number
}

/**
 * Lightweight message record maintained from SSE events.
 * Contains all the data needed for the map inline list — no body.
 */
export interface EventMessage {
  messageId: string
  approximateReceiveCount: number
  visibleAfter: number
  inflight: boolean
  delayed: boolean
  sentTimestamp: number
}

/**
 * Adapt an EventMessage to the SQSMessage shape expected by the map's
 * visual message and ghost tracker systems.  Body-dependent fields
 * are left empty — they are populated on-demand when the user clicks.
 */
export function eventMessageToSQSMessage(em: EventMessage): SQSMessage {
  return {
    messageId: em.messageId,
    receiptHandle: "",
    body: "",
    md5OfBody: "",
    attributes: {
      SentTimestamp: String(em.sentTimestamp),
      ApproximateReceiveCount: String(em.approximateReceiveCount),
    },
    messageAttributes: {},
    inflight: em.inflight,
    delayed: em.delayed,
    visibleAfter: em.visibleAfter,
    approximateReceiveCount: em.approximateReceiveCount,
  }
}

function sqsMapPeekQueryOptions(queueName: string) {
  return queryOptions({
    queryKey: [...sqsKeys.mapPeek(), queueName, "initial"],
    queryFn: () => sqs.receiveMessages(queueName),
    // Every ServiceNode calls this hook; only queue nodes pass a name. The
    // others must not each fire a request for a queue called "".
    enabled: queueName !== "",
    staleTime: Infinity, // never re-fetch automatically
    gcTime: 5 * 60_000,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
}

/**
 * Hook that provides an event-driven message list for a single SQS queue.
 *
 * - On mount, fetches the current messages once (the "snapshot").
 * - After that, SSE events maintain the list without any polling.
 * - Returns SQSMessage[] compatible with the existing visual message system.
 */
export function useSqsEventMessages(queueName: string): SQSMessage[] {
  const { events: sqsEvents, connected } = useEventStream({ source: "sqs" })
  const queryClient = useQueryClient()

  // Ref-based message registry: messageId → EventMessage
  const registry = useRef<Map<string, EventMessage>>(new Map())
  // Track which SSE events we've already processed
  const eventCursor = useRef(0)
  const [messages, setMessages] = useState<SQSMessage[]>([])
  const syncMessages = () => {
    setMessages(Array.from(registry.current.values(), eventMessageToSQSMessage))
  }

  // Track previous connection state for reconnect detection
  const prevConnected = useRef<boolean>(connected)

  // ONE initial fetch — populates the registry with current queue state.
  const mapPeekQuery = useMemo(() => sqsMapPeekQueryOptions(queueName), [queueName])
  const { data: initialMessages } = useQuery(mapPeekQuery)

  // Seed the registry from the initial fetch (only once per distinct snapshot).
  const seededRef = useRef(false)
  useEffect(() => {
    if (!initialMessages || seededRef.current) return
    seededRef.current = true
    const now = Date.now()
    for (const m of initialMessages) {
      registry.current.set(m.messageId, {
        messageId: m.messageId,
        approximateReceiveCount: m.approximateReceiveCount,
        visibleAfter: m.visibleAfter,
        inflight: m.inflight,
        delayed: m.delayed,
        sentTimestamp: Number(m.attributes["SentTimestamp"] ?? now),
      })
    }
    syncMessages()
  }, [initialMessages])

  // On SSE reconnect, refetch the snapshot to reconcile any missed events.
  useEffect(() => {
    if (prevConnected.current === false && connected === true) {
      // Connection restored — invalidate the initial fetch so it re-runs.
      seededRef.current = false
      registry.current.clear()
      eventCursor.current = 0
      void queryClient.invalidateQueries({
        queryKey: mapPeekQuery.queryKey,
      })
    }
    prevConnected.current = connected
  }, [connected, mapPeekQuery.queryKey, queryClient])

  // Apply SSE events incrementally to the registry.
  useEffect(() => {
    if (sqsEvents.length < eventCursor.current) {
      // Events were cleared (e.g. user cleared the stream) — reset cursor.
      eventCursor.current = 0
    }

    let changed = false
    for (let i = eventCursor.current; i < sqsEvents.length; i++) {
      const ev = sqsEvents[i]
      const payload = ev.payload as SQSMessageEventPayload | undefined
      if (!payload || payload.queueName !== queueName || !payload.messageId) continue

      const t = new Date(ev.time).getTime() || Date.now()

      if (ev.type === EventType.sqs.MessageSent) {
        registry.current.set(payload.messageId, {
          messageId: payload.messageId,
          approximateReceiveCount: 0,
          visibleAfter: payload.visibleAfter ?? 0,
          inflight: false,
          delayed: (payload.visibleAfter ?? 0) > t,
          sentTimestamp: t,
        })
        changed = true
      } else if (ev.type === EventType.sqs.MessageInflight) {
        const existing = registry.current.get(payload.messageId)
        registry.current.set(payload.messageId, {
          messageId: payload.messageId,
          approximateReceiveCount:
            payload.approximateReceiveCount ??
            (existing ? existing.approximateReceiveCount + 1 : 1),
          visibleAfter: payload.visibleAfter ?? 0,
          inflight: true,
          delayed: false,
          sentTimestamp: existing?.sentTimestamp ?? t,
        })
        changed = true
      } else if (ev.type === EventType.sqs.MessageVisible) {
        const existing = registry.current.get(payload.messageId)
        if (existing) {
          existing.inflight = false
          existing.delayed = false
          existing.visibleAfter = 0
          changed = true
        }
      } else if (ev.type === EventType.sqs.MessageDeleted) {
        if (registry.current.delete(payload.messageId)) {
          changed = true
        }
      } else if (ev.type === EventType.sqs.QueuePurged) {
        if (registry.current.size > 0) {
          registry.current.clear()
          changed = true
        }
      }
    }
    eventCursor.current = sqsEvents.length

    if (changed) syncMessages()
  }, [sqsEvents, queueName])

  return messages
}
