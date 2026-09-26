import { describe, expect, it, vi } from "vitest"
import type * as ApiModule from "@/services/api"
import { glue } from "@/services/api"
import { createTestQueryClient, renderWithRouter, screen, within } from "@/test/render"
import { glueKeys } from "../../data"
import type { PrefixScan } from "../../scan-prefix"
import type { InferredSchema } from "../../infer-schema"
import { CreateTableWizard } from "./create-table-wizard"

vi.mock("@/services/api", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiModule>()),
  glue: {
    listDatabases: vi.fn(() => Promise.resolve([{ Name: "sales" }])),
    createDatabase: vi.fn(() => Promise.resolve()),
    createTable: vi.fn(() => Promise.resolve()),
    batchCreatePartitions: vi.fn(() => Promise.resolve([])),
  },
}))

const sample = { key: "orders/dt=2026-09-01/part-0.csv", size: 42 }

const scan: PrefixScan = {
  sample,
  truncated: false,
  layout: {
    keys: ["dt"],
    partitions: [{ values: ["2026-09-01"], location: "s3://lake/orders/dt=2026-09-01/" }],
  },
}

const schema: InferredSchema = {
  format: "csv",
  delimiter: ",",
  columns: [
    { Name: "id", Type: "bigint" },
    { Name: "amount", Type: "double" },
  ],
}

/** The wizard opened on s3://lake/orders/, with its scan and sample already read. */
function renderWizard(database?: string) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(glueKeys.prefixScan("lake", "orders/"), scan)
  queryClient.setQueryData(glueKeys.sample("lake", sample), schema)
  queryClient.setQueryData(glueKeys.databases(), [{ Name: "sales" }])
  return renderWithRouter(
    () => (
      <CreateTableWizard
        database={database}
        state={{ open: true, location: "s3://lake/orders/", onOpen: vi.fn(), onClose: vi.fn() }}
      />
    ),
    { queryClient },
  )
}

describe("CreateTableWizard > opened on a prefix", () => {
  it("starts on the schema it inferred from the sampled object", async () => {
    renderWizard()

    const columns = await screen.findByRole("group", { name: "Columns" })
    expect(within(columns).getByRole("textbox", { name: "Columns 2 name" })).toHaveValue("amount")
  })

  it("turns the Hive folders into partition keys", async () => {
    renderWizard()

    const keys = await screen.findByRole("group", { name: "Partition keys" })
    expect(within(keys).getByRole("textbox", { name: "Partition keys 1 name" })).toHaveValue("dt")
  })
})

describe("CreateTableWizard > creating", () => {
  it("creates the table it inferred, as edited, with its partitions", async () => {
    const { user } = renderWizard("sales")
    const type = await screen.findByRole("combobox", { name: "Columns 1 type" })
    await user.clear(type)
    await user.type(type, "int")
    await user.click(screen.getByRole("button", { name: "Next" }))

    await user.click(await screen.findByRole("button", { name: "Create table" }))

    expect(glue.createTable).toHaveBeenCalledWith(
      "sales",
      expect.objectContaining({
        Name: "orders",
        PartitionKeys: [{ Name: "dt", Type: "string" }],
        StorageDescriptor: expect.objectContaining({
          Location: "s3://lake/orders/",
          Columns: [
            { Name: "id", Type: "int" },
            { Name: "amount", Type: "double" },
          ],
        }),
      }),
    )
    expect(glue.batchCreatePartitions).toHaveBeenCalledWith("sales", "orders", [
      expect.objectContaining({ Values: ["2026-09-01"] }),
    ])
  })

  it("creates a database it was given a new name for first", async () => {
    const { user } = renderWizard("lake_raw")
    await user.click(await screen.findByRole("button", { name: "Next" }))

    await user.click(await screen.findByRole("button", { name: "Create table" }))

    expect(glue.createDatabase).toHaveBeenCalledWith("lake_raw")
  })

  it("will not create a table with an invalid name", async () => {
    const { user } = renderWizard()
    await user.click(await screen.findByRole("button", { name: "Next" }))
    const name = await screen.findByRole("textbox", { name: /Table name/ })

    await user.clear(name)
    await user.type(name, "Bad Name")

    expect(screen.getByRole("button", { name: "Create table" })).toBeDisabled()
  })
})
