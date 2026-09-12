/**
 * The one tree every value in the debugger is drawn with — a scope's
 * variables in Locals, a watch's result, an object a console evaluation
 * returned (docs/plans/compute-debugger-console.md § 3.5). Rows are
 * `name: value`; a value with a handle expands on click or ArrowRight, and
 * its children are fetched then, once, through `loadChildren` — the tree
 * never asks for what nobody has opened. An accessor row shows `(getter)`
 * and stops there: reading it would run code in the paused program.
 *
 * Keyboard: a WAI-ARIA tree with every item focusable. Enter and Space
 * toggle; ArrowRight expands, or moves into the children when already
 * expanded; ArrowLeft collapses, or moves to the parent when already
 * collapsed; ArrowUp and ArrowDown walk the visible rows.
 *
 * Nothing here knows where a value came from: `RemoteValue` and `Property`
 * are the session's protocol-neutral shapes, and `loadChildren` is whatever
 * the panel hands over (in practice `session.getProperties`).
 */
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
} from "react"
import { ChevronRight } from "lucide-react"
import { cn } from "@/lib/utils"
import type { Property, RemoteValue } from "../session/session"

export interface TreeRoot {
  /** Stable identity within the tree. */
  key: string
  name: string
  /** The value line; `null` draws the name alone (a scope) or the error. */
  value: RemoteValue | null
  error?: string | null
  /** A muted aside in the value's place — "not available" while running. */
  note?: string | null
  /** Open on first render — the Local scope, a watch. */
  expanded?: boolean
}

export interface ValueTreeProps {
  roots: readonly TreeRoot[]
  loadChildren: (objectId: string) => Promise<Property[]>
  "aria-label": string
  className?: string
  /** Hover text for the whole tree — where a stale value came from, say. */
  title?: string
}

export function ValueTree({ roots, loadChildren, className, title, ...aria }: ValueTreeProps) {
  return (
    <ul
      role="tree"
      aria-label={aria["aria-label"]}
      title={title}
      className={cn("m-0 list-none p-0", className)}
    >
      {roots.map((root) => (
        <TreeNode
          key={root.key}
          name={root.name}
          value={root.value}
          error={root.error ?? null}
          note={root.note ?? null}
          accessor={false}
          internal={false}
          level={1}
          initiallyExpanded={root.expanded ?? false}
          loadChildren={loadChildren}
        />
      ))}
    </ul>
  )
}

// ─── Nodes ────────────────────────────────────────────────────────────────

interface TreeNodeProps {
  name: string
  value: RemoteValue | null
  error: string | null
  note: string | null
  accessor: boolean
  internal: boolean
  level: number
  initiallyExpanded: boolean
  loadChildren: (objectId: string) => Promise<Property[]>
}

