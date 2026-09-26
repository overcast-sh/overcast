import { describe, expect, it } from "vitest"
import { isDataFile } from "./data-lake-data"

describe("isDataFile", () => {
  it.each([
    ["clicks/dt=2026-09-01/part-0.csv", true],
    ["clicks/part-0.parquet", true],
    ["clicks/_SUCCESS", false],
    ["clicks/.part-0.parquet.crc", false],
    ["clicks/_temporary/0/part-0.csv", false],
    ["clicks/metadata/00001-a.metadata.json", false],
    ["clicks/metadata/snap-1.avro", false],
    ["clicks/readme.txt", false],
  ])("%s holds rows: %s", (key, want) => {
    expect(isDataFile(key, "clicks/")).toBe(want)
  })
})
