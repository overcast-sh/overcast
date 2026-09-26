import { NotTabularError } from "@/lib/data-sources/row-source"
import { schemaFromParquet, schemaFromText } from "./infer-schema"

describe("schemaFromText", () => {
  it("types a CSV's columns from its header and values, in lower case", () => {
    const schema = schemaFromText("csv", "Id,Name,Price\n1,apple,1.5\n2,pear,2\n", false)
    expect(schema.columns).toEqual([
      { Name: "id", Type: "bigint" },
      { Name: "name", Type: "string" },
      { Name: "price", Type: "double" },
    ])
  })

  it("keeps the delimiter the text chose", () => {
    expect(schemaFromText("csv", "a;b\n1;2\n", false).delimiter).toBe(";")
  })

  it("notices quoted values, which need OpenCSVSerde", () => {
    expect(schemaFromText("csv", 'a,b\n"x, y",2\n', false).quoted).toBe(true)
  })

  it("types JSON Lines keys from their values", () => {
    const schema = schemaFromText("jsonl", '{"id":1,"tags":["a"]}\n{"id":2,"tags":[]}\n', false)
    expect(schema.columns).toEqual([
      { Name: "id", Type: "bigint" },
      { Name: "tags", Type: "array<string>" },
    ])
  })

  it("throws the preview's own reason when the text is not a table", () => {
    expect(() => schemaFromText("jsonl", '{"a":1}\n{"b":2}\n', false)).toThrow(NotTabularError)
  })
})

describe("schemaFromParquet", () => {
  it("maps the footer's fields to Hive types", () => {
    const schema = schemaFromParquet([
      { name: "id", type: "INT64", nullable: false },
      { name: "at", type: "TIMESTAMP(MICROS, UTC)", nullable: true },
    ])
    expect(schema).toEqual({
      format: "parquet",
      columns: [
        { Name: "id", Type: "bigint" },
        { Name: "at", Type: "timestamp" },
      ],
    })
  })
})