function TreeNode({
  name,
  value,
  error,
  note,
  accessor,
  internal,
  level,
  initiallyExpanded,
  loadChildren,
}: TreeNodeProps) {
  const objectId = value?.objectId ?? null
  const expandable = objectId !== null
  const [expanded, setExpanded] = useState(initiallyExpanded && expandable)
  const [children, setChildren] = useState<Property[] | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  // Fetched on the first expansion and kept: a handle names one snapshot of
  // the value, so re-reading it on every open would only cost a round trip.
  // The ref, not state, records that the request is out, so the effect that
  // sends it sets nothing synchronously; "loading" is simply "open with no
  // rows and no error yet".
  const requested = useRef(false)
  const loading = expanded && children === null && loadError === null
  // The item's accessible name is its own row — not, as content naming
  // would have it, the row plus every open descendant.
  const rowId = useId()

  // Opening is what fetches — by click, by key, or by a root that starts open.
  useEffect(() => {
    if (!expanded || !objectId || requested.current) return
    requested.current = true
    loadChildren(objectId).then(
      (props) => setChildren(props),
      (err: unknown) => setLoadError(err instanceof Error ? err.message : String(err)),
    )
  }, [expanded, objectId, loadChildren])

  const toggle = useCallback(() => {
    if (expandable) setExpanded((open) => !open)
  }, [expandable])

  const onKeyDown = (e: KeyboardEvent<HTMLLIElement>) => {
    const item = e.currentTarget
    switch (e.key) {
      case "Enter":
      case " ":
        toggle()
        break
      case "ArrowRight":
        if (!expandable) return
        if (!expanded) toggle()
        else focusSibling(item, 1)
        break
      case "ArrowLeft":
        if (expandable && expanded) toggle()
        else focusParent(item)
        break
      case "ArrowDown":
        focusSibling(item, 1)
        break
      case "ArrowUp":
        focusSibling(item, -1)
        break
      default:
        return
    }
    e.preventDefault()
    e.stopPropagation()
  }

  // The item owns the click, and stops it there: a row inside an open node
  // must not also toggle the node it sits in.
  const onClick = (e: MouseEvent<HTMLLIElement>) => {
    e.stopPropagation()
    toggle()
  }

  return (
    <li
      role="treeitem"
      aria-level={level}
      aria-expanded={expandable ? expanded : undefined}
      aria-labelledby={rowId}
      tabIndex={0}
      onKeyDown={onKeyDown}
      onClick={onClick}
      className="group/row m-0 list-none p-0 outline-none"
    >
      <div
        id={rowId}
        style={{ paddingLeft: `${(level - 1) * 0.75}rem` }}
        className={cn(
          "flex min-w-0 items-start rounded-sm py-0.5 pr-1 font-mono text-2xs leading-relaxed",
          "group-focus-visible/row:ring-1 group-focus-visible/row:ring-accent",
          expandable && "cursor-pointer hover:bg-bg-muted",
        )}
      >
        <span aria-hidden className="flex h-4 w-3.5 shrink-0 items-center justify-center">
          {expandable && (
            <ChevronRight
              className={cn("h-3 w-3 text-fg-muted transition-transform", expanded && "rotate-90")}
            />
          )}
        </span>
        <span className="min-w-0 break-all">
          {name !== "" && (
            <span className={cn(internal ? "text-fg-subtle italic" : "token property")}>
              {name}
            </span>
          )}
          {name !== "" && (error || note || accessor || (value && value.description !== "")) && (
            <>
              <span className="token punctuation">:</span>{" "}
            </>
          )}
          {error ? (
            <span className="text-danger">{error}</span>
          ) : note ? (
            <span className="text-fg-subtle italic">{note}</span>
          ) : accessor ? (
            <span className="text-fg-subtle italic">(getter)</span>
          ) : value ? (
            <ValueText value={value} />
          ) : null}
        </span>
      </div>
      {expandable && expanded && (
        <ul role="group" className="m-0 list-none p-0" onClick={(e) => e.stopPropagation()}>
          {loading && (
            <li className="py-0.5 pl-6 font-mono text-2xs text-fg-muted italic">Loading…</li>
          )}
          {loadError && (
            <li role="alert" className="py-0.5 pl-6 font-mono text-2xs text-danger">
              {loadError}
            </li>
          )}
          {children?.length === 0 && (
            <li className="py-0.5 pl-6 font-mono text-2xs text-fg-muted italic">No properties</li>
          )}
          {children?.map((child, index) => (
            <TreeNode
              key={`${index}:${child.name}`}
              name={child.name}
              value={child.value}
              error={null}
              // Neither a value nor a getter: a `let`/`const` not yet
              // initialised at this point of the frame, which V8 reports
              // as nothing at all.
              note={child.value === null && !child.accessor ? "(unavailable)" : null}
              accessor={child.accessor}
              internal={child.internal}
              level={level + 1}
              initiallyExpanded={false}
              loadChildren={loadChildren}
            />
          ))}
        </ul>
      )}
    </li>
  )
}

// ─── Value text ───────────────────────────────────────────────────────────

/** The syntax-token class for a value's type, so a string reads as one wherever it is drawn. */
function tokenClass(value: RemoteValue): string {
  switch (value.type) {
    case "string":
      return "token string"
    case "number":
    case "bigint":
      return "token number"
    case "boolean":
      return "token boolean"
    case "undefined":
      return "token null keyword"
    case "function":
      return "token function"
    case "object":
      return value.subtype === "null" ? "token null keyword" : "text-fg"
    default:
      return "text-fg"
  }
}

export function ValueText({ value, className }: { value: RemoteValue; className?: string }) {
  return <span className={cn(tokenClass(value), className)}>{value.description}</span>
}

// ─── Focus movement ───────────────────────────────────────────────────────

function visibleItems(from: HTMLElement): HTMLElement[] {
  const tree = from.closest<HTMLElement>('[role="tree"]')
  return tree ? [...tree.querySelectorAll<HTMLElement>('[role="treeitem"]')] : [from]
}

function focusSibling(item: HTMLElement, delta: 1 | -1): void {
  const items = visibleItems(item)
  const at = items.indexOf(item) + delta
  if (at >= 0 && at < items.length) items[at].focus()
}

function focusParent(item: HTMLElement): void {
  item.parentElement?.closest<HTMLElement>('[role="treeitem"]')?.focus()
}
