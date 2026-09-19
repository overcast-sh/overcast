/**
 * Shared helpers for measuring the layout on the fixtures: the benchmark,
 * the tuner and the engine tests all lay out, score and time a fixture the
 * same way, and read the same baseline file.
 */

import { readFileSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"
import { buildLayout, type MapLayout } from "../map-layout"
import { scoreLayout, SOFT_RULES, type LayoutScore, type SoftRuleId } from "../map-layout-score"
import type { FixtureName, LayoutFixture } from "."

/** One fixture's recorded result. */
export interface BaselineEntry {
  nodes: number
  edges: number
  /** Infinity is not JSON; an invalid layout records null and its violation count. */
  score: number | null
  /** The soft total whether or not the layout was valid; null when the layout threw. */
  soft: number | null
  terms: Record<SoftRuleId, number> | null
  counts: Record<SoftRuleId, number> | null
  violations: number
  sampleViolations: string[]
  ms: number | null
  /** Set when the implementation threw instead of producing a layout. */
  error?: string
}
export type BaselineFile = Partial<Record<string, Partial<Record<FixtureName, BaselineEntry>>>>

// Resolved through fileURLToPath rather than `new URL(…, import.meta.url)`,
// which Vite would rewrite into an asset import.
export const BASELINE_PATH = path.join(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
  "map-layout.baseline.json",
)

/** Timed runs per fixture; the median is recorded so one GC pause does not set the number. */
const TIMED_RUNS = 3

export function readBaseline(): BaselineFile {
  try {
    return JSON.parse(readFileSync(BASELINE_PATH, "utf8")) as BaselineFile
  } catch {
    return {}
  }
}

/** Lays out a fixture once, untimed. */
export function layoutFixture(f: LayoutFixture): MapLayout {
  return buildLayout(f.nodes, f.edges, f.sizes, f.activeRegion, f.collapsed)
}

export function scoreFixture(f: LayoutFixture, layout: MapLayout): LayoutScore {
  return scoreLayout(layout, f.nodes, f.edges, f.sizes, f.activeRegion)
}

/** Median wall time of TIMED_RUNS single calls, after one warm-up call. */
export function timeFixture(f: LayoutFixture): number {
  layoutFixture(f)
  const times: number[] = []
  for (let i = 0; i < TIMED_RUNS; i++) {
    const start = performance.now()
    layoutFixture(f)
    times.push(performance.now() - start)
  }
  times.sort((a, b) => a - b)
  return times[Math.floor(times.length / 2)]
}

export function toEntry(f: LayoutFixture, score: LayoutScore, ms: number): BaselineEntry {
  return {
    nodes: f.nodes.length,
    edges: f.edges.length,
    score: Number.isFinite(score.total) ? round(score.total) : null,
    soft: round(score.soft),
    terms: roundAll(score.terms),
    counts: roundAll(score.counts),
    violations: score.violations.length,
    sampleViolations: score.violations.slice(0, 5),
    ms: round(ms),
  }
}

/** Records an implementation that threw on a fixture, so the table still has a row for it. */
export function failedEntry(f: LayoutFixture, err: unknown): BaselineEntry {
  return {
    nodes: f.nodes.length,
    edges: f.edges.length,
    score: null,
    soft: null,
    terms: null,
    counts: null,
    violations: 0,
    sampleViolations: [],
    ms: null,
    error: String(err),
  }
}

function round(n: number): number {
  return Math.round(n * 100) / 100
}

function roundAll(terms: Record<SoftRuleId, number>): Record<SoftRuleId, number> {
  const out = {} as Record<SoftRuleId, number>
  for (const id of SOFT_RULES) out[id] = round(terms[id])
  return out
}

/** The three soft terms contributing most, as "S1=120 S2=33 …". */
export function worstTerms(entry: BaselineEntry): string {
  const terms = entry.terms
  if (!terms) return entry.error ?? "threw"
  return SOFT_RULES.map((id) => [id, terms[id]] as const)
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 3)
    .map(([id, v]) => `${id}=${Math.round(v)}`)
    .join(" ")
}
