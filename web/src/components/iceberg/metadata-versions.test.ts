import { parseIcebergMetadata } from "./metadata"
import { findVersion, metadataVersions, previousVersion } from "./metadata-versions"

const metadata = parseIcebergMetadata(
  JSON.stringify({
    "format-version": 2,
    "table-uuid": "u",
    location: "s3://w",
    "last-updated-ms": 3000,
    "metadata-log": [
      { "timestamp-ms": 1000, "metadata-file": "s3://w/metadata/00000-a.metadata.json" },
      { "timestamp-ms": 2000, "metadata-file": "s3://w/metadata/00001-b.metadata.json" },
    ],
  }),
)
const versions = metadataVersions("s3://w/metadata/00002-c.metadata.json", metadata)

describe("metadataVersions", () => {
  it("lists the current file first, then the log newest first", () => {
    expect(versions.map((v) => [v.fileName, v.current])).toEqual([
      ["00002-c.metadata.json", true],
      ["00001-b.metadata.json", false],
      ["00000-a.metadata.json", false],
    ])
  })

  it("lists only the current file when the metadata did not parse", () => {
    expect(metadataVersions("s3://w/metadata/00000-a.metadata.json", null)).toHaveLength(1)
  })
})

describe("findVersion", () => {
  it("finds a version by the file name a deep link carries", () => {
    expect(findVersion(versions, "00001-b.metadata.json")?.location).toBe(
      "s3://w/metadata/00001-b.metadata.json",
    )
  })

  it("is undefined for a file the table no longer lists", () => {
    expect(findVersion(versions, "00009-z.metadata.json")).toBeUndefined()
  })
})

describe("previousVersion", () => {
  it("is the version just before", () => {
    expect(previousVersion(versions, versions[0])?.fileName).toBe("00001-b.metadata.json")
  })

  it("is undefined for the oldest", () => {
    expect(previousVersion(versions, versions[2])).toBeUndefined()
  })
})
