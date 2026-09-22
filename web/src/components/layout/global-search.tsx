import { useCallback, useEffect, useRef, useState } from "react"
import { useNavigate, useRouterState } from "@tanstack/react-router"
import {
  Search,
  Star,
  X,
  ChevronRight,
  Clock,
  Loader2,
  LayoutDashboard,
  BookOpen,
} from "lucide-react"
import * as DialogPrimitive from "@radix-ui/react-dialog"
import { sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { useFavourites } from "@/hooks/use-favourites"
import { useSearch } from "@/hooks/use-search"
import { useServiceIconColor } from "@/hooks/use-service-icon-color"
import { PinButton } from "@/components/service/pin-button"
import { ServiceIconTile } from "@/components/service/service-icon-tile"
import {
  ALL_SERVICES,
  CATEGORY_LABELS,
  CATEGORY_ORDER,
  findServiceKeyForPathname,
  type ServiceDefinition,
} from "@/lib/nav-services"
import { matchesQuery, orderGroupsByActiveService, type SearchResult } from "@/lib/search"
import { CATALOG, type CatalogEntry } from "@/lib/unsupported-services"
import { Tooltip } from "@/components/ui/tooltip"

// ─── Types ─────────────────────────────────────────────────────────────────

interface GlobalSearchProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

// ─── Keyboard shortcut hook ────────────────────────────────────────────────
// eslint-disable-next-line react-refresh/only-export-components
export function useGlobalSearchShortcut(onOpen: () => void) {
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault()
        onOpen()
      }
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [onOpen])
}

// ─── Service card (used in mega menu) ─────────────────────────────────────

function ServiceCard({
  service,
  onSelect,
}: {
  service: ServiceDefinition
  onSelect: (service: ServiceDefinition) => void
}) {
  return (
    <div
      role="group"
      aria-label={service.label}
      className={cn(
        "relative flex flex-col gap-2 rounded-control border border-border bg-bg p-2 transition-colors",
        "hover:border-accent",
      )}
    >
      {/* Full-bleed click target keeps the star a sibling rather than a nested button. */}
      <button
        onClick={() => onSelect(service)}
        aria-label={service.label}
        className="absolute inset-0 cursor-pointer rounded-control focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none"
      />

      <div className="flex items-center justify-between">
        <ServiceIconTile service={service} size={26} iconSize={15} />
        {/* Above the card's full-bleed click target. */}
        <PinButton serviceKey={service.key} label={service.label} className="relative z-10" />
      </div>

      <div className="flex min-w-0 flex-col gap-px">
        <span className="truncate font-mono text-[12px] font-bold text-fg">{service.label}</span>
        <span className="truncate text-2xs leading-[14px] text-fg-subtle">
          {service.description}
        </span>
      </div>
    </div>
  )
}

// ─── Mega menu (shown when query is empty) ─────────────────────────────────

function MegaMenu({ onSelectService }: { onSelectService: (service: ServiceDefinition) => void }) {
  const { recentServices } = useFavourites()

  function renderCard(s: ServiceDefinition) {
    return <ServiceCard key={s.key} service={s} onSelect={onSelectService} />
  }

  // Recently used — limited to services in ALL_SERVICES
  const recentItems = recentServices
    .map((key) => ALL_SERVICES.find((s) => s.key === key))
    .filter((s): s is ServiceDefinition => s !== undefined)
    .slice(0, 6)

  return (
    <div>
      <div className="flex flex-col gap-6 p-4">
        {/* Recently used */}
        {recentItems.length > 0 && (
          <section className="flex flex-col gap-2">
            <p className={cn(sectionLabel, "flex items-center gap-1.5 text-fg-subtle")}>
              <Clock className="h-3 w-3" strokeWidth={1.75} />
              Recently Used
            </p>
            <div className="grid grid-cols-3 gap-2 sm:grid-cols-4 md:grid-cols-6">
              {recentItems.map(renderCard)}
            </div>
          </section>
        )}

        {/* Services by category */}
        {CATEGORY_ORDER.map((cat) => {
          const services = ALL_SERVICES.filter((s) => s.category === cat)
          if (services.length === 0) return null
          return (
            <section key={cat} className="flex flex-col gap-2">
              <h3 className="font-mono text-2xs font-bold text-fg-muted">
                {CATEGORY_LABELS[cat]}
              </h3>
              <div className="grid grid-cols-3 gap-2 sm:grid-cols-4 md:grid-cols-5">
                {services.map(renderCard)}
              </div>
            </section>
          )
        })}
      </div>

      {/* Hint */}
      <div className="border-t border-border px-4 py-3 text-xs text-fg-subtle">
        <Star className="mr-1 inline h-3 w-3 text-accent" fill="currentColor" strokeWidth={1.6} />
        Star a service to pin it to the sidebar
      </div>
    </div>
  )
}

