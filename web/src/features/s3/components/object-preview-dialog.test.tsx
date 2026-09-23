import { render, screen, userEvent, waitFor, within } from "@/test/render"
// Type-only, so referencing it inside the hoisted vi.mock factory is legal.
import type * as ApiModule from "@/services/api"
import type * as ParquetModule from "@/features/s3/preview-parquet"
import type { ParquetPreview } from "@/features/s3/preview-parquet"
import type { S3ObjectVersion } from "@/types"
import { ObjectPreviewDialog } from "./object-preview-dialog"
import { formatPreviewText, isTextPreviewable } from "./object-preview-format"

// Only the version listing and the preview body are stubbed.
// `getObjectDownloadUrl` is the real one throughout — the download-href
// assertions below are about what it builds.
const api = vi.hoisted(() => ({
  versions: [] as S3ObjectVersion[],
  preview: { text: "", truncated: false },
  /** When set, the text preview never arrives — the loading state holds. */
  previewPending: false,
}))

// The Parquet reader itself is tested against a real file in
// preview-parquet.test.ts; here only what the dialog does with its answer is.
const parquet = vi.hoisted(() => ({
  result: undefined as unknown,
  error: undefined as Error | undefined,
}))

vi.mock("@/features/s3/preview-parquet", async (importOriginal) => {
  const actual = await importOriginal<typeof ParquetModule>()
  return {
    ...actual,
    readParquetPreview: () =>
      parquet.error ? Promise.reject(parquet.error) : Promise.resolve(parquet.result),
  }
})

vi.mock("@/services/api", async (importOriginal) => {
  const actual = await importOriginal<typeof ApiModule>()
  return {
    ...actual,
    s3: {
      ...actual.s3,
      listObjectVersions: () =>
        Promise.resolve({ versions: api.versions, prefixes: [], isTruncated: false }),
      getObjectText: () =>
        api.previewPending ? new Promise(() => {}) : Promise.resolve(api.preview),
    },
  }
})

describe("formatPreviewText", () => {
  it("keeps self-closing XML tags at the current indentation level", () => {
    const formatted = formatPreviewText(
      "<root><empty/><with-space /><parent><child /></parent><after /></root>",
      "application/xml; charset=utf-8",
      "document.xml",
    )

    expect(formatted.text).toBe(
      [
        "<root>",
        "  <empty/>",
        "  <with-space />",
        "  <parent>",
        "    <child />",
        "  </parent>",
        "  <after />",
        "</root>",
      ].join("\n"),
    )
  })

  it("indents XML closing tags to match their opening tags", () => {
    const formatted = formatPreviewText(
      "<rss><channel><title>News</title><item><title>One</title><link>https://example.test/one</link></item><item><title>Two</title></item></channel></rss>",
      "application/rss+xml",
      "feed",
    )

    expect(formatted.text).toBe(
      [
        "<rss>",
        "  <channel>",
        "    <title>",
        "      News",
        "    </title>",
        "    <item>",
        "      <title>",
        "        One",
        "      </title>",
        "      <link>",
        "        https://example.test/one",
        "      </link>",
        "    </item>",
        "    <item>",
        "      <title>",
        "        Two",
        "      </title>",
        "    </item>",
        "  </channel>",
        "</rss>",
      ].join("\n"),
    )
    expect(formatted.language).toBe("markup")
  })

  it("uses the object extension when the content type is generic", () => {
    const formatted = formatPreviewText(
      "<root><child>value</child></root>",
      "application/octet-stream",
      "backup.xml",
    )

    expect(isTextPreviewable("application/octet-stream", "backup.xml")).toBe(true)
    expect(formatted.text).toBe(
      ["<root>", "  <child>", "    value", "  </child>", "</root>"].join("\n"),
    )
    expect(formatted.language).toBe("markup")
  })

  it("prefers a specific content type over a conflicting file extension", () => {
    const formatted = formatPreviewText(
      "<rss><channel><title>News</title></channel></rss>",
      "application/rss+xml",
      "feed.json",
    )

    expect(formatted.text).toBe(
      [
        "<rss>",
        "  <channel>",
        "    <title>",
        "      News",
        "    </title>",
        "  </channel>",
        "</rss>",
      ].join("\n"),
    )
  })

  it("detects JSON content types with parameters", () => {
    const formatted = formatPreviewText('{"ok":true}', "application/json; charset=utf-8", "data")

    expect(formatted.text).toBe(["{", '  "ok": true', "}"].join("\n"))
    expect(formatted.language).toBe("json")
  })

  it("chooses the CSS and JavaScript languages for their types", () => {
    // Policy only — how a chosen language actually renders (ranges vs markup)
    // is HighlightedCode's contract, tested with the component.
    expect(formatPreviewText("body { color: red }", "text/css", "site.css").language).toBe("css")
    expect(formatPreviewText("const x = 1", "text/javascript", "app.js").language).toBe(
      "javascript",
    )
  })

  it("declines to highlight a script too large for the frame budget", () => {
    // Tokenizing is linear in the text (the logs work measured ~19 ms of
    // Prism at 166 KiB); a preview can hold up to 1 MiB, which would be a
    // multi-hundred millisecond freeze at dialog-open. Past the cap the
    // preview is plain text, and says so.
    const huge = `var x = 1;\n`.repeat(30_000) // ~330 KiB
    const formatted = formatPreviewText(huge, "text/javascript", "bundle.js")

    expect(formatted.language).toBeNull()
    expect(formatted.text).toBe(huge)
    expect(formatted.skipped).toBe(true)
  })

  it("says when a JSON document was too large to pretty-print", () => {
    const huge = JSON.stringify({ blob: "x".repeat(300_000) })
    const formatted = formatPreviewText(huge, "application/json", "blob.json")

    expect(formatted.language).toBeNull()
    expect(formatted.text).toBe(huge)
    expect(formatted.skipped).toBe(true)
  })

  it("does not blame size when JSON simply failed to parse", () => {
    const formatted = formatPreviewText('{"truncated":', "application/json", "part.json")

    expect(formatted.language).toBeNull()
    expect(formatted.skipped).toBe(false)
  })
})

