import { describe, expect, it } from "vitest"
import type { TopologyDataTable } from "@/types"
import {
  DATA_MAX_ROWS,
  DATA_MORE_H,
  DATA_NODE_HEADER_H,
  DATA_ROW_H,
  DATA_SECTION_H,
  tableList,
  tableListHeight,
  workgroupHeight,
} from "./data-lake-layout"

const t = (name: string, namespace?: string): TopologyDataTable => ({ name, namespace })

describe("tableList", () => {
  it("sorts a bucket's tables by namespace, with a heading before each namespace", () => {
    const list = tableList([t("orders", "sales"), t("clicks", "web"), t("accounts", "sales")])
    expect(
      list.items.map((i) => (i.kind === "namespace" ? `# ${i.namespace}` : i.row.key)),
    ).toEqual(["# sales", "sales.accounts", "sales.orders", "# web", "web.clicks"])
    expect(list.hidden).toBe(0)
  })

  it("puts a dropped table's ghost among the live ones, unless it is live again", () => {
    const list = tableList(
      [t("b"), t("c")],
      [
        { name: "a", deletedAt: 1 },
        { name: "c", deletedAt: 1 },
      ],
    )
    expect(list.items.map((i) => i.kind === "table" && [i.row.key, !!i.row.ghost])).toEqual([
      ["a", true],
      ["b", false],
      ["c", false],
    ])
  })

  it("cuts a long list and counts what it left off", () => {
    const tables = Array.from({ length: DATA_MAX_ROWS + 3 }, (_, i) => t(`t${i}`))
    const list = tableList(tables)
    expect(list.items).toHaveLength(DATA_MAX_ROWS)
    expect(list.hidden).toBe(3)
  })
})

describe("node heights", () => {
  it("reserves the header, each row and heading, and the more line", () => {
    expect(tableListHeight(tableList([]))).toBe(DATA_NODE_HEADER_H)
    const oneNamespace = tableListHeight(tableList([t("a", "n"), t("b", "n")]))
    const cut = tableListHeight(tableList(Array.from({ length: 9 }, (_, i) => t(`t${i}`))))
    expect(cut - oneNamespace).toBe((DATA_MAX_ROWS - 2) * DATA_ROW_H + DATA_MORE_H - DATA_SECTION_H)
  })

  it("gives a workgroup a row per recent query, up to three", () => {
    expect(workgroupHeight(0)).toBe(DATA_NODE_HEADER_H)
    expect(workgroupHeight(5) - workgroupHeight(1)).toBe(2 * DATA_ROW_H)
  })
})
