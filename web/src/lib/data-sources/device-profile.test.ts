import { cacheBudget, DEFAULT_CACHE_BYTES } from "./device-profile"

describe("cacheBudget", () => {
  it.each([
    ["a desktop that reports nothing", {}, DEFAULT_CACHE_BYTES],
    ["a desktop with 8 GB", { deviceMemory: 8 }, DEFAULT_CACHE_BYTES],
    ["a laptop with 4 GB", { deviceMemory: 4 }, DEFAULT_CACHE_BYTES / 2],
    ["a tablet", { coarsePointer: true }, DEFAULT_CACHE_BYTES / 2],
    ["a phone with 2 GB", { deviceMemory: 2, coarsePointer: true }, DEFAULT_CACHE_BYTES / 4],
  ])("gives %s its share", (_, signals, expected) => {
    expect(cacheBudget(signals)).toBe(expected)
  })
})
