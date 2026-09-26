import type { GetQueryResultsOutput } from "@aws-sdk/client-athena"
import {
  parseComplexValue,
  resultColumn,
  resultPage,
  resultSchema,
  resultValue,
} from "./result-values"

describe("resultColumn", () => {
  it("marks a decimal right-aligned, with its precision and scale", () => {
    expect(resultColumn({ Name: "amount", Type: "decimal", Precision: 10, Scale: 2 })).toEqual({
      name: "amount",
      type: "decimal(10,2)",
      numeric: true,
      dateOnly: undefined,
    })
  })

  it("marks a date as a calendar day", () => {
    expect(resultColumn({ Name: "dt", Type: "date" }).dateOnly).toBe(true)
  })
})

describe("resultValue", () => {
  it.each([
    [null, "varchar", null],
    ["", "varchar", ""],
    ["42", "integer", 42],
    ["9007199254740993", "bigint", 9007199254740993n],
    ["1.5", "double", 1.5],
    ["12345678901234567.89", "decimal", "12345678901234567.89"],
    ["true", "boolean", true],
    ['{"a":1}', "json", { a: 1 }],
    ["2026-09-26 10:00:00.000 UTC", "timestamp with time zone", "2026-09-26 10:00:00.000 UTC"],
  ])("reads %j as a %s", (text, type, value) => {
    expect(resultValue(text, type)).toEqual(value)
  })

  it("reads a varbinary's hex as bytes", () => {
    expect(resultValue("01 ff", "varbinary")).toEqual(Uint8Array.from([1, 255]))
  })

  it("reads an array as a list", () => {
    expect(resultValue("[gift, express]", "array")).toEqual(["gift", "express"])
  })
})

describe("parseComplexValue", () => {
  it.each([
    ["[]", []],
    ["{}", {}],
    ["[1, null, 3]", ["1", null, "3"]],
    ["{city=London, zip=N1}", { city: "London", zip: "N1" }],
    ["{a=[1, 2], b={c=d}}", { a: ["1", "2"], b: { c: "d" } }],
    ["[{x=1}, {x=2}]", [{ x: "1" }, { x: "2" }]],
  ])("reads %s", (text, value) => {
    expect(parseComplexValue(text)).toEqual(value)
  })

  it.each(["[1, 2", "not a list", "{novalue}", "[1]trailing"])(
    "leaves %s alone when it does not parse to the end",
    (text) => {
      expect(parseComplexValue(text)).toBeUndefined()
    },
  )
})

describe("resultPage", () => {
  const output: GetQueryResultsOutput = {
    ResultSet: {
      ResultSetMetadata: {
        ColumnInfo: [
          { Name: "id", Type: "bigint" },
          { Name: "name", Type: "varchar" },
        ],
      },
      Rows: [
        { Data: [{ VarCharValue: "id" }, { VarCharValue: "name" }] },
        { Data: [{ VarCharValue: "1" }, {}] },
      ],
    },
    NextToken: "next",
  }

  it("drops a SELECT's header row and types the values", () => {
    expect(resultPage(output, true).rows).toEqual([[1, null]])
  })

  it("keeps every row of a statement without a header", () => {
    expect(resultPage(output, false).rows).toHaveLength(2)
  })

  it("carries the next page's token", () => {
    expect(resultPage(output, true).nextToken).toBe("next")
  })

  it("types the result CSV's fields the way it types the page's", () => {
    // The CSV path reads the same columns through the same mapping, so a
    // large result renders as a small one does.
    const schema = resultSchema(output)
    expect(schema.columns).toEqual(resultPage(output, true).columns)
    expect([schema.value("1", 0), schema.value(null, 1), schema.value("", 1)]).toEqual([
      1,
      null,
      "",
    ])
  })
})