// ─── Catalog chip (greyed-out entry for unsupported/stub services) ──────────

function CatalogChip({ entry, onSelect }: { entry: CatalogEntry; onSelect: () => void }) {
  return (
    <button
      onClick={onSelect}
      className={cn(
        "group relative flex shrink-0 items-center gap-2 rounded-lg border border-border-muted bg-bg px-3 py-2",
        "opacity-60 transition-colors hover:border-accent hover:opacity-100",
        "focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none",
      )}
    >
      <BookOpen className="h-4 w-4 shrink-0 text-fg-subtle" />
      <span className="text-sm font-medium whitespace-nowrap text-fg-muted">{entry.label}</span>
      <span className="shrink-0 rounded-full border border-border-muted px-1.5 py-0.5 text-2xs text-fg-subtle">
        {entry.tier === "stub" ? "Stub" : "Unsupported"}
      </span>
    </button>
  )
}

// ─── Search results (shown when query is non-empty) ────────────────────────

const searchOptionId = (index: number) => `global-search-option-${index}`

function ResultRow({
  result,
  index,
  isSelected,
  onSelect,
  onPointerMove,
}: {
  result: SearchResult
  index: number
  isSelected: boolean
  onSelect: (r: SearchResult) => void
  onPointerMove: () => void
}) {
  const service = ALL_SERVICES.find((s) => s.key === result.serviceKey)
  const isDocsResult = result.serviceKey === "/docs"
  const Icon = service?.icon ?? (isDocsResult ? BookOpen : LayoutDashboard)
  const { enabled: colorEnabled } = useServiceIconColor()

  return (
    // An option in the listbox the input controls, not a bare link: arrowing the
    // selection moves `aria-activedescendant` on the input, and only a real `option`
    // with an id can be pointed at. It stays an <a> so the href still opens in a new
    // tab on middle-click and ctrl-click.
    <a
      id={searchOptionId(index)}
      role="option"
      aria-selected={isSelected}
      href={result.href}
      onPointerMove={onPointerMove}
      onClick={(e) => {
        if (
          e.defaultPrevented ||
          e.button !== 0 ||
          e.metaKey ||
          e.ctrlKey ||
          e.shiftKey ||
          e.altKey
        ) {
          return
        }
        e.preventDefault()
        onSelect(result)
      }}
      className={cn(
        "flex w-full items-center gap-3 rounded-md px-3 py-2 text-left transition-colors",
        isSelected ? "bg-accent-muted" : "hover:bg-accent-muted",
      )}
    >
      <Icon
        className={cn(
          "h-4 w-4 shrink-0",
          colorEnabled && service?.color
            ? service.color
            : isDocsResult
              ? "text-accent"
              : "text-fg-muted",
        )}
      />
      <Tooltip
        content={
          <div className="space-y-1">
            <div className="font-medium break-all">{result.label}</div>
            {result.sublabel && (
              <div className="font-mono break-all text-fg-muted">{result.sublabel}</div>
            )}
          </div>
        }
      >
        {/* Every result is a resource identifier — name and ARN alike are mono. */}
        <div className="min-w-0 flex-1">
          <div className="truncate font-mono text-sm font-medium text-fg">{result.label}</div>
          {result.sublabel && (
            <div className="truncate font-mono text-xs text-fg-subtle">{result.sublabel}</div>
          )}
        </div>
      </Tooltip>
      <span className="shrink-0 rounded bg-bg-muted px-1.5 py-0.5 font-mono text-xs text-fg-subtle">
        {result.type}
      </span>
      {isSelected && <ChevronRight className="h-3.5 w-3.5 shrink-0 text-accent" />}
    </a>
  )
}

