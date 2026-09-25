/**
 * The registry's keys are the backend's service names: the dashboard looks
 * each routed entry's tier up by its key in `/_overcast/health`'s
 * `serviceTiers`, and an entry the backend does not know falls back to
 * "fully emulated" without a word. Checked against the backend's own tier
 * table, as the unsupported-services catalogue is.
 */
import { describe, expect, it } from "vitest"
import { backendTiers } from "@/test/backend-tiers"
import { SERVICES, type ServiceEntry } from "./service-registry"

describe("SERVICES", () => {
  const tiers = backendTiers()
  const routed = Object.entries(SERVICES as Record<string, ServiceEntry>).filter(
    ([, entry]) => entry.to !== undefined,
  )

  it("names every routed service as the backend does", () => {
    const unknown = routed.map(([key]) => key).filter((key) => !tiers.has(key))

    expect(unknown).toEqual([])
  })

  // #2085: the data-lake services, registered before their console pages.
  it.each(["athena", "glue", "s3tables"])(
    "registers %s in the analytics category with its docs",
    (key) => {
      const entry: ServiceEntry | undefined = (SERVICES as Record<string, ServiceEntry>)[key]
      expect(entry).toMatchObject({ to: `/${key}`, category: "analytics", docKey: key })
    },
  )
})
