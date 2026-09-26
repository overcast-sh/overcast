import { useState } from "react"
import { Bell } from "lucide-react"
import { useNavigate, useSearch } from "@tanstack/react-router"
import { render, renderWithRouter, screen, waitFor, within } from "@/test/render"
import type * as React from "react"
import { useSortSearchParam } from "@/hooks/use-sort-search-param"
import { ResourceTable, type ResourceTableColumn, type ResourceTableSort } from "./resource-table"

interface Topic {
  arn: string
  name: string
}

const columns: ResourceTableColumn<Topic>[] = [
  { header: "Name", cell: (t) => t.name },
  { header: "ARN", cell: (t) => t.arn },
]

function TopicsTable(props: {
  data?: Topic[]
  isLoading?: boolean
  error?: unknown
  onRowClick?: (t: Topic) => void
  onDeleteMutate?: (id: string) => void
  isDeletePending?: boolean
  isFiltered?: boolean
  onClearFilter?: () => void
  canDelete?: (t: Topic) => boolean
  getVars?: (t: Topic) => string | Promise<string>
  emptyExtra?: React.ReactNode
  rowClassName?: (t: Topic, index: number) => string | undefined
  columns?: ResourceTableColumn<Topic>[]
}) {
  const [deleteTarget, setDeleteTarget] = useState<Topic>()

  return (
    <ResourceTable
      query={{ data: props.data, isLoading: props.isLoading ?? false, error: props.error }}
      columns={props.columns ?? columns}
      rowKey={(t) => t.arn}
      noun="topics"
      emptyIcon={Bell}
      emptyTitle="No topics yet"
      emptyDescription="Create a topic to get started."
      isFiltered={props.isFiltered}
      onClearFilter={props.onClearFilter}
      onRowClick={props.onRowClick}
      rowClassName={props.rowClassName}
      emptyExtra={props.emptyExtra}
      onDelete={
        props.onDeleteMutate
          ? {
              target: deleteTarget,
              onRequest: setDeleteTarget,
              onOpenChange: (open) => !open && setDeleteTarget(undefined),
              mutation: {
                mutate: (id: string) => {
                  props.onDeleteMutate?.(id)
                  setDeleteTarget(undefined)
                },
                isPending: props.isDeletePending ?? false,
              },
              getVars: props.getVars ?? ((t) => t.arn),
              canDelete: props.canDelete,
              label: (t) => t.name,
              noun: "topic",
            }
          : undefined
      }
    />
  )
}

const topics: Topic[] = [
  { arn: "arn:aws:sns:us-east-1:1:a", name: "alerts" },
  { arn: "arn:aws:sns:us-east-1:1:b", name: "billing" },
]

describe("ResourceTable > loading and empty states", () => {
  it("shows a loading skeleton while the query is in flight", () => {
    const { container } = render(<TopicsTable isLoading data={[]} />)
    expect(container.querySelector('[data-slot="skeleton-row"]')).toBeInTheDocument()
  })

  it("shows the empty state when there is no data", () => {
    render(<TopicsTable data={[]} />)
    expect(screen.getByText("No topics yet")).toBeInTheDocument()
    expect(screen.getByText("Create a topic to get started.")).toBeInTheDocument()
  })

  it("shows an error state when the query failed", () => {
    render(<TopicsTable data={[]} error={new Error("network down")} />)
    expect(screen.getByText("Failed to load topics")).toBeInTheDocument()
    expect(screen.getByText("network down")).toBeInTheDocument()
  })
})

