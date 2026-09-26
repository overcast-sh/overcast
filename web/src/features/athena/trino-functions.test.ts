import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { pinnedTrinoVersion } from "../../../scripts/generate-trino-functions.mjs"
import { TRINO_FUNCTIONS } from "./trino-functions"

describe("the Trino function list", () => {
  it("was generated from the Trino version the engine image is pinned to", () => {
    // When this fails, DefaultAthenaEngineImage moved to another Trino: rerun
    // web/scripts/generate-trino-functions.mjs against the new image.
    const config = readFileSync(resolve(__dirname, "../../../../internal/config/config.go"), "utf8")
    expect(TRINO_FUNCTIONS.trinoVersion).toBe(pinnedTrinoVersion(config))
  })

  it("carries the common functions with their signatures", () => {
    const dateTrunc = TRINO_FUNCTIONS.functions.find((f) => f.name === "date_trunc")
    expect(dateTrunc?.signatures.length).toBeGreaterThan(0)
  })
})