// ─── The dialog ────────────────────────────────────────────────────────────
//
// `application/octet-stream` on a `.bin` key is deliberately not previewable,
// so these render without the body fetch a previewable object would make. What
// is under test is which *address* the dialog builds, not what it renders from
// the bytes.

const metadata = {
  contentType: "application/octet-stream",
  contentLength: 12,
  lastModified: "2026-08-10T00:00:00.000Z",
  etag: "d41d8cd98f00b204e9800998ecf8427e",
  metadata: {},
  storageClass: "STANDARD",
}

function downloadHref(): string {
  return screen.getByRole("link", { name: /download/i }).getAttribute("href") ?? ""
}

describe("ObjectPreviewDialog > a named version", () => {
  const renderVersion = (versionId: string) =>
    render(
      <ObjectPreviewDialog
        bucket="my-bucket"
        objectKey="report.bin"
        versionId={versionId}
        metadata={metadata}
        loading={false}
        onSelectVersion={() => {}}
        onClose={() => {}}
      />,
    )

  it("downloads the version that was inspected, not the current one", () => {
    renderVersion("v2")
    expect(new URL(downloadHref(), "http://console.test").searchParams.get("versionId")).toBe("v2")
  })

  it('downloads by the literal id "null" rather than dropping it as falsy', () => {
    renderVersion("null")
    expect(new URL(downloadHref(), "http://console.test").searchParams.get("versionId")).toBe(
      "null",
    )
  })

  it("shows which version the metadata describes", () => {
    renderVersion("v2")
    expect(screen.getByText("Version")).toBeInTheDocument()
    expect(screen.getByText("v2")).toBeInTheDocument()
  })
})

describe("ObjectPreviewDialog > the current version", () => {
  beforeEach(() => {
    render(
      <ObjectPreviewDialog
        bucket="my-bucket"
        objectKey="report.bin"
        metadata={metadata}
        loading={false}
        onSelectVersion={() => {}}
        onClose={() => {}}
      />,
    )
  })

  it("downloads without naming a version", () => {
    expect(new URL(downloadHref(), "http://console.test").searchParams.has("versionId")).toBe(false)
  })

  it("shows no version row, there being no version to name", () => {
    expect(screen.queryByText("Version")).not.toBeInTheDocument()
  })
})