function SearchResults({
  groups,
  flat,
  isLoading,
  query,
  activeServiceKey,
  selectedIndex,
  onSelect,
  onSetSelectedIndex,
  onSelectService,
  onSelectCatalogEntry,
}: {
  /** Result groups in display order (active-service group already promoted). */
  groups: [serviceKey: string, items: SearchResult[]][]
  flat: SearchResult[]
  isLoading: boolean
  query: string
  activeServiceKey: string | undefined
  selectedIndex: number
  onSelect: (r: SearchResult) => void
  onSetSelectedIndex: (i: number) => void
  onSelectService: (service: ServiceDefinition) => void
  onSelectCatalogEntry: (id: string) => void
}) {
  const { enabled: colorEnabled } = useServiceIconColor()

  // Matching services, with the current route's service promoted to the front.
  const matchedServices = ALL_SERVICES.filter((s) =>
    matchesQuery(query, s.label, s.key, s.description),
  )
  const activeMatchIndex = activeServiceKey
    ? matchedServices.findIndex((s) => s.key === activeServiceKey)
    : -1
  if (activeMatchIndex > 0) {
    matchedServices.unshift(...matchedServices.splice(activeMatchIndex, 1))
  }

  const matchedCatalog = CATALOG.filter((e) => matchesQuery(query, e.label, e.id, e.description))

  if (isLoading) {
    return (
      <div className="flex items-center justify-center gap-2 px-4 py-10 text-sm text-fg-subtle">
        <Loader2 className="h-4 w-4 animate-spin" />
        Searching…
      </div>
    )
  }

  if (flat.length === 0 && matchedServices.length === 0 && matchedCatalog.length === 0) {
    return (
      <div className="px-4 py-10 text-center text-sm text-fg-subtle">
        No results for <span className="font-medium text-fg">"{query}"</span>
      </div>
    )
  }

  // Build sections with running total index for keyboard selection.
  // `groups` is already in display order, so indices match the flat list.
  const sections: Array<{ serviceKey: string; items: SearchResult[]; startIndex: number }> = []
  let runningIndex = 0
  for (const [serviceKey, items] of groups) {
    sections.push({ serviceKey, items, startIndex: runningIndex })
    runningIndex += items.length
  }

  return (
    <div className="py-2">
      {/* Matching services — horizontal scrollable strip */}
      {(matchedServices.length > 0 || matchedCatalog.length > 0) && (
        <div className="px-4 pb-3">
          <div className="mb-1.5 font-mono text-xs font-medium text-fg-subtle">Services</div>
          <div className="flex gap-2 overflow-x-auto pb-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
            {matchedServices.map((s) => {
              const Icon = s.icon
              return (
                // Two sibling buttons in a bordered row, not a pin nested inside the
                // service button: interactive content inside a <button> is invalid, and
                // the pin was `tabIndex={-1}`, so the only way to reach it was a mouse.
                <div
                  key={s.key}
                  className={cn(
                    "group relative flex shrink-0 items-center gap-2 rounded-lg border border-border bg-bg px-3 py-2",
                    "transition-colors focus-within:ring-2 focus-within:ring-accent",
                    // Cards and chips hover on the border alone — no fill change.
                    "hover:border-accent",
                  )}
                >
                  <button
                    type="button"
                    onClick={() => onSelectService(s)}
                    className="flex min-w-0 items-center gap-2 focus-visible:outline-none"
                  >
                    <Icon
                      aria-hidden
                      className={cn("h-4 w-4 shrink-0", colorEnabled ? s.color : "text-fg-muted")}
                    />
                    <span className="text-sm font-medium whitespace-nowrap text-fg">{s.label}</span>
                  </button>
                  <PinButton serviceKey={s.key} label={s.label} className="ml-0.5" />
                </div>
              )
            })}
            {matchedCatalog.map((entry) => (
              <CatalogChip
                key={entry.id}
                entry={entry}
                onSelect={() => onSelectCatalogEntry(entry.id)}
              />
            ))}
          </div>
        </div>
      )}

      {/* The listbox is exactly the options: the result count below is a live region,
          and a listbox may contain only options and groups of them. */}
      <div id="global-search-listbox" role="listbox" aria-label="Search results">
      {sections.map(({ serviceKey, items, startIndex }) => {
        const service = ALL_SERVICES.find((s) => s.key === serviceKey)
        const isDocsSection = serviceKey === "/docs"
        const Icon = service?.icon ?? (isDocsSection ? BookOpen : LayoutDashboard)
        const groupLabel = service?.label ?? (isDocsSection ? "Documentation" : serviceKey)
        return (
          <div
            key={serviceKey}
            role="group"
            aria-label={groupLabel}
            className="px-2 pb-3"
          >
            {/* Group header — named through the group above, so it is not announced twice. */}
            <div
              aria-hidden
              className="mb-1 flex items-center gap-1.5 px-1 py-1 text-xs font-medium text-fg-subtle"
            >
              <Icon
                className={cn(
                  "h-3.5 w-3.5",
                  colorEnabled && service?.color
                    ? service.color
                    : isDocsSection
                      ? "text-accent"
                      : "text-fg-muted",
                )}
              />
              {service?.label ?? (isDocsSection ? "Documentation" : serviceKey)}
              <span className="ml-auto rounded-full bg-bg-muted px-1.5 font-mono text-fg-subtle">
                {items.length}
              </span>
            </div>
            {items.map((result, i) => (
              <ResultRow
                key={result.id}
                result={result}
                index={startIndex + i}
                isSelected={selectedIndex === startIndex + i}
                onSelect={onSelect}
                onPointerMove={() => onSetSelectedIndex(startIndex + i)}
              />
            ))}
          </div>
        )
      })}

      </div>

      {/* A count and a key list — both machine text, both mono. The count doubles as
          the live region: typing narrows the list silently otherwise, and "how many did
          that find" is the one thing a search box has to answer out loud. */}
      {flat.length > 0 && (
        <div
          role="status"
          aria-live="polite"
          className="border-t border-border px-4 pt-2 font-mono text-xs text-fg-subtle"
        >
          {flat.length} result{flat.length !== 1 ? "s" : ""}
          <span className="float-right hidden sm:block">
            ↑↓ Navigate · Enter Select · Esc Close
          </span>
        </div>
      )}
    </div>
  )
}

