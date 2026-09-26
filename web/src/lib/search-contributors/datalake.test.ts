/**
 * The Athena, Glue and S3 Tables search contributors: what each finds, and
 * where each result leads — the routes arn-routes.ts sends the same resources'
 * ARNs to.
 */
import { QueryClient } from "@tanstack/react-query"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { runSearch, type SearchContext } from "@/lib/search"
import { glueKeys } from "@/features/glue/data"
import { glue } from "@/services/api"
import "./athena"
import "./glue"
import "./s3tables"

vi.mock("@/services/api", () => ({
  athena: {
    listWorkGroups: vi.fn(() =>
      Promise.resolve([{ Name: "primary" }, { Name: "etl", Description: "Nightly loads" }]),
    ),
    listAllNamedQueries: vi.fn(() =>
      Promise.resolve([{ NamedQueryId: "nq-1", Name: "daily orders", WorkGroup: "etl" }]),
    ),
  },
  glue: {
    listDatabases: vi.fn(() =>
      Promise.resolve([{ Name: "sales", LocationUri: "s3://lake/sales/" }]),
    ),
    listAllTables: vi.fn(() =>
      Promise.resolve([
        { Name: "orders", DatabaseName: "sales" },
        { Name: "clicks", DatabaseName: "web" },
      ]),
    ),
  },
  s3tables: {
    listTableBuckets: vi.fn(() =>
      Promise.resolve([
        { name: "lake", arn: "arn:aws:s3tables:us-east-1:000000000000:bucket/lake" },
      ]),
    ),
    listAllNamespaces: vi.fn(() =>
      Promise.resolve([{ namespace: ["web"], tableBucketName: "lake" }]),
    ),
    listAllTables: vi.fn(() =>
      Promise.resolve([
        {
          name: "events",
          namespace: ["web"],
          tableARN: "arn:aws:s3tables:us-east-1:000000000000:bucket/lake/table/0f3c-9a",
          tableBucketName: "lake",
        },
      ]),
    ),
  },
}))

function ctx(queryClient = new QueryClient()): SearchContext {
  return {
    queryClient,
    endpoint: { baseUrl: "http://localhost:4566", region: "us-east-1", label: "Local" },
  }
}

async function search(query: string, serviceKey: string, context = ctx()) {
  return (await runSearch(query, context)).get(serviceKey) ?? []
}

beforeEach(() => {
  vi.mocked(glue.listDatabases).mockClear()
})

describe("athena contributor", () => {
  it("finds a workgroup and opens the Workgroups tab filtered to it", async () => {
    const results = await search("etl", "/athena")

    expect(results).toContainEqual(
      expect.objectContaining({
        label: "etl",
        type: "Workgroup",
        href: "/athena?tab=workgroups&q=etl",
      }),
    )
  })

  it("finds a saved query by name, with its workgroup as the sublabel", async () => {
    const results = await search("daily", "/athena")

    expect(results).toEqual([
      expect.objectContaining({
        label: "daily orders",
        sublabel: "etl",
        type: "Saved query",
        href: "/athena?tab=saved-queries&q=daily+orders",
      }),
    ])
  })
})

describe("glue contributor", () => {
  it("finds a database and links to its page", async () => {
    const results = await search("sales", "/glue")

    expect(results).toContainEqual(
      expect.objectContaining({ label: "sales", type: "Database", href: "/glue/sales" }),
    )
  })

  it("finds a table by its qualified name, with its database as the sublabel", async () => {
    const results = await search("sales.ord", "/glue")

    expect(results).toEqual([
      expect.objectContaining({
        label: "orders",
        sublabel: "sales",
        type: "Table",
        href: "/glue/sales/orders",
      }),
    ])
  })

  it("reads databases from the feature's own query cache when it is warm", async () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(glueKeys.databases(), [{ Name: "cached" }])

    const results = await search("cached", "/glue", ctx(queryClient))

    expect(results).toContainEqual(expect.objectContaining({ label: "cached" }))
    expect(glue.listDatabases).not.toHaveBeenCalled()
  })
})

describe("s3tables contributor", () => {
  it("finds a table bucket and links to its page", async () => {
    const results = await search("lake", "/s3tables")

    expect(results).toContainEqual(
      expect.objectContaining({ label: "lake", type: "Table bucket", href: "/s3tables/lake" }),
    )
  })

  it("finds a namespace and opens its bucket filtered to it", async () => {
    const results = await search("web", "/s3tables")

    expect(results).toContainEqual(
      expect.objectContaining({
        label: "web",
        sublabel: "lake",
        type: "Namespace",
        href: "/s3tables/lake?q=web",
      }),
    )
  })

  it("finds a table and links to it by the id its ARN carries", async () => {
    const results = await search("events", "/s3tables")

    expect(results).toEqual([
      expect.objectContaining({
        label: "events",
        sublabel: "lake · web",
        type: "Table",
        href: "/s3tables/lake/0f3c-9a",
      }),
    ])
  })
})
