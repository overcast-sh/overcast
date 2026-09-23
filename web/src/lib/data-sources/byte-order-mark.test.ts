import { bomLength, utf8BomLength } from "./byte-order-mark"

describe("bomLength", () => {
  it.each([
    ["text that opens with U+FEFF", "\uFEFFid,name", 1],
    ["text without one", "id,name", 0],
    ["empty text", "", 0],
  ])("is right for %s", (_, text, expected) => {
    expect(bomLength(text)).toBe(expected)
  })
})

describe("utf8BomLength", () => {
  it.each([
    ["bytes that open with EF BB BF", [0xef, 0xbb, 0xbf, 0x61], 3],
    ["bytes without one", [0x61, 0x62, 0x63], 0],
    ["fewer than three bytes", [0xef, 0xbb], 0],
  ])("is right for %s", (_, bytes, expected) => {
    expect(utf8BomLength(Uint8Array.from(bytes))).toBe(expected)
  })
})
