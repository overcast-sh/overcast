import { describe, expect, it } from "vitest"
import type { TopologyWebEndpoint } from "@/types"
import { DATA_MORE_H, DATA_NODE_HEADER_H } from "./data-lake-layout"
import {
  ENDPOINT_ROW_H,
  MAX_ENDPOINT_ROWS,
  endpointList,
  servesText,
  webFunctionHeight,
} from "./web-function-layout"

const endpoint = (name: string): TopologyWebEndpoint => ({
  name,
  type: "HomeRegion",
  authType: "ApplicationManaged",
  autoDeployment: "Disabled",
  domainName: "",
  serves: [],
})

describe("endpointList", () => {
  it("sorts the endpoints by name", () => {
    const list = endpointList([endpoint("prod"), endpoint("dev")])
    expect(list.items.map((i) => i.name)).toEqual(["dev", "prod"])
    expect(list.hidden).toBe(0)
  })

  it("puts a deleted endpoint's ghost among the live ones, unless it is live again", () => {
    const list = endpointList(
      [endpoint("b"), endpoint("c")],
      [
        { name: "a", deletedAt: 1 },
        { name: "c", deletedAt: 1 },
      ],
    )
    expect(list.items.map((i) => [i.name, i.ghost !== undefined])).toEqual([
      ["a", true],
      ["b", false],
      ["c", false],
    ])
  })

  it("cuts the list at the row limit and counts what it left off", () => {
    const many = Array.from({ length: MAX_ENDPOINT_ROWS + 3 }, (_, i) => endpoint(`ep-${i}`))
    const list = endpointList(many)
    expect(list.items).toHaveLength(MAX_ENDPOINT_ROWS)
    expect(list.hidden).toBe(3)
  })
})

describe("webFunctionHeight", () => {
  it("is the header alone for a function with no endpoints", () => {
    expect(webFunctionHeight(endpointList([]))).toBe(DATA_NODE_HEADER_H)
  })

  it("reserves a row per endpoint, and a line for the ones cut off", () => {
    const two = webFunctionHeight(endpointList([endpoint("a"), endpoint("b")]))
    expect(two - DATA_NODE_HEADER_H).toBeGreaterThanOrEqual(2 * ENDPOINT_ROW_H)
    const many = Array.from({ length: MAX_ENDPOINT_ROWS + 1 }, (_, i) => endpoint(`ep-${i}`))
    const cut = webFunctionHeight(endpointList(many))
    const full = webFunctionHeight(endpointList(many.slice(0, MAX_ENDPOINT_ROWS)))
    expect(cut - full).toBe(DATA_MORE_H)
  })
})

describe("servesText", () => {
  it("lists the revisions with their weights, or says there is none", () => {
    expect(
      servesText([
        { revisionId: "rev-ab12c", weight: 90 },
        { revisionId: "rev-de34f", weight: 10 },
      ]),
    ).toBe("rev-ab12c 90% · rev-de34f 10%")
    expect(servesText([])).toBe("no revision")
  })
})
