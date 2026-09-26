import { useState } from "react"
import { render, screen } from "@/test/render"
import { newSchemaRow, type SchemaRow } from "../create-table-model"
import { SchemaBuilder } from "./schema-builder"

const onRows = vi.fn<(rows: SchemaRow[]) => void>()
/** The rows after the builder's last change. */
const latest = () => onRows.mock.lastCall?.[0] ?? []

function Builder({ initial }: { initial: SchemaRow[] }) {
  const [rows, setRows] = useState(initial)
  return (
    <SchemaBuilder
      rows={rows}
      onChange={(next) => {
        setRows(next)
        onRows(next)
      }}
    />
  )
}

const struct: SchemaRow = { ...newSchemaRow(), name: "shipping", type: "struct" }

describe("SchemaBuilder", () => {
  it("adds the row after a struct as its first field", async () => {
    const { user } = render(<Builder initial={[struct]} />)
    await user.click(screen.getByRole("button", { name: "Add column" }))
    expect(latest()[1].depth).toBe(1)
  })

  it("indents a column under the struct above with ⌥→", async () => {
    const { user } = render(<Builder initial={[struct, { ...newSchemaRow(), name: "carrier" }]} />)
    await user.click(screen.getByRole("textbox", { name: "Column 2 name" }))
    await user.keyboard("{Alt>}{ArrowRight}{/Alt}")
    expect(latest()[1].depth).toBe(1)
  })

  it("does not indent a column with no struct above it", () => {
    render(<Builder initial={[newSchemaRow(), newSchemaRow()]} />)
    expect(
      screen.getByRole("button", { name: "Indent column 2 under the struct above" }),
    ).toBeDisabled()
  })

  it("removes a struct together with its fields", async () => {
    const { user } = render(
      <Builder initial={[struct, { ...newSchemaRow(1), name: "carrier" }, newSchemaRow()]} />,
    )
    await user.click(screen.getByRole("button", { name: "Remove column 1" }))
    expect(latest()).toHaveLength(1)
  })
})
