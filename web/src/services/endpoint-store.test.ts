import { beforeEach, describe, expect, it } from "vitest"
import { delay, http, HttpResponse } from "msw"
import { server } from "@/test/server"
import { DEFAULT_ENDPOINT } from "./discovery"
import { endpointStore } from "./endpoint-store"

const INFO_URL = `${DEFAULT_ENDPOINT.baseUrl}/_overcast/info`

describe("endpointStore.seedServerRegion", () => {
  beforeEach(() => {
    endpointStore.reset()
    server.use(
      http.get(INFO_URL, async () => {
        await delay(20)
        return HttpResponse.json({ region: "eu-west-1" })
      }),
    )
  })

  it("applies the server's default region when nothing has chosen one", async () => {
    await endpointStore.seedServerRegion()
    expect(endpointStore.get().region).toBe("eu-west-1")
  })

  // The router applies a `?region=` in its beforeLoad, which runs while the
  // /_overcast/info request is still in flight. The seed used to check for a
  // chosen region only before sending the request, so its later answer
  // overwrote the URL's region with the server default.
  it("keeps a region chosen while the server's answer was in flight", async () => {
    const seeding = endpointStore.seedServerRegion()
    endpointStore.set({ ...endpointStore.get(), region: "ap-southeast-2" })
    await seeding
    expect(endpointStore.get().region).toBe("ap-southeast-2")
  })
})
