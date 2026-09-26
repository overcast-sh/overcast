import { inferJsonHiveType, inferTextHiveType, parquetHiveType } from "./hive-types"

describe("inferTextHiveType", () => {
  it.each([
    [["1", "42", "-7"], "bigint"],
    [["1.5", "2", "3e2"], "double"],
    [["true", "FALSE"], "boolean"],
    [["2026-09-01", "2026-09-02"], "date"],
    [["2026-09-01 10:00:00", "2026-09-01T11:30:00.123"], "timestamp"],
    [["007", "12"], "string"],
    [["1", "x"], "string"],
  ])("types %j as %s", (values, type) => {
    expect(inferTextHiveType(values)).toBe(type)
  })

  it("skips empty values rather than letting them make the column a string", () => {
    expect(inferTextHiveType(["1", "", " ", "2"])).toBe("bigint")
  })

  it("types a column with no values as string", () => {
    expect(inferTextHiveType(["", ""])).toBe("string")
  })
})

describe("inferJsonHiveType", () => {
  it.each([
    [[1, 2], "bigint"],
    [[1, 2.5], "double"],
    [[true, null], "boolean"],
    [["a", "b"], "string"],
    [[[1, 2], [3]], "array<bigint>"],
    [[{ a: 1 }, { a: 2, b: "x" }], "struct<a:bigint,b:string>"],
    [[1, "a"], "string"],
  ])("types %j as %s", (values, type) => {
    expect(inferJsonHiveType(values)).toBe(type)
  })
})

describe("parquetHiveType", () => {
  it.each([
    ["INT64", "bigint"],
    ["INT32", "int"],
    ["DECIMAL(10, 2)", "decimal(10,2)"],
    ["TIMESTAMP(MICROS, UTC)", "timestamp"],
    ["STRING", "string"],
    ["LIST<STRING>", "array<string>"],
    ["STRUCT<a, b>", "struct<a:string,b:string>"],
  ])("maps %s to %s", (declared, type) => {
    expect(parquetHiveType(declared)).toBe(type)
  })
})
