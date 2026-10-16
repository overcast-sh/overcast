/**
 * The Lambda Web Functions search contributors: what each finds, and where each
 * result leads — the routes arn-routes.ts sends the same resources' ARNs to.
 */
import { QueryClient } from "@tanstack/react-query"
import { http, HttpResponse } from "msw"
import { describe, expect, it } from "vitest"
import { lambdaWebKeys } from "@/features/lambda-web/data"
import { runSearch, type SearchContext } from "@/lib/search"
import { server } from "@/test/server"
import "./lambda-web"

function ctx(queryClient = new QueryClient()): SearchContext {
  return {
    queryClient,
    endpoint: { baseUrl: "http://localhost:4566", region: "us-east-1", label: "Local" },
  }
}

const shop = {
  functionName: "shop",
  functionArn: "arn:aws:lambda:us-east-1:000000000000:web-function/shop",
  state: "Active",
}

describe("Lambda Web Functions search contributors", () => {
  it("finds a web function by name and opens its page", async () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(lambdaWebKeys.functions(), [shop])

    const results = (await runSearch("sho", ctx(queryClient))).get("/lambda-web") ?? []

    expect(results).toContainEqual(
      expect.objectContaining({
        label: "shop",
        type: "Web function",
        href: "/lambda-web/shop",
      }),
    )
  })

  it("finds an endpoint across functions and opens the function's Endpoints tab", async () => {
    server.use(
      http.post("*/2025-03-07/web-functions", () => HttpResponse.json({ functions: [shop] })),
      http.post("*/2025-03-07/web-functions/shop/list-endpoints", () =>
        HttpResponse.json({
          endpoints: [
            {
              endpointName: "prod",
              endpointArn: `${shop.functionArn}/endpoint/prod`,
              domainName: "abc.lambda-url.us-east-1.on.aws",
            },
          ],
        }),
      ),
    )

    const results = (await runSearch("prod", ctx())).get("/lambda-web") ?? []

    expect(results).toEqual([
      expect.objectContaining({
        label: "prod",
        sublabel: "shop",
        type: "Endpoint",
        href: "/lambda-web/shop?tab=endpoints",
      }),
    ])
  })
})
