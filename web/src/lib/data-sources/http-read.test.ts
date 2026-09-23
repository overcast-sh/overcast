import { checkedResponse, HttpReadError, rangeHeader } from "./http-read"

describe("rangeHeader", () => {
  it.each([
    [100, 200, "bytes=100-199"],
    [990, undefined, "bytes=990-"],
  ])("asks for [%s, %s) as %s", (start, end, header) => {
    expect(rangeHeader(start, end)).toBe(header)
  })
})

describe("checkedResponse", () => {
  it("passes a partial response through", () => {
    const response = new Response("x", { status: 206 })
    expect(checkedResponse(response)).toBe(response)
  })

  it("fails a refused read with its status", () => {
    expect(() => checkedResponse(new Response(null, { status: 404 }))).toThrow(
      new HttpReadError(404),
    )
  })
})
