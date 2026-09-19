/**
 * Layout benchmark: scores and times `buildLayout` on every fixture and
 * records the result in map-layout.baseline.json.
 *
 * Runs only under `pnpm layout:bench` (`--mode bench`), which records under
 * the "engine" key; `--mode bench-dagre` recorded the "dagre" key once,
 * before dagre was replaced, and that entry is the acceptance bar the tests
 * hold the engine to. A script in a test's clothes: vitest is the one runner
 * here that resolves the `@/` alias and extension-less imports.
 *
 * Prints a table — fixture, score, worst terms, time, delta against the
 * other key — through process.stdout so it survives vitest's console capture.
 */

import { writeFileSync } from "node:fs"
import { describe, expect, it } from "vitest"
import { FIXTURE_NAMES, loadFixture } from "./__fixtures__"
import {
  BASELINE_PATH,
  failedEntry,
  layoutFixture,
  readBaseline,
  scoreFixture,
  timeFixture,
  toEntry,
  worstTerms,
  type BaselineEntry,
  type BaselineFile,
} from "./__fixtures__/baseline"

const mode = import.meta.env.MODE
const key = mode === "bench-dagre" ? "dagre" : "engine"
const against = key === "dagre" ? "engine" : "dagre"

describe.skipIf(!mode.startsWith("bench"))("layout benchmark", () => {
  it(`records the ${key} baseline`, () => {
    const baseline = readBaseline()
    const results: BaselineFile[string] = {}
    const out: string[] = []
    out.push(
      `${"fixture".padEnd(18)} ${"nodes".padStart(5)} ${"edges".padStart(5)} ${"score".padStart(9)} ${"viol".padStart(4)} ${"ms".padStart(7)}  ${"worst terms".padEnd(28)} vs ${against}`,
    )
    for (const name of FIXTURE_NAMES) {
      const f = loadFixture(name)
      let entry: BaselineEntry
      try {
        const score = scoreFixture(f, layoutFixture(f))
        entry = toEntry(f, score, timeFixture(f))
      } catch (err) {
        entry = failedEntry(f, err)
      }
      results[name] = entry
      const other = baseline[against]?.[name]
      const ratio =
        other && other.soft && entry.soft !== null ? `${(entry.soft / other.soft).toFixed(2)}×` : "-"
      out.push(
        `${name.padEnd(18)} ${String(entry.nodes).padStart(5)} ${String(entry.edges).padStart(5)} ${(entry.score ?? entry.soft ?? NaN).toFixed(0).padStart(9)} ${String(entry.violations).padStart(4)} ${(entry.ms ?? NaN).toFixed(1).padStart(7)}  ${worstTerms(entry).padEnd(28)} ${ratio}`,
      )
    }
    expect(Object.keys(results)).toHaveLength(FIXTURE_NAMES.length)
    baseline[key] = results
    writeFileSync(BASELINE_PATH, `${JSON.stringify(baseline, null, 2)}\n`)
    process.stdout.write(`\n${out.join("\n")}\n\nwrote ${key} → ${BASELINE_PATH}\n`)
  })
})
