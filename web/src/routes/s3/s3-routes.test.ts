import { isRedirect } from "@tanstack/react-router"
import { Route as BucketIndexRoute } from "./$bucket/index"
import { Route as ObjectBrowserRoute } from "./$bucket/objects/$"
import { Route as DataViewerRoute } from "./$bucket/view"

describe("S3 bucket routes", () => {
  it("forwards the bucket's front door to the object browser without a history entry", () => {
    // Every existing link into a bucket points at /s3/<bucket>; the browser
    // itself now lives a segment down so the folder or object it shows can be
    // part of the path.
    let thrown: unknown
    try {
      BucketIndexRoute.options.beforeLoad?.({ params: { bucket: "demo" } } as never)
    } catch (err) {
      thrown = err
    }
    expect(isRedirect(thrown)).toBe(true)
    expect((thrown as { options: unknown }).options).toMatchObject({
      to: "/s3/$bucket/objects/$",
      params: { bucket: "demo", _splat: "" },
      replace: true,
    })
  })

  it("titles the browser by where in the bucket it is", async () => {
    // Splats as the router supplies them — the trailing slash of a folder is
    // already trimmed off the param by the time `head` sees it.
    const at = (splat: string) =>
      ObjectBrowserRoute.options.head?.({ params: { bucket: "demo", _splat: splat } } as never)

    expect((await at(""))?.meta?.[0]?.title).toBe("demo — S3 — Overcast")
    expect((await at("logs"))?.meta?.[0]?.title).toBe("logs — demo — S3 — Overcast")
    expect((await at("logs/app.log"))?.meta?.[0]?.title).toBe("logs/app.log — demo — S3 — Overcast")
  })

  it("takes the inspected revision from the query string and nothing else", () => {
    const validate = ObjectBrowserRoute.options.validateSearch as unknown as (
      search: Record<string, unknown>,
    ) => { versionId?: string }

    expect(validate({ versionId: "v2" })).toEqual({ versionId: "v2" })
    expect(validate({})).toEqual({ versionId: undefined })
    expect(validate({ versionId: 7 })).toEqual({ versionId: undefined })
  })

  it("titles the data viewer by its bucket", async () => {
    const head = await DataViewerRoute.options.head?.({ params: { bucket: "lake" } } as never)
    expect(head?.meta?.[0]?.title).toBe("Data viewer — lake — S3 — Overcast")
  })

  it.each([
    [{ key: "a.csv", row: "42" }, { key: "a.csv", row: 42, versionId: undefined }],
    [{ key: "a.csv", row: "0" }, { key: "a.csv", row: undefined, versionId: undefined }],
    [{ key: "a.csv", row: "1.5" }, { key: "a.csv", row: undefined, versionId: undefined }],
    [{ versionId: "v2" }, { key: "", row: undefined, versionId: "v2" }],
  ])("reads the viewer's key, 1-based row and revision from %o", (search, expected) => {
    const validate = DataViewerRoute.options.validateSearch as unknown as (
      search: Record<string, unknown>,
    ) => unknown
    expect(validate(search)).toEqual(expected)
  })
})
