import { http, HttpResponse } from "msw"
import { server } from "@/test/server"
import { createTestQueryClient, renderWithRouter, screen, waitFor, within } from "@/test/render"
import type { WebEndpoint, WebFunction, WebRevision } from "@/services/api/lambda-web"
import { iamRolesQueryOptions } from "@/features/iam/data"
import { s3BucketsQueryOptions } from "@/features/s3/data"
import {
  webEndpointsQueryOptions,
  webFunctionsQueryOptions,
  webRevisionsQueryOptions,
} from "../data"
import { WebFunctionList } from "./web-function-list"

const ARN = "arn:aws:lambda:us-east-1:000000000000:web-function/shop"

const shop: WebFunction = {
  functionName: "shop",
  functionArn: ARN,
  state: "Active",
  stateReason: "",
  createdAt: new Date("2026-10-01T10:00:00Z"),
  updatedAt: new Date("2026-10-01T10:00:00Z"),
}

function renderList(functions: WebFunction[] = [shop], filter = "") {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(webFunctionsQueryOptions().queryKey, functions)
  queryClient.setQueryData(webEndpointsQueryOptions("shop").queryKey, [{}, {}] as WebEndpoint[])
  queryClient.setQueryData(webRevisionsQueryOptions("shop").queryKey, [{}, {}, {}] as WebRevision[])
  // What the revision form's pickers list.
  queryClient.setQueryData(s3BucketsQueryOptions().queryKey, [])
  queryClient.setQueryData(iamRolesQueryOptions().queryKey, [])
  return renderWithRouter(
    () => <WebFunctionList filter={filter} onFilterChange={() => {}} onSortChange={() => {}} />,
    { queryClient },
  )
}

describe("WebFunctionList", () => {
  // A create invalidates the list and the counts under it; they refetch.
  beforeEach(() => {
    server.use(
      http.post("*/2025-03-07/web-functions", () => HttpResponse.json({ functions: [] })),
      http.post("*/2025-03-07/web-functions/:name/list-endpoints", () =>
        HttpResponse.json({ endpoints: [] }),
      ),
      http.post("*/2025-03-07/web-functions/:name/list-revisions", () =>
        HttpResponse.json({ revisions: [] }),
      ),
    )
  })

  it("lists a function with its state and how many endpoints and revisions it holds", async () => {
    renderList()
    const row = (await screen.findByText("shop")).closest("tr") as HTMLElement
    expect(within(row).getByText("Active")).toBeInTheDocument()
    expect(within(row).getByText("2")).toBeInTheDocument()
    expect(within(row).getByText("3")).toBeInTheDocument()
    expect(within(row).getByRole("button", { name: "Copy ARN" })).toBeInTheDocument()
  })

  it("says up front that the endpoints are not served", async () => {
    renderList()
    expect(await screen.findByText("Endpoint domains are not served yet")).toBeInTheDocument()
  })

  it("explains an empty region and offers to create the first function", async () => {
    renderList([])
    expect(await screen.findByText("No web functions yet")).toBeInTheDocument()
  })

  it("filters by name", async () => {
    renderList(
      [shop, { ...shop, functionName: "blog", functionArn: ARN.replace("shop", "blog") }],
      "bl",
    )
    expect(await screen.findByText("blog")).toBeInTheDocument()
    expect(screen.queryByText("shop")).not.toBeInTheDocument()
  })

  /** Answers CreateWebFunction with the function, and keeps the request body. */
  function captureCreate() {
    const seen: { body?: Record<string, unknown> } = {}
    server.use(
      http.put("*/2025-03-07/web-functions", async ({ request }) => {
        seen.body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({
          functionName: "blog",
          functionArn: ARN.replace("shop", "blog"),
          state: "Pending",
          stateReason: "",
          createdAt: "2026-10-01T10:00:00Z",
          updatedAt: "2026-10-01T10:00:00Z",
        })
      }),
    )
    return seen
  }

  it("creates just a function, and opens its page", async () => {
    const seen = captureCreate()
    const { user, router } = renderList([])
    await user.click((await screen.findAllByRole("button", { name: "Create web function" }))[0])
    await user.type(await screen.findByLabelText(/Function name/), "blog")
    await user.click(screen.getByRole("button", { name: "Create" }))
    await waitFor(() => expect(router.state.location.pathname).toBe("/lambda-web/blog"))
    expect(seen.body).toEqual({ functionName: "blog" })
  })

  it("creates the function with its first revision and endpoint in one call", async () => {
    const seen = captureCreate()
    const { user } = renderList([])
    await user.click((await screen.findAllByRole("button", { name: "Create web function" }))[0])
    await user.type(await screen.findByLabelText(/Function name/), "blog")
    await user.click(screen.getByRole("switch", { name: "Create a first revision" }))
    await user.type(await screen.findByLabelText(/Object key/), "app.zip")
    await user.type(screen.getByLabelText("S3 bucket"), "code-bucket")
    await user.type(
      screen.getByLabelText(/Execution role ARN/),
      "arn:aws:iam::000000000000:role/web-role{Enter}",
    )
    await user.click(screen.getByRole("switch", { name: "Create a first endpoint" }))
    await user.type(await screen.findByLabelText(/^Endpoint name/), "prod")
    await user.click(screen.getByRole("button", { name: "Create" }))
    await waitFor(() => expect(seen.body).toBeDefined())
    expect(seen.body).toMatchObject({
      functionName: "blog",
      revisionConfig: {
        buildConfig: {
          codeConfig: { s3Object: { bucket: "code-bucket", key: "app.zip" } },
          runtimeConfig: { runtime: "nodejs24.x" },
        },
        serviceConfig: { executionRoleArn: "arn:aws:iam::000000000000:role/web-role" },
      },
      endpointConfig: {
        endpointName: "prod",
        endpointType: "HomeRegion",
        authType: "ApplicationManaged",
        autoDeploymentMode: "LatestRevision",
      },
    })
  }, 20_000)

  it("refuses an invalid name before asking the service", async () => {
    const { user } = renderList([])
    await user.click((await screen.findAllByRole("button", { name: "Create web function" }))[0])
    await user.type(await screen.findByLabelText(/Function name/), "-bad")
    await user.click(screen.getByRole("button", { name: "Create" }))
    expect(
      await screen.findAllByText(/can't begin|not starting or ending|Letters, numbers/),
    ).not.toHaveLength(0)
  })
})
