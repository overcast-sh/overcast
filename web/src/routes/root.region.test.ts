import { beforeEach, describe, expect, it } from "vitest"
import { delay, http, HttpResponse } from "msw"
import { server } from "@/test/server"
import { DEFAULT_ENDPOINT } from "@/services/discovery"
import { endpointStore } from "@/services/endpoint-store"
import { Route } from "./__root"

describe("root route > ?region=", () => {
  beforeEach(() => {
    endpointStore.reset()
    server.use(
      http.get(`${DEFAULT_ENDPOINT.baseUrl}/_overcast/info`, async () => {
        await delay(20)
        return HttpResponse.json({ region: "eu-west-1" })
      }),
    )
  })

  // A link naming the region the console already defaults to used to change
  // nothing, so nothing recorded it as chosen and the server's default
  // replaced it when that arrived. Every console link carries ?region= now.
  it("keeps a URL region equal to the current one over the server's default", async () => {
    const seeding = endpointStore.seedServerRegion()
    await Route.options.beforeLoad?.({
      search: { region: DEFAULT_ENDPOINT.region },
    } as never)
    await seeding
    expect(endpointStore.get().region).toBe(DEFAULT_ENDPOINT.region)
  })
})
