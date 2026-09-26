import { discoverPartitions, isDataObject } from "./hive-partitions"

const obj = (key: string, size = 10) => ({ key, size })

describe("discoverPartitions", () => {
  it("reads the keys and one partition per folder from the objects' paths", () => {
    const layout = discoverPartitions("lake", "orders/", [
      obj("orders/dt=2026-09-01/region=eu/part-0.csv"),
      obj("orders/dt=2026-09-01/region=eu/part-1.csv"),
      obj("orders/dt=2026-09-02/region=us/part-0.csv"),
    ])
    expect(layout).toEqual({
      keys: ["dt", "region"],
      partitions: [
        { values: ["2026-09-01", "eu"], location: "s3://lake/orders/dt=2026-09-01/region=eu/" },
        { values: ["2026-09-02", "us"], location: "s3://lake/orders/dt=2026-09-02/region=us/" },
      ],
    })
  })

  it("decodes Hive's escaped values but keeps the folder as written in the location", () => {
    const layout = discoverPartitions("lake", "t/", [obj("t/ts=10%3A00/a.csv")])
    expect(layout.partitions).toEqual([{ values: ["10:00"], location: "s3://lake/t/ts=10%3A00/" }])
  })

  it("leaves out objects whose folders name other keys", () => {
    const layout = discoverPartitions("lake", "t/", [obj("t/dt=1/a.csv"), obj("t/other=2/b.csv")])
    expect(layout.partitions.map((p) => p.values)).toEqual([["1"]])
  })

  it("finds no keys under an unpartitioned prefix", () => {
    expect(discoverPartitions("lake", "t/", [obj("t/a.csv")])).toEqual({ keys: [], partitions: [] })
  })
})

describe("isDataObject", () => {
  it.each([
    ["t/a.csv", 10, true],
    ["t/_SUCCESS", 10, false],
    ["t/.a.csv.crc", 10, false],
    ["t/_temporary/a.csv", 10, false],
    ["t/dir_$folder$", 10, false],
    ["t/empty.csv", 0, false],
    ["t/folder/", 0, false],
  ])("treats %s (%i bytes) as data: %s", (key, size, data) => {
    expect(isDataObject({ key, size }, "t/")).toBe(data)
  })
})
