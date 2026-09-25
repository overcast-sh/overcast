import type { IcebergSnapshot } from "./metadata"
import { commitChanges, parentOf, totalsDelta } from "./snapshot-changes"

function snapshot(id: string, summary: Record<string, string>, parent?: string): IcebergSnapshot {
  return { snapshotId: id, parentSnapshotId: parent, timestampMs: 0, operation: "append", summary }
}

const first = snapshot("1", { "added-records": "1200", "total-records": "1200", "total-data-files": "3" })
const second = snapshot(
  "2",
  { "added-records": "860", "added-data-files": "2", "total-records": "2060", "total-data-files": "5" },
  "1",
)

describe("parentOf", () => {
  it("finds the snapshot a commit was made on top of", () => {
    expect(parentOf([first, second], second)).toBe(first)
  })

  it("is undefined for the table's first snapshot", () => {
    expect(parentOf([first, second], first)).toBeUndefined()
  })
})

describe("totalsDelta", () => {
  it("pairs each running total before and after the commit", () => {
    expect(totalsDelta(second, first)).toEqual([
      { label: "Records", unit: "count", before: 1200, after: 2060 },
      { label: "Data files", unit: "count", before: 3, after: 5 },
    ])
  })

  it("leaves before unset for the first snapshot", () => {
    expect(totalsDelta(first)[0]).toEqual({ label: "Records", unit: "count", after: 1200 })
  })
})

describe("commitChanges", () => {
  it("says what the commit added", () => {
    expect(commitChanges(second).map((c) => c.text)).toEqual(["+860 records", "+2 files"])
  })

  it("marks deletions as removals", () => {
    expect(commitChanges(snapshot("3", { "deleted-records": "1" }))).toEqual([
      { text: "−1 record", tone: "removed" },
    ])
  })
})
