/**
 * The Athena and S3 Tables API modules' own logic — paging, batching and
 * listing across buckets — against SDK clients whose `send` is stubbed, so
 * the real paginators and commands run.
 */
import {
  AthenaClient,
  BatchGetNamedQueryCommand,
  ListNamedQueriesCommand,
} from "@aws-sdk/client-athena"
import {
  ListTableBucketsCommand,
  S3TablesClient,
  type ListTablesCommand,
} from "@aws-sdk/client-s3tables"
import { describe, expect, it, vi } from "vitest"
import { athena } from "./athena"
import { collectPages } from "./paginate"
import { s3tables } from "./s3tables"

const clients = vi.hoisted(() => ({ athena: undefined as unknown, s3tables: undefined as unknown }))

vi.mock("../aws-clients", () => ({
  awsClients: { athena: () => clients.athena, s3tables: () => clients.s3tables },
}))

/** A real client whose every call is answered by `answer`. */
function stubbed<C extends { send: unknown }>(client: C, answer: (command: object) => unknown) {
  const send = vi.fn((command: object) => Promise.resolve(answer(command)))
  Object.assign(client, { send })
  return send
}

describe("collectPages", () => {
  it("concatenates every page's items, skipping a page that has none", async () => {
    async function* pages() {
      for (const page of [{ items: [1, 2] }, { items: undefined }, { items: [3] }]) {
        yield await Promise.resolve(page)
      }
    }

    expect(await collectPages(pages(), (page) => page.items)).toEqual([1, 2, 3])
  })
})

describe("athena.listNamedQueries", () => {
  it("fetches the queries in batches of 50, the most BatchGetNamedQuery takes", async () => {
    const ids = Array.from({ length: 51 }, (_, i) => `nq-${i}`)
    const client = new AthenaClient({ region: "us-east-1" })
    clients.athena = client
    const send = stubbed(client, (command) =>
      command instanceof ListNamedQueriesCommand
        ? { NamedQueryIds: ids }
        : {
            NamedQueries: (command as BatchGetNamedQueryCommand).input.NamedQueryIds?.map((id) => ({
              NamedQueryId: id,
            })),
          },
    )

    const queries = await athena.listNamedQueries("etl")

    const batches = send.mock.calls
      .map(([command]) => command)
      .filter((command) => command instanceof BatchGetNamedQueryCommand)
      .map((command) => command.input.NamedQueryIds?.length)
    expect(batches).toEqual([50, 1])
    expect(queries).toHaveLength(51)
  })
})

describe("s3tables.listAllTables", () => {
  it("names the bucket each table is in", async () => {
    const client = new S3TablesClient({ region: "us-east-1" })
    clients.s3tables = client
    const bucketARN = (name: string) => `arn:aws:s3tables:us-east-1:000000000000:bucket/${name}`
    stubbed(client, (command) => {
      if (command instanceof ListTableBucketsCommand)
        return {
          tableBuckets: [
            { name: "lake", arn: bucketARN("lake") },
            { name: "archive", arn: bucketARN("archive") },
          ],
        }
      const arn = (command as ListTablesCommand).input.tableBucketARN
      return { tables: [{ name: `events-in-${arn?.split("/").pop()}` }] }
    })

    const tables = await s3tables.listAllTables()

    expect(tables.map((t) => [t.name, t.tableBucketName])).toEqual([
      ["events-in-lake", "lake"],
      ["events-in-archive", "archive"],
    ])
  })
})
