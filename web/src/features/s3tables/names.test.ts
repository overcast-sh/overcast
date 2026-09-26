import { bucketNameProblem, identifierProblem } from "./names"

describe("bucketNameProblem", () => {
  it.each(["analytics", "a1b", "click-stream-2026"])("accepts %s", (name) => {
    expect(bucketNameProblem(name)).toBeUndefined()
  })

  it.each(["ab", "Analytics", "-lead", "trail-", "under_score", "x".repeat(64)])(
    "refuses %s",
    (name) => {
      expect(bucketNameProblem(name)).toMatch(/lowercase/)
    },
  )

  it("refuses a name ending in the warehouse suffix", () => {
    expect(bucketNameProblem("orders--table-s3")).toMatch(/reserved/)
  })
})

describe("identifierProblem", () => {
  it("accepts snake_case", () => {
    expect(identifierProblem("stock_levels", "table")).toBeUndefined()
  })

  it("refuses a hyphen", () => {
    expect(identifierProblem("stock-levels", "table")).toMatch(/underscores/)
  })

  it("refuses a namespace starting with aws", () => {
    expect(identifierProblem("aws_data", "namespace")).toMatch(/reserved/)
  })

  it("allows a table starting with aws", () => {
    expect(identifierProblem("aws_data", "table")).toBeUndefined()
  })
})
