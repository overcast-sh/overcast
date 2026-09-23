import { useId, useState, type ReactNode, type RefObject } from "react"
import { ChevronDown, ChevronUp, Search } from "lucide-react"
import { Button } from "@/components/ui/button"
import { CheckboxFilterDropdown } from "@/components/ui/checkbox-filter-dropdown"
import { Input } from "@/components/ui/input"
import { formatCount } from "@/lib/format"
import { MAX_MATCHES } from "./loaded-rows"
import type { GridColumns } from "./use-grid-columns"
import type { GridFind } from "./use-grid-find"

/**
 * The grid's toolbar: Find over the loaded rows, Go to row (`⌘G`), the
 * Columns menu, and whatever the caller puts at the end — *Query with
 * Athena*, *Open in viewer*.
 */
export function GridToolbar({
  find,
  goToRef,
  onGoTo,
  visibility,
  end,
}: {
  find: GridFind
  goToRef: RefObject<HTMLInputElement | null>
  /** Moves to a 1-based row; returns a note when it could not go exactly there. */
  onGoTo: (row: number) => string | null
  visibility: GridColumns["visibility"]
  end?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border px-3 py-2">
      <FindField find={find} />
      <GoToRowField inputRef={goToRef} onGoTo={onGoTo} />
      <div className="ml-auto flex items-center gap-2">
        {visibility.items.length > 1 && (
          <CheckboxFilterDropdown
            triggerLabel="Columns"
            model="hide"
            align="end"
            triggerClassName="h-8"
            items={visibility.items}
            selected={visibility.hidden}
            onToggle={visibility.toggle}
            onShowAll={visibility.showAll}
            onHideAll={visibility.hideAll}
          />
        )}
        {end}
      </div>
    </div>
  )
}

/**
 * Find searches the rows already loaded, and says so in its placeholder and
 * its count: anything more is a query (see *Query with Athena*).
 */
function FindField({ find }: { find: GridFind }) {
  const { query, setQuery, matches, index, step } = find
  const searching = query.trim() !== ""
  const total =
    matches.length >= MAX_MATCHES ? `${formatCount(MAX_MATCHES)}+` : formatCount(matches.length)
  return (
    <div className="flex min-w-0 flex-1 items-center gap-1 sm:flex-none">
      <div className="relative min-w-0 flex-1">
        <Search
          aria-hidden
          className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-fg-subtle"
        />
        <Input
          type="search"
          aria-label="Find in loaded rows"
          placeholder="Find in loaded rows"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key !== "Enter") return
            event.preventDefault()
            step(event.shiftKey ? -1 : 1)
          }}
          className="w-full pl-8 sm:w-56"
        />
      </div>
      {searching && (
        <>
          <span role="status" className="font-mono text-2xs whitespace-nowrap text-fg-muted">
            {matches.length === 0
              ? "no matches in loaded rows"
              : `${formatCount(index + 1)} of ${total} in loaded rows`}
          </span>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Previous match"
            disabled={matches.length === 0}
            onClick={() => step(-1)}
          >
            <ChevronUp aria-hidden className="size-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Next match"
            disabled={matches.length === 0}
            onClick={() => step(1)}
          >
            <ChevronDown aria-hidden className="size-3.5" />
          </Button>
        </>
      )}
    </div>
  )
}

function GoToRowField({
  inputRef,
  onGoTo,
}: {
  inputRef: RefObject<HTMLInputElement | null>
  onGoTo: (row: number) => string | null
}) {
  const id = useId()
  const [value, setValue] = useState("")
  const [note, setNote] = useState<string | null>(null)
  const submit = () => {
    const row = Number(value.replace(/[,_\s]/g, ""))
    setNote(Number.isInteger(row) && row >= 1 ? onGoTo(row) : "Enter a row number")
  }
  return (
    <form
      className="flex items-center gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        submit()
      }}
    >
      <label
        htmlFor={id}
        className="sr-only font-mono text-2xs whitespace-nowrap text-fg-muted sm:not-sr-only"
      >
        Go to row
      </label>
      <div className="relative">
        <Input
          id={id}
          ref={inputRef}
          inputMode="numeric"
          autoComplete="off"
          value={value}
          onChange={(event) => {
            setValue(event.target.value)
            setNote(null)
          }}
          aria-describedby={note ? `${id}-note` : undefined}
          placeholder="Row"

          className="w-28 pr-10"
        />
        <kbd
          aria-hidden
          className="pointer-events-none absolute top-1/2 right-2 -translate-y-1/2 rounded-sm border border-border px-1 font-mono text-2xs text-fg-subtle"
        >
          ⌘G
        </kbd>
      </div>
      {note && (
        <span id={`${id}-note`} role="status" className="font-mono text-2xs text-warning">
          {note}
        </span>
      )}
    </form>
  )
}