describe("ObjectPreviewDialog > text preview", () => {
  const renderText = (contentLength: number) =>
    render(
      <ObjectPreviewDialog
        bucket="my-bucket"
        objectKey="server.log"
        metadata={{ ...metadata, contentType: "text/plain", contentLength }}
        loading={false}
        onSelectVersion={() => {}}
        onClose={() => {}}
      />,
    )

  it("previews a text-like object larger than the fetch window", async () => {
    // The size of the *fetch* is capped by the Range request, not by refusing
    // the object: a 5 MiB log previews as its first 1 MiB, labelled as such.
    api.preview = { text: "first lines of a big log", truncated: true }
    renderText(5 * 1024 * 1024)

    expect(await screen.findByText("first lines of a big log")).toBeInTheDocument()
    expect(await screen.findByText(/first 1 MiB/)).toBeInTheDocument()
    expect(screen.queryByText(/Preview is available for/)).not.toBeInTheDocument()
  })

  it("does not claim truncation for an object shown whole", async () => {
    api.preview = { text: "all twelve b.", truncated: false }
    renderText(12)

    expect(await screen.findByText("all twelve b.")).toBeInTheDocument()
    expect(screen.queryByText(/first 1 MiB/)).not.toBeInTheDocument()
  })
})

describe("ObjectPreviewDialog > a version that cannot be read", () => {
  const renderError = (status: number, versionId?: string) =>
    render(
      <ObjectPreviewDialog
        bucket="my-bucket"
        objectKey="report.bin"
        versionId={versionId}
        metadata={undefined}
        loading={false}
        error={Object.assign(new Error("UnknownError"), {
          $metadata: { httpStatusCode: status },
        })}
        onSelectVersion={() => {}}
        onClose={() => {}}
      />,
    )

  it("explains a delete marker instead of showing an empty dialog", () => {
    renderError(405, "v3")
    expect(screen.getByRole("alert")).toHaveTextContent(/delete marker/i)
  })

  it("names the version that no longer exists", () => {
    renderError(404, "v2")
    expect(screen.getByRole("alert")).toHaveTextContent("Version v2")
  })

  it("offers no download, there being nothing at that address to fetch", () => {
    renderError(404, "v2")
    expect(screen.queryByRole("link", { name: /download/i })).not.toBeInTheDocument()
  })
})

