/**
 * InboxPage — two-panel capture inbox at /inbox.
 *
 * Left panel: scrollable list of captured messages (live-updated via SSE),
 * with a search bar and kind filter (All / Email / SMS).
 * Right panel: message detail view.
 *
 * Captures email (SMTP) and SMS deliveries from all services.
 */
import { useState, useMemo, useEffect, useCallback } from "react"
import { useQuery } from "@tanstack/react-query"
import { Search, Trash2, Inbox } from "lucide-react"
import { useToast } from "@/components/ui/toast"
import {
  inboxMessagesQueryOptions,
  inboxKeys,
  clearInboxMutationOptions,
  deleteInboxMessageMutationOptions,
} from "@/features/mail/data"
import { MessageList } from "@/features/mail/message-list"
import { MessageDetail } from "@/features/mail/message-detail"
import { useInboxReadState } from "@/features/mail/read-state"
import { PageHeader, Spinner } from "@/components/ui/primitives"
import { Button } from "@/components/ui/button"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { cn } from "@/lib/utils"
import type { MessageKind } from "@/types"
import { formatQuantity } from "@/lib/format"

type KindFilter = "all" | MessageKind
type ReadFilter = "all" | "unread"

// ─── Component ─────────────────────────────────────────────────────────────

export function InboxPage() {
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [search, setSearch] = useState("")
  const [kindFilter, setKindFilter] = useState<KindFilter>("all")
  const [readFilter, setReadFilter] = useState<ReadFilter>("all")

  const {
    data: allMessages = [],
    isLoading,
    isError,
    error,
  } = useQuery(inboxMessagesQueryOptions())
  const { toast } = useToast()
  const { unreadCount, isRead, markRead } = useInboxReadState(allMessages)

  useEffect(() => {
    if (!isError) return
    toast({
      title: "Failed to load inbox",
      // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- error may be null at runtime
      description: error?.message ?? "Could not fetch messages",
      variant: "danger",
    })
  }, [isError, error, toast])

  const clearMut = useResourceMutation({
    options: clearInboxMutationOptions(),
    invalidateKeys: [inboxKeys.all()],
    successTitle: "Inbox cleared",
    errorTitle: "Failed to clear inbox",
    onSuccess: () => setSelectedId(null),
  })

  const deleteMut = useResourceMutation({
    options: deleteInboxMessageMutationOptions(),
    invalidateKeys: [inboxKeys.messages()],
    successTitle: "Message deleted",
    errorTitle: "Failed to delete message",
    onSuccess: (_, id) => {
      if (selectedId === id) setSelectedId(null)
    },
  })

  // Client-side filtering — runs only when search/filter changes.
  const messages = useMemo(() => {
    let filtered = allMessages
    if (kindFilter !== "all") {
      filtered = filtered.filter((m) => m.kind === kindFilter)
    }
    if (readFilter === "unread") {
      filtered = filtered.filter((m) => m.id === selectedId || !isRead(m.id))
    }
    if (search.trim()) {
      const q = search.trim().toLowerCase()
      filtered = filtered.filter(
        (m) =>
          m.from.toLowerCase().includes(q) ||
          m.to.some((t) => t.toLowerCase().includes(q)) ||
          (m.subject ?? "").toLowerCase().includes(q) ||
          m.textBody.toLowerCase().includes(q),
      )
    }
    return filtered
  }, [allMessages, search, kindFilter, readFilter, selectedId, isRead])

  const selectedMessage = messages.find((m) => m.id === selectedId) ?? null

  const handleSelectMessage = useCallback(
    (id: string) => {
      markRead(id)
      setSelectedId(id)
    },
    [markRead],
  )

  // Keyboard shortcuts: j / ↓ → next message, k / ↑ → previous message,
  // Backspace / Delete → delete the selected message.
  useEffect(() => {
    const ids = messages.map((m) => m.id)
    const handle = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return
      if (e.key === "j" || e.key === "ArrowDown") {
        e.preventDefault()
        const idx = selectedId ? ids.indexOf(selectedId) : -1
        const next = ids[Math.min(idx + 1, ids.length - 1)]
        if (next != null) setSelectedId(next) // eslint-disable-line @typescript-eslint/no-unnecessary-condition -- defensive array index
      } else if (e.key === "k" || e.key === "ArrowUp") {
        e.preventDefault()
        const idx = selectedId ? ids.indexOf(selectedId) : ids.length
        const prev = ids[Math.max(idx - 1, 0)]
        if (prev != null) setSelectedId(prev) // eslint-disable-line @typescript-eslint/no-unnecessary-condition -- defensive array index
      } else if ((e.key === "Backspace" || e.key === "Delete") && selectedId) {
        e.preventDefault()
        deleteMut.mutate(selectedId)
      }
    }
    window.addEventListener("keydown", handle)
    return () => window.removeEventListener("keydown", handle)
  }, [messages, selectedId, deleteMut])

  const countLabel =
    messages.length === allMessages.length
      ? formatQuantity(allMessages.length, "message")
      : `${messages.length} of ${allMessages.length}`

  return (
    <div className="flex w-full flex-col gap-4">
      <PageHeader
        title="Inbox"
        description="Captured outbound messages — email, SMS, webhooks, and push — from all services."
        actions={
          <Button
            variant="ghost"
            size="sm"
            onClick={() => clearMut.mutate()}
            disabled={clearMut.isPending || allMessages.length === 0}
            className="text-fg-muted hover:text-danger"
          >
            <Trash2 className="h-3.5 w-3.5" />
            <span className="ml-1.5">Clear All</span>
          </Button>
        }
      />

      {/* Two-panel layout */}
      <div className="flex h-[calc(100vh-10rem)] overflow-hidden rounded-lg border border-border bg-bg-elevated">
        {/* Left: search + filter + message list */}
        <div className="flex w-80 shrink-0 flex-col border-r border-border">
          {/* Toolbar */}
          <div className="shrink-0 space-y-2 border-b border-border p-3">
            {/* Search input */}
            <div className="relative">
              <Search className="absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-fg-subtle" />
              <input
                type="search"
                placeholder="Search messages…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="w-full rounded-md border border-border bg-bg-muted py-1.5 pr-3 pl-8 text-sm text-fg placeholder:text-fg-subtle focus:border-accent focus:outline-none"
              />
            </div>

            {/* Kind filter pills */}
            <div className="flex flex-wrap items-center gap-1">
              {(["all", "unread"] as const).map((filter) => (
                <button
                  key={filter}
                  onClick={() => setReadFilter(filter)}
                  className={cn(
                    "rounded-full px-2.5 py-0.5 font-mono text-xs font-medium capitalize transition-colors",
                    readFilter === filter
                      ? "bg-accent text-fg-on-accent"
                      : "bg-bg-muted text-fg-muted hover:bg-accent-muted hover:text-accent",
                  )}
                >
                  {filter === "all"
                    ? "All messages"
                    : `Unread${unreadCount > 0 ? ` ${unreadCount}` : ""}`}
                </button>
              ))}
              <span className="mx-1 h-4 w-px bg-border" aria-hidden="true" />
              {(["all", "email", "sms", "webhook", "push"] as const).map((k) => (
                <button
                  key={k}
                  onClick={() => setKindFilter(k)}
                  className={cn(
                    "rounded-full px-2.5 py-0.5 font-mono text-xs font-medium capitalize transition-colors",
                    kindFilter === k
                      ? "bg-accent text-fg-on-accent"
                      : "bg-bg-muted text-fg-muted hover:bg-accent-muted hover:text-accent",
                  )}
                >
                  {k === "all"
                    ? "All types"
                    : k === "email"
                      ? "Email"
                      : k === "sms"
                        ? "SMS"
                        : k === "webhook"
                          ? "Webhook"
                          : "Push"}
                </button>
              ))}
              <span className="ml-auto text-xs text-fg-subtle">{countLabel}</span>
            </div>
          </div>

          {/* Message list */}
          <div className="min-h-0 flex-1 overflow-y-auto">
            {isLoading ? (
              <div className="flex h-full items-center justify-center">
                <Spinner />
              </div>
            ) : (
              <MessageList
                messages={messages}
                selectedId={selectedId}
                onSelect={handleSelectMessage}
              />
            )}
          </div>
        </div>

        {/* Right: message detail */}
        <div className="min-w-0 flex-1">
          <MessageDetail
            message={selectedMessage}
            onDelete={(id) => deleteMut.mutate(id)}
            deleting={deleteMut.isPending}
          />
        </div>
      </div>
    </div>
  )
}

// ─── Icon export (used by route) ───────────────────────────────────────────
export { Inbox as InboxIcon }