// The three states a list page can land in when it's empty: nothing exists
// yet (create CTA), a filter narrowed it to nothing (clear-filter action,
// distinct copy), or the fetch failed (error copy, wins over both).
describe("ResourceTable > filtered vs. true-empty vs. error", () => {
  it("shows the create-CTA empty state when isFiltered is unset", () => {
    render(<TopicsTable data={[]} />)
    expect(screen.getByText("No topics yet")).toBeInTheDocument()
    expect(screen.getByText("Create a topic to get started.")).toBeInTheDocument()
    expect(screen.queryByText("No results match your filter.")).not.toBeInTheDocument()
  })

  it("shows 'no matches' copy and a clear-filter action instead of the create CTA when isFiltered is set", () => {
    const onClearFilter = vi.fn()
    render(<TopicsTable data={[]} isFiltered onClearFilter={onClearFilter} />)

    expect(screen.getByText("No matching topics")).toBeInTheDocument()
    expect(screen.getByText("No topics match your filter.")).toBeInTheDocument()
    // The true-empty copy must not also be showing.
    expect(screen.queryByText("No topics yet")).not.toBeInTheDocument()
    expect(screen.queryByText("Create a topic to get started.")).not.toBeInTheDocument()

    expect(screen.getByRole("button", { name: "Clear filter" })).toBeInTheDocument()
  })

  it("clicking Clear filter calls onClearFilter", async () => {
    const onClearFilter = vi.fn()
    const { user } = render(<TopicsTable data={[]} isFiltered onClearFilter={onClearFilter} />)

    await user.click(screen.getByRole("button", { name: "Clear filter" }))
    expect(onClearFilter).toHaveBeenCalledOnce()
  })

  it("shows the error state, not the filtered-empty state, when a filtered query also failed", () => {
    render(
      <TopicsTable
        data={[]}
        error={new Error("network down")}
        isFiltered
        onClearFilter={() => {}}
      />,
    )
    expect(screen.getByText("Failed to load topics")).toBeInTheDocument()
    expect(screen.getByText("network down")).toBeInTheDocument()
    // Neither empty-state variant should show — the error must win.
    expect(screen.queryByText("No matching topics")).not.toBeInTheDocument()
    expect(screen.queryByText("No topics yet")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Clear filter" })).not.toBeInTheDocument()
  })
})

describe("ResourceTable > populated table", () => {
  it("renders one row per resource with the configured columns", () => {
    render(<TopicsTable data={topics} />)
    const row = screen.getByRole("row", { name: /alerts/ })
    expect(within(row).getByText("arn:aws:sns:us-east-1:1:a")).toBeInTheDocument()
    expect(screen.getByRole("row", { name: /billing/ })).toBeInTheDocument()
  })

  it("calls onRowClick with the row's item", async () => {
    const onRowClick = vi.fn()
    const { user } = render(<TopicsTable data={topics} onRowClick={onRowClick} />)
    await user.click(screen.getByRole("row", { name: /alerts/ }))
    expect(onRowClick).toHaveBeenCalledWith(topics[0])
  })
})

describe("ResourceTable > delete flow", () => {
  it("opens a confirm dialog naming the row and mutates its id on confirm", async () => {
    const onDeleteMutate = vi.fn()
    const { user } = render(<TopicsTable data={topics} onDeleteMutate={onDeleteMutate} />)

    await user.click(screen.getByRole("button", { name: "Delete alerts" }))
    expect(screen.getByRole("dialog")).toBeInTheDocument()
    expect(screen.getByText("alerts", { selector: "span" })).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Delete" }))
    expect(onDeleteMutate).toHaveBeenCalledWith("arn:aws:sns:us-east-1:1:a")
  })

  it("does not trigger the row click when the delete action is clicked", async () => {
    const onRowClick = vi.fn()
    const { user } = render(
      <TopicsTable data={topics} onRowClick={onRowClick} onDeleteMutate={() => {}} />,
    )
    await user.click(screen.getByRole("button", { name: "Delete alerts" }))
    expect(onRowClick).not.toHaveBeenCalled()
  })

  it("does not render a delete action when onDelete is not configured", () => {
    render(<TopicsTable data={topics} />)
    expect(screen.queryByRole("button", { name: /Delete/ })).not.toBeInTheDocument()
  })
})

describe("ResourceTable > empty-state extras", () => {
  it("renders emptyExtra beneath the empty state", () => {
    render(<TopicsTable data={[]} emptyExtra={<p>There are 3 in ap-southeast-2.</p>} />)
    expect(screen.getByText("No topics yet")).toBeInTheDocument()
    expect(screen.getByText("There are 3 in ap-southeast-2.")).toBeInTheDocument()
  })

  it("leaves it out once the list has rows", () => {
    render(<TopicsTable data={topics} emptyExtra={<p>There are 3 in ap-southeast-2.</p>} />)
    expect(screen.queryByText("There are 3 in ap-southeast-2.")).not.toBeInTheDocument()
  })

  // A filter turning up nothing says nothing about other regions, and a failed
  // fetch says nothing about anything — neither is the fact this explains.
  it("leaves it out on the filtered-empty state and on an error", () => {
    const { unmount } = render(
      <TopicsTable
        data={[]}
        isFiltered
        onClearFilter={() => {}}
        emptyExtra={<p>There are 3 in ap-southeast-2.</p>}
      />,
    )
    expect(screen.getByText("No matching topics")).toBeInTheDocument()
    expect(screen.queryByText("There are 3 in ap-southeast-2.")).not.toBeInTheDocument()
    unmount()

    render(
      <TopicsTable
        data={[]}
        error={new Error("network down")}
        emptyExtra={<p>There are 3 in ap-southeast-2.</p>}
      />,
    )
    expect(screen.getByText("Failed to load topics")).toBeInTheDocument()
    expect(screen.queryByText("There are 3 in ap-southeast-2.")).not.toBeInTheDocument()
  })
})

describe("ResourceTable > row appearance and identity", () => {
  it("puts rowClassName on the row itself, not on a cell", () => {
    render(
      <TopicsTable
        data={topics}
        rowClassName={(t) => (t.name === "billing" ? "bg-danger-muted" : undefined)}
      />,
    )
    expect(screen.getByRole("row", { name: /billing/ })).toHaveClass("bg-danger-muted")
    expect(screen.getByRole("row", { name: /alerts/ })).not.toHaveClass("bg-danger-muted")
  })

  // A feed's entries can be identical — two log lines, two RDS events sharing a
  // timestamp and a message — so the index has to be reachable from `rowKey`.
  it("keys rows by index when the rows themselves are indistinguishable", () => {
    const duplicates: Topic[] = [
      { arn: "", name: "alerts" },
      { arn: "", name: "alerts" },
    ]
    render(
      <ResourceTable
        query={{ data: duplicates, isLoading: false }}
        noun="topics"
        columns={columns}
        rowKey={(_t, index) => index}
      />,
    )
    expect(screen.getAllByRole("row", { name: /alerts/ })).toHaveLength(2)
  })

  it("stops a click inside an interactive cell before it reaches onRowClick", async () => {
    const onRowClick = vi.fn()
    const { user } = render(
      <TopicsTable
        data={topics}
        onRowClick={onRowClick}
        columns={[
          { header: "Name", cell: (t) => t.name },
          { header: "URL", interactive: true, cell: () => <button type="button">Copy</button> },
        ]}
      />,
    )

    await user.click(screen.getAllByRole("button", { name: "Copy" })[0])
    expect(onRowClick).not.toHaveBeenCalled()

    await user.click(screen.getByRole("row", { name: /alerts/ }))
    expect(onRowClick).toHaveBeenCalledWith(topics[0])
  })
})

describe("ResourceTable > expanding a row", () => {
  function ExpandableTopics(props: {
    onRowClick?: (t: Topic) => void
    canExpand?: (t: Topic) => boolean
    defaultExpanded?: (t: Topic) => boolean
    expandLabel?: string
  }) {
    return (
      <ResourceTable
        query={{ data: topics, isLoading: false }}
        noun="topics"
        columns={columns}
        rowKey={(t) => t.arn}
        onRowClick={props.onRowClick}
        canExpand={props.canExpand}
        defaultExpanded={props.defaultExpanded}
        expandLabel={props.expandLabel}
        expandedContent={(t) => <p>Detail for {t.name}</p>}
      />
    )
  }

  it("renders the panel under the row the chevron belongs to", async () => {
    const { user } = render(<ExpandableTopics />)
    expect(screen.queryByText("Detail for alerts")).not.toBeInTheDocument()

    const row = screen.getByRole("row", { name: /alerts/ })
    await user.click(within(row).getByRole("button", { name: "Expand row" }))
    expect(screen.getByText("Detail for alerts")).toBeInTheDocument()

    await user.click(within(row).getByRole("button", { name: "Collapse row" }))
    expect(screen.queryByText("Detail for alerts")).not.toBeInTheDocument()
  })

  it("names the chevron after what the panel shows when the page says", async () => {
    const { user } = render(<ExpandableTopics expandLabel="subscriptions" />)
    const row = screen.getByRole("row", { name: /alerts/ })
    await user.click(within(row).getByRole("button", { name: "Show subscriptions" }))
    expect(within(row).getByRole("button", { name: "Hide subscriptions" })).toBeInTheDocument()
  })

  // Reading two events side by side is the reason this is a row rather than a
  // detail pane below the table.
  it("keeps several rows open at once", async () => {
    const { user } = render(<ExpandableTopics />)
    for (const name of ["alerts", "billing"]) {
      const row = screen.getByRole("row", { name: new RegExp(name) })
      await user.click(within(row).getByRole("button", { name: "Expand row" }))
    }
    expect(screen.getByText("Detail for alerts")).toBeInTheDocument()
    expect(screen.getByText("Detail for billing")).toBeInTheDocument()
  })

  it("toggles on a row click when the row does not navigate", async () => {
    const { user } = render(<ExpandableTopics />)
    await user.click(screen.getByRole("row", { name: /alerts/ }))
    expect(screen.getByText("Detail for alerts")).toBeInTheDocument()
  })

  // A row that navigates keeps its click: the chevron is then the only opener,
  // or a reader could never reach the detail page.
  it("leaves the row click alone when the page navigates, and the chevron does not navigate", async () => {
    const onRowClick = vi.fn()
    const { user } = render(<ExpandableTopics onRowClick={onRowClick} />)

    const row = screen.getByRole("row", { name: /alerts/ })
    await user.click(within(row).getByRole("button", { name: "Expand row" }))
    expect(screen.getByText("Detail for alerts")).toBeInTheDocument()
    expect(onRowClick).not.toHaveBeenCalled()

    await user.click(within(row).getAllByRole("cell")[0])
    expect(onRowClick).toHaveBeenCalledWith(topics[0])
  })

  it("offers no chevron on a row canExpand refuses", () => {
    render(<ExpandableTopics canExpand={(t) => t.name !== "billing"} />)
    const billing = screen.getByRole("row", { name: /billing/ })
    expect(within(billing).queryByRole("button", { name: /Expand row/ })).not.toBeInTheDocument()
    const alerts = screen.getByRole("row", { name: /alerts/ })
    expect(within(alerts).getByRole("button", { name: "Expand row" })).toBeInTheDocument()
  })

  it("opens the rows defaultExpanded names on first paint", async () => {
    const { user } = render(<ExpandableTopics defaultExpanded={(t) => t.name === "billing"} />)
    await waitFor(() => expect(screen.getByText("Detail for billing")).toBeInTheDocument())
    expect(screen.queryByText("Detail for alerts")).not.toBeInTheDocument()

    // Closing it stays closed — the seed runs once, not on every render.
    await user.click(screen.getByRole("button", { name: "Collapse row" }))
    expect(screen.queryByText("Detail for billing")).not.toBeInTheDocument()
  })
})

describe("ResourceTable > delete variables and enablement", () => {
  it("hands the mutation whatever getVars builds, not just an id", async () => {
    const mutate = vi.fn()
    const { user } = render(
      <TopicsTable data={topics} onDeleteMutate={mutate} getVars={(t) => `${t.name}:${t.arn}`} />,
    )

    await user.click(screen.getByRole("button", { name: "Delete alerts" }))
    await user.click(screen.getByRole("button", { name: "Delete" }))
    expect(mutate).toHaveBeenCalledWith("alerts:arn:aws:sns:us-east-1:1:a")
  })

  it("keeps the confirm button busy while an async getVars resolves", async () => {
    const mutate = vi.fn()
    let release: ((value: string) => void) | undefined
    const { user } = render(
      <TopicsTable
        data={topics}
        onDeleteMutate={mutate}
        getVars={() => new Promise<string>((resolve) => (release = resolve))}
      />,
    )

    await user.click(screen.getByRole("button", { name: "Delete alerts" }))
    await user.click(screen.getByRole("button", { name: "Delete" }))

    expect(screen.getByRole("button", { name: "Delete" })).toHaveAttribute("aria-busy", "true")
    expect(mutate).not.toHaveBeenCalled()

    release?.("etag-42")
    await waitFor(() => expect(mutate).toHaveBeenCalledWith("etag-42"))
  })

  it("offers no delete action on a row canDelete refuses", () => {
    render(
      <TopicsTable
        data={topics}
        onDeleteMutate={() => {}}
        canDelete={(t) => t.name !== "billing"}
      />,
    )
    expect(screen.getByRole("button", { name: "Delete alerts" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Delete billing" })).not.toBeInTheDocument()
  })
})

// ─── TanStack engine: sorting, visibility, pagination ─────────────────────

interface Stream {
  name: string
  createdAt: Date
  retention: number
}

const streams: Stream[] = [
  { name: "stream-10", createdAt: new Date("2026-03-01T00:00:00Z"), retention: 7 },
  { name: "stream-2", createdAt: new Date("2026-01-01T00:00:00Z"), retention: 30 },
  { name: "stream-30", createdAt: new Date("2026-02-01T00:00:00Z"), retention: 1 },
]

/** Body-row order, read from the first cell of each row. */
function rowOrder(): string[] {
  const [, body] = screen.getAllByRole("rowgroup")
  return within(body)
    .getAllByRole("row")
    .map((row) => within(row).getAllByRole("cell")[0].textContent)
}

function StreamsTable(props: {
  sortable?: boolean
  sort?: ResourceTableSort
  onSortChange?: (next: ResourceTableSort | undefined) => void
  defaultSort?: ResourceTableSort
  pageSize?: number
  columnToggle?: boolean
  hideRetention?: boolean
  data?: Stream[]
}) {
  return (
    <ResourceTable
      query={{ data: props.data ?? streams, isLoading: false }}
      noun="streams"
      rowKey={(s) => s.name}
      sort={props.sort}
      onSortChange={props.onSortChange}
      defaultSort={props.defaultSort}
      pageSize={props.pageSize}
      columnToggle={props.columnToggle}
      columns={[
        {
          header: "Name",
          sortValue: props.sortable === false ? undefined : (s) => s.name,
          cell: (s) => s.name,
        },
        {
          header: "Created",
          sortValue: props.sortable === false ? undefined : (s) => s.createdAt,
          cell: (s) => s.createdAt.toISOString().slice(0, 10),
        },
        {
          id: "retention-days",
          header: "Retention",
          defaultHidden: props.hideRetention,
          sortValue: props.sortable === false ? undefined : (s) => s.retention,
          cell: (s) => `${s.retention}d`,
        },
      ]}
    />
  )
}

/** Five columns — the width at which the columns menu appears on its own. */
function WideTable(props: { variant?: "card" | "embedded"; columnToggle?: boolean }) {
  return (
    <ResourceTable
      query={{ data: streams, isLoading: false }}
      noun="streams"
      rowKey={(s) => s.name}
      variant={props.variant}
      columnToggle={props.columnToggle}
      columns={[
        { header: "Name", cell: (s) => s.name },
        { header: "Created", cell: (s) => s.createdAt.toISOString().slice(0, 10) },
        { header: "Retention", cell: (s) => `${s.retention}d` },
        { header: "Shards", cell: () => "1" },
        { header: "Status", cell: () => "ACTIVE" },
      ]}
    />
  )
}

describe("ResourceTable > sorting", () => {
  it("leaves a column without sortValue as a plain header", () => {
    render(<StreamsTable sortable={false} />)
    const header = screen.getByRole("columnheader", { name: "Name" })
    expect(header).not.toHaveAttribute("aria-sort")
    expect(within(header).queryByRole("button")).not.toBeInTheDocument()
  })

  it("turns a column with sortValue into an unsorted sort control", () => {
    render(<StreamsTable />)
    expect(screen.getByRole("columnheader", { name: "Name" })).toHaveAttribute("aria-sort", "none")
    expect(screen.getByRole("button", { name: "Name" })).toBeInTheDocument()
  })

  it("cycles none → ascending → descending → none on header clicks", async () => {
    const { user } = render(<StreamsTable />)
    const header = () => screen.getByRole("columnheader", { name: "Name" })

    await user.click(screen.getByRole("button", { name: "Name" }))
    expect(header()).toHaveAttribute("aria-sort", "ascending")

    await user.click(screen.getByRole("button", { name: "Name" }))
    expect(header()).toHaveAttribute("aria-sort", "descending")

    await user.click(screen.getByRole("button", { name: "Name" }))
    expect(header()).toHaveAttribute("aria-sort", "none")
  })

  it("orders names alphanumerically, so stream-2 precedes stream-10", async () => {
    const { user } = render(<StreamsTable />)
    await user.click(screen.getByRole("button", { name: "Name" }))
    expect(rowOrder()).toEqual(["stream-2", "stream-10", "stream-30"])
  })

  // Text sorts A→Z on the first click; anything else — dates, counts — starts
  // descending, so "most recent" and "largest" are one click away.
  it("orders a Date column newest-first on the first click", async () => {
    const { user } = render(<StreamsTable />)
    await user.click(screen.getByRole("button", { name: "Created" }))
    expect(rowOrder()).toEqual(["stream-10", "stream-30", "stream-2"])

    await user.click(screen.getByRole("button", { name: "Created" }))
    expect(rowOrder()).toEqual(["stream-2", "stream-30", "stream-10"])
  })

  it("applies defaultSort before the user touches a header", () => {
    render(<StreamsTable defaultSort={{ id: "retention-days", desc: true }} />)
    expect(rowOrder()).toEqual(["stream-2", "stream-10", "stream-30"])
    expect(screen.getByRole("columnheader", { name: "Retention" })).toHaveAttribute(
      "aria-sort",
      "descending",
    )
  })

  // Only one column is sorted at a time — the URL param is one token.
  it("replaces the active sort rather than adding to it", async () => {
    const { user } = render(<StreamsTable />)
    await user.click(screen.getByRole("button", { name: "Name" }))
    await user.click(screen.getByRole("button", { name: "Created" }))

    expect(screen.getByRole("columnheader", { name: "Name" })).toHaveAttribute("aria-sort", "none")
    expect(screen.getByRole("columnheader", { name: "Created" })).toHaveAttribute(
      "aria-sort",
      "descending",
    )
  })

  // The header button is a full-bleed flex child, which `text-align` does not
  // reach — so the column's own alignment class has to reach the button.
  it("aligns the sort button with the column", () => {
    render(
      <TopicsTable
        data={topics}
        columns={[
          { header: "Name", sortValue: (t) => t.name, cell: (t) => t.name },
          {
            header: "Size",
            headerClassName: "text-right",
            sortValue: (t) => t.arn.length,
            cell: (t) => t.arn.length,
          },
        ]}
      />,
    )
    expect(screen.getByRole("button", { name: "Size" })).toHaveClass("justify-end")
    expect(screen.getByRole("button", { name: "Name" })).toHaveClass("justify-start")
  })
})

describe("ResourceTable > controlled sort", () => {
  it("reports the clicked column and direction to onSortChange", async () => {
    const onSortChange = vi.fn()
    const { user } = render(<StreamsTable onSortChange={onSortChange} />)

    await user.click(screen.getByRole("button", { name: "Name" }))
    expect(onSortChange).toHaveBeenCalledWith({ id: "name", desc: false })
  })

  it("uses the column's explicit id as the sort key", async () => {
    const onSortChange = vi.fn()
    const { user } = render(<StreamsTable onSortChange={onSortChange} />)

    await user.click(screen.getByRole("button", { name: "Retention" }))
    expect(onSortChange).toHaveBeenCalledWith({ id: "retention-days", desc: true })
  })

  // A list that declares a default means it for the no-param case too, which is
  // most page loads: `?sort=` absent must not mean the emulator's storage order
  // on a list that refetches.
  it("falls back to defaultSort while the controlled sort is undefined", async () => {
    const onSortChange = vi.fn()
    const { user } = render(
      <StreamsTable
        sort={undefined}
        onSortChange={onSortChange}
        defaultSort={{ id: "name", desc: true }}
      />,
    )
    expect(rowOrder()).toEqual(["stream-30", "stream-10", "stream-2"])

    // The cycle is asc ⇄ desc while a fallback is in place: "none" would render
    // as the fallback, so it is not offered as a click that changes nothing.
    await user.click(screen.getByRole("button", { name: "Name" }))
    expect(onSortChange).toHaveBeenCalledWith({ id: "name", desc: false })
  })

  it("renders the order the sort prop asks for", () => {
    render(<StreamsTable sort={{ id: "name", desc: true }} onSortChange={() => {}} />)
    expect(rowOrder()).toEqual(["stream-30", "stream-10", "stream-2"])
  })

  it("clears the sort back to undefined at the end of the cycle", async () => {
    const onSortChange = vi.fn()
    const { user } = render(
      <StreamsTable sort={{ id: "name", desc: true }} onSortChange={onSortChange} />,
    )
    await user.click(screen.getByRole("button", { name: "Name" }))
    expect(onSortChange).toHaveBeenCalledWith(undefined)
  })
})

describe("ResourceTable > column visibility", () => {
  it("offers every column but the first, which identifies the row", async () => {
    const { user } = render(<StreamsTable columnToggle />)
    await user.click(screen.getByRole("button", { name: /Columns/ }))

    expect(screen.getByRole("checkbox", { name: /Created/ })).toBeInTheDocument()
    expect(screen.getByRole("checkbox", { name: /Retention/ })).toBeInTheDocument()
    expect(screen.queryByRole("checkbox", { name: /Name/ })).not.toBeInTheDocument()
  })

  it("hides the column's header and cells when it is toggled off", async () => {
    const { user } = render(<StreamsTable columnToggle />)
    await user.click(screen.getByRole("button", { name: /Columns/ }))
    await user.click(screen.getByRole("checkbox", { name: /Created/ }))

    expect(screen.queryByRole("columnheader", { name: "Created" })).not.toBeInTheDocument()
    expect(screen.queryByText("2026-03-01")).not.toBeInTheDocument()
    expect(screen.getByRole("columnheader", { name: "Name" })).toBeInTheDocument()
  })

  it("starts a defaultHidden column hidden and lets the menu bring it back", async () => {
    const { user } = render(<StreamsTable hideRetention columnToggle />)
    expect(screen.queryByRole("columnheader", { name: "Retention" })).not.toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: /Columns/ }))
    await user.click(screen.getByRole("checkbox", { name: /Retention/ }))
    expect(screen.getByRole("columnheader", { name: "Retention" })).toBeInTheDocument()
  })
})

// Six conversion waves turned the menu off by hand at 70 call sites, so the
// bar for showing one unasked is a wide card table — not "more than one column
// could be hidden", which was nearly every list in the app (#1327).
describe("ResourceTable > columns menu default", () => {
  it("offers no menu on a card table narrower than five columns", () => {
    render(<StreamsTable />)
    expect(screen.queryByRole("button", { name: /Columns/ })).not.toBeInTheDocument()
  })

  it("offers one unasked on a five-column card table", () => {
    render(<WideTable />)
    expect(screen.getByRole("button", { name: /Columns/ })).toBeInTheDocument()
  })

  it("offers none on an embedded sub-table, however wide", () => {
    render(<WideTable variant="embedded" />)
    expect(screen.queryByRole("button", { name: /Columns/ })).not.toBeInTheDocument()
  })

  it("can be forced on below the threshold and off above it", () => {
    const { unmount } = render(<StreamsTable columnToggle />)
    expect(screen.getByRole("button", { name: /Columns/ })).toBeInTheDocument()
    unmount()

    render(<WideTable columnToggle={false} />)
    expect(screen.queryByRole("button", { name: /Columns/ })).not.toBeInTheDocument()
  })
})

describe("ResourceTable > pagination", () => {
  it("renders no pager without pageSize", () => {
    render(<StreamsTable />)
    expect(screen.queryByRole("button", { name: "Next" })).not.toBeInTheDocument()
  })

  it("pages the rows and walks forward and back", async () => {
    const { user } = render(<StreamsTable pageSize={2} />)
    expect(rowOrder()).toEqual(["stream-10", "stream-2"])
    expect(screen.getByText(/Page 1 of 2/)).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Next" }))
    expect(rowOrder()).toEqual(["stream-30"])
    expect(screen.getByText(/Page 2 of 2/)).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Previous" }))
    expect(rowOrder()).toEqual(["stream-10", "stream-2"])
  })

  it("hides the pager when everything fits on one page", () => {
    render(<StreamsTable pageSize={10} />)
    expect(screen.queryByRole("button", { name: "Next" })).not.toBeInTheDocument()
  })
})

// The deep-link contract: the sort lives in the route's `sort` search param,
// exactly as the filter box lives in `q`. `useSortSearchParam` is the wiring.
describe("ResourceTable > sort bound to the URL", () => {
  function SortedStreamsRoute() {
    const search: { sort?: string } = useSearch({ strict: false })
    const navigate = useNavigate()
    const [sort, setSort] = useSortSearchParam(
      search,
      navigate as unknown as Parameters<typeof useSortSearchParam<{ sort?: string }>>[1],
    )
    return <StreamsTable sort={sort} onSortChange={setSort} />
  }

  it("renders the order named by the sort param on first paint", async () => {
    renderWithRouter(SortedStreamsRoute, { initialEntry: "/?sort=-name" })
    await screen.findByRole("button", { name: "Name" })
    expect(rowOrder()).toEqual(["stream-30", "stream-10", "stream-2"])
    expect(screen.getByRole("columnheader", { name: "Name" })).toHaveAttribute(
      "aria-sort",
      "descending",
    )
  })

  it("writes the clicked column into the URL", async () => {
    const { user, router } = renderWithRouter(SortedStreamsRoute, { initialEntry: "/" })

    await user.click(await screen.findByRole("button", { name: "Created" }))
    await waitFor(() =>
      expect((router.state.location.search as { sort?: string }).sort).toBe("-created"),
    )
    expect(rowOrder()).toEqual(["stream-10", "stream-30", "stream-2"])
  })

  it("drops the param entirely when the sort is cleared", async () => {
    const { user, router } = renderWithRouter(SortedStreamsRoute, {
      initialEntry: "/?sort=-name",
    })

    await user.click(await screen.findByRole("button", { name: "Name" }))
    await waitFor(() =>
      expect((router.state.location.search as { sort?: string }).sort).toBeUndefined(),
    )
    expect(router.state.location.searchStr).not.toContain("sort")
  })
})
