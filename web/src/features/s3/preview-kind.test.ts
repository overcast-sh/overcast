import { dataPreviewKind, isIcebergMetadataKey, isTextDataKind } from "./preview-kind"

describe("dataPreviewKind", () => {
  it.each([
    ["binary/octet-stream", "sales/2026/09.csv", "csv"],
    ["text/plain", "export.CSV", "csv"],
    ["text/csv; charset=utf-8", "no-extension", "csv"],
    ["application/octet-stream", "events.tsv", "tsv"],
    ["text/tab-separated-values", "data", "tsv"],
    ["application/octet-stream", "logs/app.jsonl", "jsonl"],
    ["application/octet-stream", "logs/app.ndjson", "jsonl"],
    ["application/x-ndjson", "stream", "jsonl"],
    ["binary/octet-stream", "warehouse/orders/data/00000-0-a.parquet", "parquet"],
    ["application/vnd.apache.parquet", "blob", "parquet"],
    ["binary/octet-stream", "orders/metadata/snap-1-1-abc.avro", "avro"],
    ["application/json", "orders/metadata/00003-6f0c.metadata.json", "iceberg-metadata"],
    ["binary/octet-stream", "orders/metadata/v2.metadata.json", "iceberg-metadata"],
  ])("%s %s is %s", (contentType, key, kind) => {
    expect(dataPreviewKind(contentType, key)).toBe(kind)
  })

  it("lets a specific content type overrule the extension", () => {
    expect(dataPreviewKind("text/csv", "report.tsv")).toBe("csv")
  })

  it.each([
    ["application/json", "config.json"],
    ["text/plain", "notes.txt"],
    ["image/png", "a.png"],
    ["binary/octet-stream", "data.csv.gz"],
  ])("%s %s is not a data file", (contentType, key) => {
    expect(dataPreviewKind(contentType, key)).toBeNull()
  })
})

describe("isIcebergMetadataKey", () => {
  it("matches the file name, not a folder named like one", () => {
    expect(isIcebergMetadataKey("t/metadata/00001-x.metadata.json")).toBe(true)
    expect(isIcebergMetadataKey("x.metadata.json/readme.txt")).toBe(false)
  })

  it("leaves gzipped metadata alone — it is not text", () => {
    expect(isIcebergMetadataKey("t/metadata/00001-x.gz.metadata.json")).toBe(false)
  })
})

describe("isTextDataKind", () => {
  it("reads the text formats through the window and the binary ones not", () => {
    expect(isTextDataKind("csv")).toBe(true)
    expect(isTextDataKind("iceberg-metadata")).toBe(true)
    expect(isTextDataKind("parquet")).toBe(false)
    expect(isTextDataKind("avro")).toBe(false)
    expect(isTextDataKind(null)).toBe(false)
  })
})