describe("ObjectPreviewDialog > version history", () => {
  function version(versionId: string, over: Partial<S3ObjectVersion> = {}): S3ObjectVersion {
    return {
      key: "report.bin",
      versionId,
      isLatest: false,
      isDeleteMarker: false,
      lastModified: "2026-08-10T00:00:00.000Z",
      size: 12,
      etag: "abc",
      storageClass: "STANDARD",
      ...over,
    }
  }

  function renderInspector(props: {
    isVersioned?: boolean
    versionId?: string
    error?: Error
    onSelectVersion?: (versionId: string) => void
  }) {
    return render(
      <ObjectPreviewDialog
        bucket="my-bucket"
        objectKey="report.bin"
        versionId={props.versionId}
        metadata={props.error ? undefined : metadata}
        loading={false}
        error={props.error}
        isVersioned={props.isVersioned ?? true}
        onSelectVersion={props.onSelectVersion ?? (() => {})}
        onClose={() => {}}
      />,
    )
  }

  beforeEach(() => {
    api.versions = [version("v2", { isLatest: true }), version("v1")]
  })

  it("offers no tab strip on a bucket that keeps no history", () => {
    // One revision is not a choice, and a tab that never has anything behind it
    // is a control that only costs a click to discover.
    renderInspector({ isVersioned: false })
    expect(screen.queryByRole("tab")).not.toBeInTheDocument()
    expect(screen.getByText("Content-Type")).toBeInTheDocument()
  })

  it("counts the revisions on the tab before it is opened", async () => {
    renderInspector({})
    expect(await screen.findByRole("tab", { name: /Versions · 2/ })).toBeInTheDocument()
  })

  it("drills into the revision the user picked rather than only ticking it", async () => {
    // Clicking a row means "show me this one". Staying on the list would leave
    // the user to discover for themselves that what they asked for is behind
    // the other tab.
    const onSelectVersion = vi.fn()
    renderInspector({ onSelectVersion })
    const user = userEvent.setup()

    await user.click(await screen.findByRole("tab", { name: /Versions/ }))
    await user.click(await screen.findByText("v1"))

    expect(onSelectVersion).toHaveBeenCalledWith("v1")
    expect(screen.getByRole("tab", { name: "Details" })).toHaveAttribute("aria-selected", "true")
  })

  it("says which revision the details describe, and how to reach the rest", async () => {
    renderInspector({ versionId: "v1" })
    expect(await screen.findByText(/Older revision/)).toBeInTheDocument()
    // Counted oldest-first, so the newest of two revisions is "2 of 2" — the
    // ordinal people mean when they say which revision something is.
    expect(screen.getByText(/1 of 2/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "All 2" })).toBeInTheDocument()
  })

  it("says nothing about revisions when the inspector is following the key", async () => {
    // No version was asked for, so there is no "which one am I on" to answer —
    // the tab count already says a history exists.
    renderInspector({})
    await screen.findByRole("tab", { name: /Versions · 2/ })
    expect(screen.queryByText(/Older revision/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Current revision/)).not.toBeInTheDocument()
  })

  it("steps to the next revision without a trip back to the list", async () => {
    const onSelectVersion = vi.fn()
    renderInspector({ versionId: "v2", onSelectVersion })
    const user = userEvent.setup()

    await user.click(await screen.findByRole("button", { name: "Older revision" }))
    expect(onSelectVersion).toHaveBeenCalledWith("v1")
  })

  it("offers no step past either end of the history", async () => {
    renderInspector({ versionId: "v2" })
    // v2 is the newest, so there is nothing newer to step to.
    expect(await screen.findByRole("button", { name: "Newer revision" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Older revision" })).toBeEnabled()
  })

  it("keeps the history reachable when the revision on screen cannot be read", async () => {
    // A delete marker has no body, so the details pane is an explanation rather
    // than an object — and the history is exactly where the user goes next.
    api.versions = [version("v3", { isLatest: true, isDeleteMarker: true }), version("v2")]
    renderInspector({
      versionId: "v3",
      error: Object.assign(new Error("UnknownError"), { $metadata: { httpStatusCode: 405 } }),
    })

    expect(screen.getByRole("alert")).toHaveTextContent(/delete marker/i)
    const user = userEvent.setup()
    await user.click(await screen.findByRole("tab", { name: /Versions/ }))
    await waitFor(() => expect(screen.getByText("v2")).toBeInTheDocument())
  })

  it("goes back to the whole history on request", async () => {
    renderInspector({ versionId: "v1" })
    const user = userEvent.setup()

    await user.click(await screen.findByRole("button", { name: "All 2" }))
    expect(screen.getByRole("tab", { name: /Versions/ })).toHaveAttribute("aria-selected", "true")
  })
})

// ─── Data-file previews ────────────────────────────────────────────────────

function renderObject(
  objectKey: string,
  over: { contentType?: string; contentLength?: number; onClose?: () => void } = {},
) {
  return render(
    <ObjectPreviewDialog
      bucket="lake"
      objectKey={objectKey}
      metadata={{
        ...metadata,
        contentType: over.contentType ?? "binary/octet-stream",
        contentLength: over.contentLength ?? 64,
      }}
      loading={false}
      onSelectVersion={() => {}}
      onClose={over.onClose ?? (() => {})}
    />,
  )
}

describe("ObjectPreviewDialog > CSV and TSV", () => {
  beforeEach(() => {
    api.previewPending = false
    api.preview = { text: 'id,name,amount\n1,Ada,19.99\n2,"",5\n', truncated: false }
  })

  it("holds a static skeleton while the bytes are on their way", () => {
    api.previewPending = true
    renderObject("orders.csv")
    expect(screen.getByRole("status")).toHaveTextContent("loading preview")
    expect(screen.queryByRole("table")).not.toBeInTheDocument()
  })

  it("renders the first rows as a table, with a caption saying how many", async () => {
    renderObject("orders.csv")
    const table = await screen.findByRole("table", { name: /First rows of the CSV file/ })
    expect(within(table).getByRole("columnheader", { name: "name" })).toBeInTheDocument()
    expect(within(table).getByText("Ada")).toBeInTheDocument()
    // An empty field is a token, told apart from NULL and from a value, and
    // announced as an empty string rather than as the word on it.
    expect(within(table).getByTitle("Empty string")).toHaveTextContent("empty string")
    expect(within(table).queryByText("NULL")).not.toBeInTheDocument()
    expect(screen.getByText("2 rows · 3 columns")).toBeInTheDocument()
  })

  it("right-aligns a numeric column", async () => {
    renderObject("orders.csv")
    const cell = await screen.findByText("19.99")
    expect(cell.closest("td")).toHaveClass("text-right")
    expect(screen.getByText("Ada").closest("td")).not.toHaveClass("text-right")
  })

  it("toggles to the raw text and back, by keyboard, announcing which is on", async () => {
    renderObject("orders.csv")
    const user = userEvent.setup()
    const tableButton = await screen.findByRole("button", { name: "Table" })
    const rawButton = screen.getByRole("button", { name: "Raw" })
    expect(tableButton).toHaveAttribute("aria-pressed", "true")
    expect(rawButton).toHaveAttribute("aria-pressed", "false")

    rawButton.focus()
    await user.keyboard("{Enter}")
    expect(rawButton).toHaveAttribute("aria-pressed", "true")
    expect(screen.queryByRole("table")).not.toBeInTheDocument()
    expect(screen.getByText(/id,name,amount/)).toBeInTheDocument()

    await user.click(tableButton)
    expect(await screen.findByRole("table")).toBeInTheDocument()
  })

  it("says plainly when the table comes from a window of a larger file", async () => {
    const rows = Array.from({ length: 300 }, (_, i) => `${i},name-${i}\n`).join("")
    api.preview = { text: `n,name\n${rows}12,cut-sh`, truncated: true }
    renderObject("big.csv", { contentLength: 50 * 1024 * 1024 })
    expect(
      await screen.findByText(/Read from the first 1 MiB of this 50 MB object/),
    ).toBeInTheDocument()
    expect(screen.getByText(/first 200 rows of ~/)).toBeInTheDocument()
  })

  it("falls back to the raw text, with the reason, when the file is not valid CSV", async () => {
    api.preview = { text: 'a,b\n"never closed,1\n', truncated: false }
    renderObject("broken.csv")
    expect(
      await screen.findByText(/Shown as text: A quoted field is never closed/),
    ).toBeInTheDocument()
    expect(screen.getByText(/never closed,1/)).toBeInTheDocument()
    expect(screen.queryByRole("table")).not.toBeInTheDocument()
  })

  it("names a delimiter the text chose over the extension", async () => {
    api.preview = { text: "name;price\nTea;1,50\n", truncated: false }
    renderObject("prices.csv")
    expect(await screen.findByText(/semicolon-separated/)).toBeInTheDocument()
  })

  it("reads a TSV on its tabs", async () => {
    api.preview = { text: "a\tb\n1,5\t2\n", truncated: false }
    renderObject("data.tsv")
    expect(await screen.findByText("1,5")).toBeInTheDocument()
  })

  it("closes on Escape", async () => {
    const onClose = vi.fn()
    renderObject("orders.csv", { onClose })
    await screen.findByRole("table")
    await userEvent.setup().keyboard("{Escape}")
    expect(onClose).toHaveBeenCalled()
  })
})

describe("ObjectPreviewDialog > JSON Lines", () => {
  beforeEach(() => {
    api.previewPending = false
  })

  it("tabulates records with the same fields, drawing NULL as a token", async () => {
    api.preview = {
      text: '{"id":1,"email":null,"name":"a"}\n{"id":2,"email":"b@x"}\n',
      truncated: false,
    }
    renderObject("users.jsonl")
    const table = await screen.findByRole("table", { name: /JSON Lines/ })
    expect(within(table).getByTitle("NULL")).toHaveTextContent("NULL")
    expect(within(table).getByText("b@x")).toBeInTheDocument()
    // A key the record omits is neither NULL nor an empty string.
    expect(within(table).getByTitle("Not present in this record")).toHaveTextContent("not present")
  })

  it("widens the dialog for a table, and only for a table", async () => {
    api.preview = { text: '{"id":1}\n', truncated: false }
    const { unmount } = renderObject("users.jsonl")
    expect(await screen.findByRole("dialog")).toHaveClass("max-w-6xl")
    unmount()
    renderObject("notes.txt", { contentType: "text/plain" })
    expect(await screen.findByRole("dialog")).toHaveClass("max-w-4xl")
    expect(screen.getByRole("dialog")).not.toHaveClass("max-w-6xl")
  })

  it("keeps records of different shapes as written", async () => {
    api.preview = {
      text: '{"type":"click","x":1,"y":2}\n{"sku":"A","amount":3,"currency":"GBP"}\n',
      truncated: false,
    }
    renderObject("events.ndjson")
    expect(await screen.findByText(/do not share the same fields/)).toBeInTheDocument()
    expect(screen.queryByRole("table")).not.toBeInTheDocument()
  })
})

describe("ObjectPreviewDialog > Parquet", () => {
  const preview: ParquetPreview = {
    fields: [
      { name: "order_id", type: "INT64", nullable: false },
      { name: "customer", type: "STRING", nullable: true },
    ],
    numRows: 1204,
    rowGroups: 2,
    codecs: ["SNAPPY"],
    table: {
      columns: [
        { name: "order_id", type: "INT64", numeric: true },
        { name: "customer", type: "STRING", numeric: false },
      ],
      rows: [
        [1001n, "Ada Lovelace"],
        [1002n, null],
      ],
      totalRows: 1204,
      totalIsEstimate: false,
      truncatedByBytes: false,
      hiddenColumns: 0,
    },
  }

  beforeEach(() => {
    parquet.error = undefined
    parquet.result = preview
  })

  it("shows the first rows, their types and the file's own row count", async () => {
    renderObject("warehouse/orders/data/00000.parquet", { contentLength: 9_000_000 })
    const table = await screen.findByRole("table", { name: /Parquet file/ })
    expect(within(table).getByText("Ada Lovelace")).toBeInTheDocument()
    expect(within(table).getByText("NULL")).toBeInTheDocument()
    expect(within(table).getByText("STRING")).toBeInTheDocument()
    expect(
      screen.getByText("first 2 rows of 1,204 · 2 columns · 2 row groups · snappy"),
    ).toBeInTheDocument()
  })

  it("shows the schema on request", async () => {
    renderObject("t.parquet")
    await screen.findByRole("table", { name: /Parquet file/ })
    await userEvent.setup().click(screen.getByRole("button", { name: "Schema" }))
    const schema = screen.getByRole("table", { name: "Parquet schema" })
    expect(within(schema).getByText("order_id")).toBeInTheDocument()
    expect(within(schema).getByText("required")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Schema" })).toHaveAttribute("aria-pressed", "true")
  })

  it("opens on the schema, with the reason, when the rows cannot be read", async () => {
    parquet.result = {
      ...preview,
      table: undefined,
      codecs: ["ZSTD"],
      rowsError: "Rows are compressed with ZSTD, which the preview does not decode.",
    }
    renderObject("t.parquet")
    expect(await screen.findByRole("table", { name: "Parquet schema" })).toBeInTheDocument()
    await userEvent.setup().click(screen.getByRole("button", { name: "Rows" }))
    expect(screen.getByText(/compressed with ZSTD/)).toBeInTheDocument()
  })

  it("explains a file that is not Parquet and offers the download instead", async () => {
    parquet.error = new Error("parquet file invalid (footer != PAR1)")
    renderObject("not-really.parquet")
    expect(await screen.findByText("Could not read this file as Parquet")).toBeInTheDocument()
    expect(screen.getByText(/footer marker/)).toBeInTheDocument()
    const download = screen.getByRole("link", { name: /Download instead/ })
    expect(download.getAttribute("href")).toContain("not-really.parquet")
  })
})

describe("ObjectPreviewDialog > Avro", () => {
  it("says the file is not previewed rather than showing nothing", () => {
    renderObject("orders/metadata/snap-1.avro")
    expect(screen.getByText("Avro — not previewed")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /Download instead/ })).toBeInTheDocument()
    expect(screen.queryByText(/Preview is available for/)).not.toBeInTheDocument()
  })
})

describe("ObjectPreviewDialog > Iceberg metadata", () => {
  it("summarises the table above the JSON", async () => {
    api.previewPending = false
    api.preview = {
      text: JSON.stringify({
        "format-version": 2,
        "table-uuid": "9c12d441-03fe-4693-9a96-a0705ddf69c1",
        location: "s3://warehouse/sales/orders",
        "last-updated-ms": 1790000000000,
        "current-schema-id": 0,
        schemas: [
          { "schema-id": 0, fields: [{ id: 1, name: "order_id", required: true, type: "long" }] },
        ],
        snapshots: [{ "snapshot-id": 1 }],
      }).replace('"snapshots"', '"current-snapshot-id":3051729675574597004,"snapshots"'),
      truncated: false,
    }
    renderObject("orders/metadata/00001-abc.metadata.json", { contentType: "application/json" })
    const summary = await screen.findByLabelText("Iceberg table metadata")
    expect(within(summary).getByText("v2")).toBeInTheDocument()
    expect(within(summary).getByText("3051729675574597004")).toBeInTheDocument()
    expect(within(summary).getByText("order_id")).toBeInTheDocument()
    // The JSON itself is still there, underneath.
    expect(screen.getByText("Raw JSON")).toBeInTheDocument()
    const json = await screen.findByRole("region", { name: "Preview" })
    await waitFor(() => expect(json).toHaveTextContent('"format-version": 2'))
  })
})
