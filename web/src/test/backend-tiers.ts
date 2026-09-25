import { readFileSync } from "node:fs"

/**
 * `ServiceTiers` from `internal/router/tiers.go`, as service name → tier in
 * lower case ("full", "partial", "inert", "stub"). That table is the backend's
 * own list of every service it registers — its test enforces as much — so the
 * console's tests read it rather than restate it. vitest's cwd is `web/`.
 */
export function backendTiers(): Map<string, string> {
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
