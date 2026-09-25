import { parseS3Uri } from "./s3-uri"

describe("parseS3Uri", () => {
  it.each([
    ["s3://warehouse/metadata/00001-a.metadata.json", "warehouse", "metadata/00001-a.metadata.json"],
    ["s3://warehouse", "warehouse", ""],
    ["s3a://warehouse/data/", "warehouse", "data/"],
  ])("splits %s into bucket and key", (uri, bucket, key) => {
    expect(parseS3Uri(uri)).toEqual({ bucket, key })
  })

  it("is null for a location that is not an S3 URI", () => {
    expect(parseS3Uri("file:///tmp/warehouse")).toBeNull()
  })
})
