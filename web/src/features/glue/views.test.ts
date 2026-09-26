import { validateListSearch, validateTableSearch } from "./views"

describe("Glue search params", () => {
  it("keeps a known tab and drops an unknown one", () => {
    expect(validateTableSearch({ tab: "versions" }).tab).toBe("versions")
    expect(validateTableSearch({ tab: "nope" }).tab).toBeUndefined()
  })

  it("reads version ids back as text, though the router parsed them as numbers", () => {
    expect(validateTableSearch({ from: 1, to: 3 })).toMatchObject({ from: "1", to: "3" })
  })

  it("opens the wizard only for create=s3", () => {
    expect(validateListSearch({ create: "s3", location: "s3://lake/x/" })).toMatchObject({
      create: "s3",
      location: "s3://lake/x/",
    })
    expect(validateListSearch({ create: "other" }).create).toBeUndefined()
  })
})
