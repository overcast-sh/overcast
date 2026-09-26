import type { MetadataFile } from "./data"
import { sameTablePlaceholder } from "./use-metadata-file"

const file = (location: string): MetadataFile => ({
  location,
  text: "{}",
  truncated: false,
  metadata: null,
})

const previous = file("s3://w/orders/metadata/00005-a.metadata.json")

describe("sameTablePlaceholder", () => {
  it("keeps the last file while the same table's next commit loads", () => {
    expect(sameTablePlaceholder("s3://w/orders/metadata/00006-b.metadata.json")(previous)).toBe(
      previous,
    )
  })

  it("shows nothing of another table while it loads", () => {
    expect(
      sameTablePlaceholder("s3://w/refunds/metadata/00001-c.metadata.json")(previous),
    ).toBeUndefined()
  })

  it("stands nothing in for a table with no metadata location", () => {
    expect(sameTablePlaceholder("")(previous)).toBeUndefined()
  })
})