// ─── GlobalSearch dialog ───────────────────────────────────────────────────

export function GlobalSearch({ open, onOpenChange }: GlobalSearchProps) {
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const { addRecentService } = useFavourites()
  const { query, setQuery, grouped, isLoading, clear } = useSearch()
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const isSearching = query.trim().length > 0

  // Context-aware ranking — promote the current route's service group to the
  // front. The flat list for keyboard navigation is derived from the same
  // ordered groups so arrow-key order always matches the rendered order.
  const activeServiceKey = findServiceKeyForPathname(pathname)
  const orderedGroups = orderGroupsByActiveService(grouped, activeServiceKey)
  const flat = orderedGroups.flatMap(([, items]) => items)

  // Reset selection at the event boundary that invalidates it (a query edit)
  // instead of an effect keyed on result-map identity — see AGENTS.md on
  // avoiding effects that synchronize state from other state.
  function handleQueryChange(q: string) {
    setQuery(q)
    setSelectedIndex(0)
  }

  // Focus input when opened
  useEffect(() => {
    if (open) setTimeout(() => inputRef.current?.focus(), 50)
    else clear()
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps -- clear is stable; intentionally only react to open/close transitions

  const handleClose = useCallback(() => {
    onOpenChange(false)
    clear()
  }, [onOpenChange, clear])

  const navigateTo = useCallback(
    (href: string, serviceKey: string) => {
      addRecentService(serviceKey)
      handleClose()
      void navigate({ to: href })
    },
    [navigate, addRecentService, handleClose],
  )

  function handleSelectResult(result: SearchResult) {
    navigateTo(result.href, result.serviceKey)
  }

  function handleSelectService(service: ServiceDefinition) {
    navigateTo(service.to, service.key)
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (!isSearching) return

    if (e.key === "ArrowDown") {
      e.preventDefault()
      setSelectedIndex((i) => Math.min(i + 1, flat.length - 1))
    } else if (e.key === "ArrowUp") {
      e.preventDefault()
      setSelectedIndex((i) => Math.max(i - 1, 0))
    } else if (e.key === "Enter" && flat[selectedIndex]) {
      e.preventDefault()
      handleSelectResult(flat[selectedIndex])
    }
  }

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        {/* Backdrop */}
        {/* Shared design-system dialog scrim — rgba(9,16,22,0.62), drawn flat. */}
        <DialogPrimitive.Overlay className="data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 fixed inset-0 z-50 bg-[rgba(9,16,22,0.62)]" />

        {/* Panel — top-aligned, full palette style */}
        <DialogPrimitive.Content
          className={cn(
            "fixed top-16 left-1/2 z-50 w-[820px] max-w-[calc(100vw-2rem)] -translate-x-1/2 overflow-hidden",
            "rounded-card border border-border bg-bg-elevated shadow-[0_32px_64px_rgba(0,0,0,0.45)]",
            "flex max-h-[720px] flex-col",
            "data-[state=open]:animate-in data-[state=closed]:animate-out",
            "data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
            "data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95",
          )}
          onKeyDown={onKeyDown}
        >
          <DialogPrimitive.Title className="sr-only">Global search</DialogPrimitive.Title>

          {/* Search bar — one flat row: icon, field, esc. No recessed pill. */}
          <div className="flex shrink-0 items-center gap-2.5 border-b border-border p-4">
            <Search className="h-4 w-4 shrink-0 text-fg-muted" strokeWidth={1.75} />
            {/* A combobox, not a bare text field: it owns a list of options and a
                highlighted one, and none of that is announced unless it says so.
                `aria-activedescendant` is what makes the arrow keys speak — focus stays
                in the input while the selection moves. */}
            <input
              ref={inputRef}
              value={query}
              onChange={(e) => handleQueryChange(e.target.value)}
              placeholder="Search services and resources…"
              aria-label="Search services and resources"
              role="combobox"
              aria-expanded={isSearching && flat.length > 0}
              aria-controls={isSearching && flat.length > 0 ? "global-search-listbox" : undefined}
              aria-activedescendant={
                isSearching && flat[selectedIndex] ? searchOptionId(selectedIndex) : undefined
              }
              aria-autocomplete="list"
              className="min-w-0 flex-1 bg-transparent font-mono text-sm text-fg placeholder:text-fg-muted focus-visible:outline-none"
              autoComplete="off"
              spellCheck={false}
            />
            {query && (
              <button
                onClick={() => handleQueryChange("")}
                aria-label="Clear search"
                className="shrink-0 rounded p-0.5 text-fg-subtle transition-colors hover:text-fg"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            )}
            <kbd className="shrink-0 rounded border border-border px-[7px] py-0.5 font-mono text-2xs text-fg-subtle">
              esc
            </kbd>
          </div>

          {/* Body */}
          <div className="min-h-0 flex-1 overflow-y-auto">
            {isSearching ? (
              <SearchResults
                groups={orderedGroups}
                flat={flat}
                isLoading={isLoading}
                query={query}
                activeServiceKey={activeServiceKey}
                selectedIndex={selectedIndex}
                onSelect={handleSelectResult}
                onSetSelectedIndex={setSelectedIndex}
                onSelectService={handleSelectService}
                onSelectCatalogEntry={(id) => {
                  addRecentService("/" + id)
                  handleClose()
                  void navigate({ to: ("/" + id) as "/" })
                }}
              />
            ) : (
              <MegaMenu onSelectService={handleSelectService} />
            )}
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}

// ─── Search trigger button (used in the header) ────────────────────────────

export function GlobalSearchTrigger({ onClick }: { onClick: () => void }) {
  return (
    <>
      {/* Full search bar — visible at @md and wider */}
      <button
        onClick={onClick}
        className={cn(
          "hidden @md:flex h-8 w-full max-w-[360px] min-w-0 items-center gap-2 rounded-md border border-border bg-bg px-2.5 font-mono text-xs text-fg-subtle",
          "md:max-w-[480px] xl:max-w-[640px]",
          "transition-colors hover:border-accent hover:text-fg",
          "focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none",
        )}
      >
        <Search className="h-3.5 w-3.5 shrink-0" />
        <span className="min-w-0 flex-1 truncate text-left">Search resources, ARNs, keys…</span>
        <kbd className="shrink-0 rounded border border-border bg-bg-subtle px-1.5 py-0.5 font-mono text-2xs">
          ⌘K
        </kbd>
      </button>

      {/* Icon-only trigger — visible below @md */}
      <button
        onClick={onClick}
        className={cn(
          "@md:hidden flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border bg-bg",
          "transition-colors hover:border-accent hover:text-fg",
          "focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none",
        )}
        aria-label="Search"
      >
        <Search className="h-3.5 w-3.5" />
      </button>
    </>
  )
}
