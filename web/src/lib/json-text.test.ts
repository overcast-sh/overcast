import { parseJsonKeepingLargeIntegers, reindentJson } from "./json-text"

describe("reindentJson", () => {
  it.each([
    ['{"a":1,"b":[1,2,{"c":null}],"d":{}}'],
    ["[]"],
    ['{"nested":{"empty":[],"s":"x, y: {z}"}}'],
    ['  { "spaced" : [ true , false ] }  '],
  ])("lays %s out as JSON.stringify(…, null, 2) does", (text) => {
    expect(reindentJson(text)).toBe(JSON.stringify(JSON.parse(text), null, 2))
  })

  it("keeps an integer past 2^53 as written", () => {
    expect(reindentJson('{"snapshot-id":3051729675574597004}')).toBe(
      '{\n  "snapshot-id": 3051729675574597004\n}',
    )
  })

  it("keeps a string's escapes as written", () => {
    expect(reindentJson('["a\\"b\\u00e9"]')).toBe('[\n  "a\\"b\\u00e9"\n]')
  })

  it("throws on text that is not JSON", () => {
    expect(() => reindentJson("{nope")).toThrow(SyntaxError)
  })
})

describe("parseJsonKeepingLargeIntegers", () => {
  it("reads an integer past 2^53 as its decimal string", () => {
    expect(parseJsonKeepingLargeIntegers('{"id":3051729675574597004}')).toEqual({
      id: "3051729675574597004",
    })
  })

  it("reads a negative one the same way", () => {
    expect(parseJsonKeepingLargeIntegers("[-3051729675574597004]")).toEqual([
      "-3051729675574597004",
    ])
  })

  it("leaves safe numbers, decimals and strings alone", () => {
    expect(parseJsonKeepingLargeIntegers('{"n":-1,"f":1.5e300,"s":"3051729675574597004"}')).toEqual(
      { n: -1, f: 1.5e300, s: "3051729675574597004" },
    )
  })
})
