// Writes the Parquet fixtures the S3 preview tests read, into
// src/features/s3/__fixtures__/:
// - orders.parquet — SNAPPY, hyparquet's built-in codec;
// - orders.zstd.parquet — the same rows as ZSTD, the codec Iceberg and S3
//   Tables write by default, compressed with Node's own zlib (Node 22.15+).
//
//   cd web && node scripts/generate-parquet-fixture.mjs
//
// Deterministic: fixed values, fixed dates, no clock and no randomness, and a
// pinned ZSTD level, so a re-run produces the same bytes unless hyparquet-writer changes its encoder
// (its `created_by` string carries the version, which is the one expected
// diff after a bump). Commit the regenerated file with the version bump.
//
// The file is shaped for what the tests prove, not for realism:
// - one column per type family the preview has to render — signed ints of
//   both widths, a DECIMAL, a double, a boolean, a string with a null in it,
//   a microsecond UTC timestamp, a DATE, a LIST of strings and a STRUCT;
// - two row groups (3 rows, then 2), so a test can assert that reading the
//   first rows never fetched a byte of the second group.
import { writeFileSync } from "node:fs"
import { constants, zstdCompressSync } from "node:zlib"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import { ByteWriter, parquetWrite } from "hyparquet-writer"

const here = dirname(fileURLToPath(import.meta.url))
const fixtures = resolve(here, "../src/features/s3/__fixtures__")

const columnData = [
  { name: "order_id", data: [1001n, 1002n, 1003n, 1004n, 1005n] },
  {
    name: "customer",
    data: ["Ada Lovelace", "Grace Hopper", null, "Alan Turing", "Edsger Dijkstra"],
  },
  { name: "amount", data: [19.99, 250, 0.5, null, 1234.56] },
  { name: "quantity", data: [1, 12, 3, 7, 2] },
  { name: "discount", data: [0, 0.125, null, 0.3, 0.05] },
  { name: "paid", data: [true, false, true, null, true] },
  {
    name: "ordered_at",
    data: [
      new Date("2026-09-01T08:15:00.000Z"),
      new Date("2026-09-02T12:00:30.250Z"),
      new Date("2026-09-03T23:59:59.999Z"),
      new Date("2026-09-04T00:00:00.000Z"),
      null,
    ],
  },
  {
    name: "ship_date",
    data: [
      new Date("2026-09-03T00:00:00.000Z"),
      new Date("2026-09-05T00:00:00.000Z"),
      null,
      new Date("2026-09-08T00:00:00.000Z"),
      new Date("2026-09-09T00:00:00.000Z"),
    ],
  },
  { name: "tags", data: [["gift", "express"], [], null, ["bulk"], ["gift"]] },
  {
    name: "address",
    data: [
      { city: "London", zip: "N1 9GU" },
      { city: "Arlington", zip: null },
      null,
      { city: "Manchester", zip: "M13 9PL" },
      { city: "Austin", zip: "78712" },
    ],
  },
]

const schema = [
  { name: "root", num_children: columnData.length },
  { name: "order_id", type: "INT64", repetition_type: "REQUIRED" },
  { name: "customer", type: "BYTE_ARRAY", converted_type: "UTF8", repetition_type: "OPTIONAL" },
  {
    name: "amount",
    type: "INT64",
    converted_type: "DECIMAL",
    logical_type: { type: "DECIMAL", precision: 10, scale: 2 },
    precision: 10,
    scale: 2,
    repetition_type: "OPTIONAL",
  },
  { name: "quantity", type: "INT32", repetition_type: "REQUIRED" },
  { name: "discount", type: "DOUBLE", repetition_type: "OPTIONAL" },
  { name: "paid", type: "BOOLEAN", repetition_type: "OPTIONAL" },
  {
    name: "ordered_at",
    type: "INT64",
    logical_type: { type: "TIMESTAMP", isAdjustedToUTC: true, unit: "MICROS" },
    repetition_type: "OPTIONAL",
  },
  { name: "ship_date", type: "INT32", converted_type: "DATE", repetition_type: "OPTIONAL" },
  { name: "tags", converted_type: "LIST", repetition_type: "OPTIONAL", num_children: 1 },
  { name: "list", repetition_type: "REPEATED", num_children: 1 },
  { name: "element", type: "BYTE_ARRAY", converted_type: "UTF8", repetition_type: "OPTIONAL" },
  { name: "address", repetition_type: "OPTIONAL", num_children: 2 },
  { name: "city", type: "BYTE_ARRAY", converted_type: "UTF8", repetition_type: "OPTIONAL" },
  { name: "zip", type: "BYTE_ARRAY", converted_type: "UTF8", repetition_type: "OPTIONAL" },
]

const zstd = (bytes) =>
  new Uint8Array(zstdCompressSync(bytes, { params: { [constants.ZSTD_c_compressionLevel]: 3 } }))

for (const [name, options] of [
  ["orders.parquet", {}],
  ["orders.zstd.parquet", { codec: "ZSTD", compressors: { ZSTD: zstd } }],
]) {
  const writer = new ByteWriter()
  await parquetWrite({ writer, columnData, schema, rowGroupSize: 3, ...options })
  const out = resolve(fixtures, name)
  writeFileSync(out, new Uint8Array(writer.getBuffer()))
  console.log(`wrote ${out}`)
}
