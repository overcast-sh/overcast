import { describe, expect, it } from "vitest"
import { render, screen, within } from "@/test/render"
import { csvTable, icebergTable } from "../../__fixtures__/tables"
import { SchemaTab } from "./schema-tab"

describe("SchemaTab", () => {
  it("lists the columns, then the partition keys, marked", () => {
    render(<SchemaTab table={csvTable} />)

    const row = screen.getByRole("row", { name: /dt/ })
    expect(within(row).getByText("Partition")).toBeInTheDocument()
  })

  it("shows a column's comment", () => {
    render(<SchemaTab table={csvTable} />)

    expect(screen.getByText("in cents")).toBeInTheDocument()
  })

  it("shows an Iceberg column's field id and whether it is required", () => {
    render(<SchemaTab table={icebergTable} />)

    const row = screen.getByRole("row", { name: /bigint/ })
    expect(within(row).getByText("required")).toBeInTheDocument()
  })

  it("offers the DDL that recreates the table", () => {
    render(<SchemaTab table={csvTable} />)

    expect(screen.getByRole("button", { name: "Copy DDL" })).toBeInTheDocument()
  })
})
