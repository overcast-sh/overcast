import { describe, expect, it } from "vitest"
import { resultCount } from "./run-query"

describe("resultCount", () => {
  it("leaves a SELECT's header row out of the count", () => {
    expect(resultCount(4, true, false)).toBe("3 rows")
    expect(resultCount(1, true, false)).toBe("0 rows")
  })

  it("counts every line of a statement's text output", () => {
    expect(resultCount(1, false, false)).toBe("1 row")
  })

  it("says there is more when the result has another page", () => {
    expect(resultCount(1001, true, true)).toBe("1,000+ rows")
  })
})
