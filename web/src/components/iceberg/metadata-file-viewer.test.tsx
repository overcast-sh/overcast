import { useState } from "react"
import { renderWithRouter, screen, within } from "@/test/render"
import type { MetadataFile, MetadataReader } from "./data"
import { IcebergMetadataFiles, type MetadataSelection } from "./metadata-file-viewer"
import { parseIcebergMetadata } from "./metadata"

const OLD = "s3://w/metadata/00000-a.metadata.json"
const CURRENT = "s3://w/metadata/00001-b.metadata.json"

const oldText = '{"format-version":2,"table-uuid":"u","location":"s3://w","last-sequence-number":0}'
const currentText = JSON.stringify({
  "format-version": 2,
  "table-uuid": "u",
  location: "s3://w",
  "last-sequence-number": 1,
  "metadata-log": [{ "timestamp-ms": 1000, "metadata-file": OLD }],
})

const files: Record<string, string> = { [OLD]: oldText, [CURRENT]: currentText }
const read: MetadataReader = (location) =>
  Promise.resolve({ text: files[location], truncated: false })

const current: MetadataFile = {
  location: CURRENT,
  text: currentText,
  truncated: false,
  metadata: parseIcebergMetadata(currentText),
}

/** The viewer links each file to the S3 browser, so it renders inside a router. */
function renderViewer(initial: MetadataSelection = {}) {
  return renderWithRouter(() => <Viewer initial={initial} />, { path: "/" })
}

function Viewer({ initial }: { initial: MetadataSelection }) {
  const [selection, setSelection] = useState(initial)
  return (
    <IcebergMetadataFiles
      current={current}
      selection={selection}
      onSelectionChange={setSelection}
      read={read}
    />
  )
}

/** The raw view, once it holds `text` — highlighting splits it across spans. */
const rawView = (text: string) =>
  screen.findByText((_, el) => el?.tagName === "PRE" && el.textContent.includes(text))

describe("IcebergMetadataFiles", () => {
  it("shows the current file, pretty-printed", async () => {
    renderViewer()
    expect(await rawView('"last-sequence-number": 1')).toBeInTheDocument()
  })

  it("lists the current file and the metadata log in the version picker", async () => {
    renderViewer()
    const options = within(await screen.findByRole("combobox", { name: "Version" })).getAllByRole(
      "option",
    )
    expect(options.map((o) => o.getAttribute("value"))).toEqual([
      "00001-b.metadata.json",
      "00000-a.metadata.json",
    ])
  })

  it("diffs against the previous version on request", async () => {
    const { user } = renderViewer()
    await user.click(await screen.findByRole("button", { name: "Diff with previous" }))
    const diff = await screen.findByRole("region", { name: /Diff from 00000-a/ })
    expect(within(diff).getByText(/"last-sequence-number": 0/)).toBeInTheDocument()
    expect(within(diff).getByText(/"last-sequence-number": 1/)).toBeInTheDocument()
  })

  it("opens on the version a deep link names", async () => {
    renderViewer({ version: "00000-a.metadata.json" })
    expect(await rawView('"last-sequence-number": 0')).toBeInTheDocument()
  })
})
