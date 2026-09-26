import { describe, expect, it } from "vitest"
import {
  athenaExecutionRoute,
  glueTableRoute,
  nodeRoute,
  routeHref,
  s3TablesTableRoute,
} from "./node-route"

describe("nodeRoute for data-lake nodes", () => {
  it("opens a workgroup in Athena's Workgroups tab", () => {
    expect(nodeRoute({ service: "athena", label: "analytics" })).toEqual({
      to: "/athena",
      search: { tab: "workgroups", workgroup: "analytics" },
    })
  })

  it("opens a Glue database's page, and s3tablescatalog the Glue page", () => {
    expect(nodeRoute({ service: "glue", label: "sales", glueResourceType: "database" })).toEqual({
      to: "/glue/$database",
      params: { database: "sales" },
    })
    expect(
      nodeRoute({ service: "glue", label: "s3tablescatalog", glueResourceType: "catalog" }),
    ).toEqual({
      to: "/glue",
    })
  })

  it("opens a table bucket's page", () => {
    expect(nodeRoute({ service: "s3tables", label: "lake" })).toEqual({
      to: "/s3tables/$bucket",
      params: { bucket: "lake" },
    })
  })

  it("links a table and an execution to their own pages", () => {
    expect(routeHref(glueTableRoute("sales", "order items"))).toBe("/glue/sales/order%20items")
    expect(routeHref(s3TablesTableRoute("lake", "t-1"))).toBe("/s3tables/lake/t-1")
    expect(routeHref(athenaExecutionRoute("q-1"))).toBe("/athena?tab=history&execution=q-1")
  })
})
