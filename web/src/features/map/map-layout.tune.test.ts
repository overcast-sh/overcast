/**
 * Layout tuner: searches the annealer's own parameters (ANNEALER_TUNING in
 * map-layout-engine.ts) against the fixtures' total score and prints the
 * best settings, plus a calibration of how many annealing iterations this
 * machine gets through per millisecond (ENGINE_BUDGET.iterationsPerMs).
 *
 * Runs only under `pnpm layout:tune` (`--mode tune`). The settings it
 * prints are copied into the engine by hand, with the run's date, so the
 * shipped constants are a reviewable choice rather than a side effect.
 *
 * The objective is the mean, over the fixtures, of the engine's soft score
 * as a fraction of dagre's (or of the recorded engine baseline where dagre
 * threw), so a small fixture counts as much as a large one. The search is a
 * seeded random walk from the current settings: each candidate perturbs a
 * few parameters, and the walk moves when the candidate scores lower.
 */

import { describe, expect, it } from "vitest"
import { FIXTURE_NAMES, loadFixture, type LayoutFixture } from "./__fixtures__"
import { layoutFixture, readBaseline, scoreFixture } from "./__fixtures__/baseline"
import { ANNEALER_TUNING, ENGINE_BUDGET, ENGINE_STATS, type AnnealerTuning } from "./map-layout-engine"
import { seededRandom } from "./map-layout-random"

/** Candidates evaluated; each is a full layout of every fixture. */
const CANDIDATES = 32
const SEED = 20260920
/** The largest fixture is left out of the search — one layout of it costs more than all the others together. */
const SEARCH_FIXTURES = FIXTURE_NAMES.filter((n) => n !== "mixed-1000")

/** Ranges the walk may explore, per parameter. */
const RANGES: Record<keyof AnnealerTuning, [min: number, max: number]> = {
  orderShare: [0.1, 0.7],
  orderTemp0: [0.3, 4],
  orderTemp1: [0.01, 0.3],
  rowTemp0: [0.5, 12],
  rowTemp1: [0.01, 0.5],
  maxShift: [20, 200],
  alignShare: [0.1, 0.8],
  columnShare: [0, 0.4],
}

describe.skipIf(import.meta.env.MODE !== "tune")("layout tuner", () => {
  it("calibrates the annealing throughput", () => {
    const f = loadFixture("mixed-600")
    const share = ENGINE_BUDGET.annealTimeShare
    const measure = () => {
      layoutFixture(f)
      ENGINE_STATS.iterations = 0
      const start = performance.now()
      layoutFixture(f)
      return { ms: performance.now() - start, iterations: ENGINE_STATS.iterations }
    }
    const withAnnealing = measure()
    ENGINE_BUDGET.annealTimeShare = 0
    const without = measure()
    ENGINE_BUDGET.annealTimeShare = share
    const perMs = withAnnealing.iterations / Math.max(1, withAnnealing.ms - without.ms)
    process.stdout.write(
      `\ncalibration on mixed-600: ${withAnnealing.iterations} iterations in ${(withAnnealing.ms - without.ms).toFixed(0)}ms → iterationsPerMs ≈ ${perMs.toFixed(0)} (currently ${ENGINE_BUDGET.iterationsPerMs}); pipeline without annealing ${without.ms.toFixed(0)}ms\n`,
    )
    expect(perMs).toBeGreaterThan(0)
  })

  it("searches the annealer settings", () => {
    const baseline = readBaseline()
    const fixtures: LayoutFixture[] = SEARCH_FIXTURES.map(loadFixture)
    const reference = new Map<string, number>()
    for (const f of fixtures) {
      const dagre = baseline.dagre?.[f.name]?.soft
      const engine = baseline.engine?.[f.name]?.soft
      reference.set(f.name, dagre ?? engine ?? 1)
    }
    const objective = (): number => {
      let sum = 0
      for (const f of fixtures) {
        const score = scoreFixture(f, layoutFixture(f))
        // An invalid layout is not a candidate at all.
        if (!Number.isFinite(score.total)) return Infinity
        sum += score.total / reference.get(f.name)!
      }
      return sum / fixtures.length
    }
    const random = seededRandom(SEED)
    const original: AnnealerTuning = { ...ANNEALER_TUNING }
    let current: AnnealerTuning = { ...original }
    let currentCost = objective()
    let best = { ...current }
    let bestCost = currentCost
    const out: string[] = [`start ${currentCost.toFixed(4)} ${JSON.stringify(current)}`]
    const keys = Object.keys(RANGES) as Array<keyof AnnealerTuning>
    for (let i = 0; i < CANDIDATES; i++) {
      const candidate = { ...current }
      // Perturb one to three parameters, each by up to a quarter of its range.
      const count = 1 + Math.floor(random() * 3)
      for (let k = 0; k < count; k++) {
        const key = keys[Math.floor(random() * keys.length)]
        const [min, max] = RANGES[key]
        const value = candidate[key] + (random() * 2 - 1) * (max - min) * 0.25
        candidate[key] = Math.round(Math.min(max, Math.max(min, value)) * 1000) / 1000
      }
      Object.assign(ANNEALER_TUNING, candidate)
      const cost = objective()
      const accepted = cost < currentCost
      out.push(`${String(i).padStart(3)} ${cost.toFixed(4)} ${accepted ? "↓" : " "} ${JSON.stringify(candidate)}`)
      if (accepted) {
        current = candidate
        currentCost = cost
        if (cost < bestCost) {
          bestCost = cost
          best = { ...candidate }
        }
      }
    }
    Object.assign(ANNEALER_TUNING, original)
    out.push(`\nbest ${bestCost.toFixed(4)} (from ${out[0].split(" ")[1]}):\n${JSON.stringify(best, null, 2)}`)
    process.stdout.write(`\n${out.join("\n")}\n`)
    expect(bestCost).toBeLessThanOrEqual(Number(out[0].split(" ")[1]))
  })
})
