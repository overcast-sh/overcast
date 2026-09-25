import type { IcebergSchema } from "./metadata"
import { schemaChanges, schemaHistory } from "./schema-evolution"

const v0: IcebergSchema = {
  schemaId: 0,
  fields: [
    { id: 1, name: "order_id", required: true, type: "long" },
    { id: 2, name: "amount", required: false, type: "decimal(10, 2)" },
    { id: 3, name: "region", required: false, type: "string" },
    { id: 4, name: "qty", required: false, type: "int" },
  ],
}

const v1: IcebergSchema = {
  schemaId: 1,
  fields: [
    { id: 1, name: "order_id", required: true, type: "long" },
    { id: 2, name: "total_amount", required: false, type: "decimal(10, 2)" },
    { id: 4, name: "qty", required: true, type: "long" },
    {
      id: 5,
      name: "shipping",
      required: false,
      type: { type: "struct", fields: [{ id: 6, name: "carrier", required: false, type: "string" }] },
    },
  ],
}

describe("schemaChanges", () => {
  const changes = schemaChanges(v0, v1)

  it("reads a column that kept its id under a new name as a rename", () => {
    expect(changes).toContainEqual({ kind: "renamed", from: "amount", path: "total_amount" })
  })

  it("reports a column whose id is gone as dropped", () => {
    expect(changes).toContainEqual({ kind: "dropped", path: "region" })
  })

  it("reports a promoted type", () => {
    expect(changes).toContainEqual({ kind: "type", path: "qty", from: "int", to: "long" })
  })

  it("reports a column made required", () => {
    expect(changes).toContainEqual({ kind: "required", path: "qty", required: true })
  })

  it("reports a new struct and each of its fields as added", () => {
    expect(changes.filter((c) => c.kind === "added")).toEqual([
      { kind: "added", path: "shipping", type: "struct<carrier: string>" },
      { kind: "added", path: "shipping.carrier", type: "string" },
    ])
  })

  it("does not report a struct as retyped when only its children change", () => {
    const grown: IcebergSchema = {
      schemaId: 2,
      fields: [
        ...v1.fields.slice(0, 3),
        {
          id: 5,
          name: "shipping",
          required: false,
          type: {
            type: "struct",
            fields: [
              { id: 6, name: "carrier", required: false, type: "string" },
              { id: 7, name: "tracking_no", required: false, type: "string" },
            ],
          },
        },
      ],
    }
    expect(schemaChanges(v1, grown)).toEqual([
      { kind: "added", path: "shipping.tracking_no", type: "string" },
    ])
  })
})

describe("schemaHistory", () => {
  it("gives the first schema no changes and each later one its changes from the one before", () => {
    expect(schemaHistory([v0, v1]).map((v) => v.changes.length)).toEqual([0, 6])
  })
})
