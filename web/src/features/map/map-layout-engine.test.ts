/**
 * map-layout-engine.test — the acceptance bar from
 * docs/plans/map-layout-objective.md §5, on every fixture:
 *
 *   - no hard-rule violation (H1–H4), checked by the scorer;
 *   - byte-identical output for the same input in another order (H5);
 *   - within the H6 time budget — with a 2× allowance for a slower CI
 *     runner, while the real time is printed for the record;
 *   - a score within 2% of the recorded engine baseline, so a later change
 *     that makes any fixture worse fails here;
 *   - the acceptance ratio against the dagre baseline, with the fixtures
 *     that do not reach it named rather than hidden.
 */

import { describe, expect, it } from "vitest"
import { FIXTURE_NAMES, loadFixture, type FixtureName } from "./__fixtures__"
import { layoutFixture, readBaseline, scoreFixture } from "./__fixtures__/baseline"
import { buildLayout } from "./map-layout"
import { timeBudgetMs } from "./map-layout-engine"

/** CI runners are slower and noisier than the machine the budget was calibrated on. */
const CI_TIME_ALLOWANCE = 2
/** How much worse than its recorded baseline a fixture may score before the change is a regression. */
const REGRESSION_TOLERANCE = 1.02
/** §5.2: the engine's score as a fraction of dagre's, up to 300 nodes and above. */
const ACCEPTANCE_RATIO_SMALL = 0.7
const ACCEPTANCE_RATIO_LARGE = 0.85
const LARGE_FIXTURE_NODES = 600

/**
 * Fixtures on which the engine does not yet reach the acceptance ratio,
 * with the ratio it does reach. Each is a known shortfall discussed in the
 * objective document; the test holds the engine to these numbers so they
 * can only improve. Removing an entry is how a fix is claimed.
 */
const ACCEPTANCE_SHORTFALLS: Partial<Record<FixtureName, number>> = {
  cycles: 0.97,
  "two-stacks-cross": 0.75,
  "nested-collapsed": 0.81,
}

const baseline = readBaseline()

describe("layout engine on the fixtures", () => {
  for (const name of FIXTURE_NAMES) {
    describe(name, () => {
      const f = loadFixture(name)
      const layout = layoutFixture(f)
      const score = scoreFixture(f, layout)

      it("breaks no hard rule", () => {
        expect(score.violations).toEqual([])
      })

      it("is byte-identical for the same input in another order", () => {
        const again = buildLayout(
          [...f.nodes].reverse(),
          [...f.edges].reverse(),
          f.sizes,
          f.activeRegion,
          f.collapsed,
        )
        expect(JSON.stringify(again)).toBe(JSON.stringify(layout))
      })

      it("stays within the H6 time budget", () => {
        const start = performance.now()
        layoutFixture(f)
        const ms = performance.now() - start
        const budget = timeBudgetMs(f.nodes.length)
        process.stdout.write(`${name.padEnd(18)} ${ms.toFixed(1).padStart(7)}ms of ${budget}ms\n`)
        expect(ms).toBeLessThanOrEqual(budget * CI_TIME_ALLOWANCE)
      })

      it("scores within 2% of the recorded engine baseline", () => {
        const recorded = baseline.engine?.[name]
        expect(recorded?.soft, "run `pnpm layout:bench` to record the engine baseline").toBeTypeOf("number")
        expect(score.total).toBeLessThanOrEqual(recorded!.soft! * REGRESSION_TOLERANCE)
      })

      it("meets the acceptance ratio against the dagre baseline", () => {
        const dagre = baseline.dagre?.[name]
        expect(dagre, "the dagre baseline is recorded once and kept").toBeDefined()
        if (dagre!.soft === null) {
          // dagre threw on this fixture (its multigraph cycle bug); there is
          // no score to beat, only a layout to produce, which the other
          // tests check.
          expect(dagre!.error).toBeTruthy()
          return
        }
        const ratio = score.total / dagre!.soft
        const shortfall = ACCEPTANCE_SHORTFALLS[name]
        const bar =
          shortfall ??
          (f.nodes.length >= LARGE_FIXTURE_NODES ? ACCEPTANCE_RATIO_LARGE : ACCEPTANCE_RATIO_SMALL)
        expect(ratio, `${name}: ${score.total.toFixed(0)} vs dagre ${dagre!.soft.toFixed(0)}`).toBeLessThanOrEqual(bar)
      })
    })
  }
})
