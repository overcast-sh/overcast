/**
 * map-layout-random — the one source of randomness in the layout.
 *
 * H5 in docs/plans/map-layout-objective.md: the same input yields
 * byte-identical output, and any randomness comes from a seeded generator
 * whose seed is a function of the input. The engine's annealer and the
 * fixture generator both draw from here; nothing in the layout touches
 * Math.random.
 */

/** mulberry32: 32-bit state, fast, and well distributed enough to drive an annealer. */
export function seededRandom(seed: number): () => number {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

/** FNV-1a over a string: a seed that is a pure function of the ids it hashes. */
export function hashSeed(text: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return h >>> 0
}
