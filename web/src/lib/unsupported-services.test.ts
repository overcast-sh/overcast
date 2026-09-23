/**
 * The catalogue is the console's list of services Overcast does **not**
 * emulate: it feeds the `/$service` placeholder page, the greyed-out chips in
 * global search and the dashboard's "Other AWS Services" list, all of which
 * tell the user the service is unsupported or a 501 stub. An entry for a
 * service the backend implements is therefore a false claim — #2062 found
 * Athena, Glue, Firehose and OpenSearch listed as not emulated while each was
 * registered at the inert tier and answering calls.
 *
 * The backend's own tier table, `internal/router/tiers.go`, is the source of
 * truth (every registered service must appear in it — its own test enforces
 * that), so this reads it rather than restating it.
 */
import { readFileSync } from "node:fs"
import { describe, expect, it } from "vitest"
import { CATALOG, CATALOG_BY_ID } from "./unsupported-services"

/** `ServiceTiers` from tiers.go, as service name → tier. vitest's cwd is `web/`. */
function backendTiers(): Map<string, string> {
  const source = readFileSync("../internal/router/tiers.go", "utf8")
  const block = /var ServiceTiers = map\[string\]EmulationTier\{([\s\S]*?)\n\}/.exec(source)?.[1]
  if (!block) throw new Error("ServiceTiers map not found in internal/router/tiers.go")
  return new Map(
    [...block.matchAll(/^\s*"([a-z0-9]+)":\s*Tier([A-Za-z]+),/gm)].map(([, name, tier]) => [
      name,
      tier.toLowerCase(),
    ]),
  )
}

describe("unsupported-services CATALOG", () => {
  const tiers = backendTiers()

  it("reads the backend tier table", () => {
    // Guards the parser: an empty map would make every assertion below vacuous.
    expect(tiers.get("s3")).toBe("full")
    expect(tiers.get("shield")).toBe("stub")
  })

  // #2062, #2081
  it.each([
    "athena",
    "glue",
    "firehose",
    "opensearch",
    "acm",
    "backup",
    "cloudtrail",
    "organizations",
    "route53",
    "transfer",
  ])(
    "does not list %s, which the backend emulates",
    (id) => {
      expect(["stub", "unsupported", undefined]).not.toContain(tiers.get(id))
      expect(CATALOG_BY_ID[id]).toBeUndefined()
    },
  )

  it("lists no service the backend implements at inert tier or above", () => {
    const implemented = CATALOG.filter((entry) => {
      const tier = tiers.get(entry.id)
      return tier !== undefined && tier !== "stub" && tier !== "unsupported"
    }).map((entry) => entry.id)

    expect(implemented).toEqual([])
  })

  // #2081: bedrock was a backend stub catalogued as "unsupported". The two
  // render differently (a "Stub" chip, and a placeholder that says which
  // operations answer), so the catalogue must say which one the backend is.
  it("marks each entry stub exactly when the backend registers it as a stub", () => {
    const mismatched = CATALOG.filter(
      (entry) => entry.tier !== (tiers.get(entry.id) === "stub" ? "stub" : "unsupported"),
    ).map((entry) => `${entry.id}: catalogue ${entry.tier}, backend ${tiers.get(entry.id)}`)

    expect(mismatched).toEqual([])
    expect(CATALOG_BY_ID.bedrock?.tier).toBe("stub")
  })
})
