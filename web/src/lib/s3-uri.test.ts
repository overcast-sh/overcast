import { parseS3Uri } from "./s3-uri"

describe("parseS3Uri", () => {
  it.each([
    ["s3://results/athena/q-1.csv", { bucket: "results", key: "athena/q-1.csv" }],
    ["s3://results/athena/", { bucket: "results", key: "athena/" }],
    ["s3://results", { bucket: "results", key: "" }],
    ["s3a://lake/raw/", { bucket: "lake", key: "raw/" }],
  ])("splits %s", (uri, location) => {
    expect(parseS3Uri(uri)).toEqual(location)
  })

  it("returns null for anything that is not an S3 URI", () => {
    expect(parseS3Uri("https://example.com/a")).toBeNull()
  })
})
