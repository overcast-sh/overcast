/**
 * A revision is Pending while its package is prepared, then Active or Failed
 * for good: the states, the polling, the failure reason, the create form's
 * word on versioned buckets and the backend's refusal, and the logging
 * configuration with its links to CloudWatch Logs.
 */
import { http, HttpResponse } from "msw"
import { server } from "@/test/server"
import { useFakeTimers } from "@/test/fake-timers"
import {
  act,
  createTestQueryClient,
  renderWithRouter,
  screen,
  waitFor,
  within,
} from "@/test/render"
import { iamRolesQueryOptions } from "@/features/iam/data"
import { s3BucketsQueryOptions } from "@/features/s3/data"
import type { WebEndpoint, WebRevision, WebRevisionDetail } from "@/services/api/lambda-web"
import {
  webEndpointsQueryOptions,
  webRevisionQueryOptions,
  webRevisionsQueryOptions,
} from "../data"
import { TRANSITION_POLL_MS } from "../models"
import { RevisionsTab } from "./revisions-tab"

const FN_ARN = "arn:aws:lambda:us-east-1:000000000000:web-function/shop"
const ROLE = "arn:aws:iam::000000000000:role/web-role"

const revision = (id: string, patch: Partial<WebRevision> = {}): WebRevision => ({
  revisionArn: `${FN_ARN}/revision/${id}`,
  revisionId: id,
  state: "Active",
  stateReason: "",
  createdAt: new Date("2026-10-01T10:00:00Z"),
  ...patch,
})

const detail = (id: string, patch: Partial<WebRevisionDetail> = {}): WebRevisionDetail => ({
  functionArn: FN_ARN,
  revisionArn: `${FN_ARN}/revision/${id}`,
  revisionId: id,
  state: "Active",
  stateReason: "",
  createdAt: new Date("2026-10-01T10:00:00Z"),
  buildConfig: {
    codeConfig: { s3Object: { bucket: "code", key: "app.zip", versionId: "v1" } },
    runtimeConfig: { runtime: "nodejs24.x" },
  },
  serviceConfig: { executionRoleArn: ROLE, timeoutSeconds: 30 },
  ...patch,
})

function renderTab(revisions: WebRevision[], details: WebRevisionDetail[] = []) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(webRevisionsQueryOptions("shop").queryKey, revisions)
  queryClient.setQueryData(webEndpointsQueryOptions("shop").queryKey, [] as WebEndpoint[])
  for (const d of details) {
    queryClient.setQueryData(webRevisionQueryOptions("shop", d.revisionId ?? "").queryKey, d)
  }
  // What the revision form's pickers list.
  queryClient.setQueryData(s3BucketsQueryOptions().queryKey, [])
  queryClient.setQueryData(iamRolesQueryOptions().queryKey, [])
  return renderWithRouter(() => <RevisionsTab functionName="shop" />, { queryClient })
}

/** Opens a revision's detail by its row: the id cell holds a copy button, so it is not the target. */
async function openRevision(user: ReturnType<typeof renderTab>["user"], id: string) {
  const row = (await screen.findByText(id)).closest("tr") as HTMLElement
  await user.click(within(row).getAllByRole("cell")[3])
}

describe("revision states", () => {
  it("shows a Failed revision's reason in its row", async () => {
    renderTab([
      revision("rev-bad", {
        state: "Failed",
        stateReason: "The package could not be read from S3.",
      }),
    ])
    const row = (await screen.findByText("rev-bad")).closest("tr") as HTMLElement
    expect(within(row).getByText("Failed")).toBeInTheDocument()
    expect(within(row).getByText("The package could not be read from S3.")).toBeInTheDocument()
  })

  it("shows a Failed revision's reason and every error in its detail", async () => {
    const { user } = renderTab(
      [revision("rev-bad", { state: "Failed", stateReason: "Code unavailable" })],
      [
        detail("rev-bad", {
          state: "Failed",
          stateReason: "Code unavailable",
          errors: [
            {
              attribute: "buildConfig.codeConfig.s3Object",
              errorCode: "S3ObjectNotFound",
              errorMessage: "The object does not exist.",
            },
          ],
        }),
      ],
    )
    await openRevision(user, "rev-bad")
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("This revision failed and cannot be used")
    expect(alert).toHaveTextContent("Code unavailable")
    expect(alert).toHaveTextContent("S3ObjectNotFound")
    expect(alert).toHaveTextContent("buildConfig.codeConfig.s3Object")
    expect(alert).toHaveTextContent("The object does not exist.")
    expect(alert).toHaveTextContent("create a new one")
  })

  it("says a Pending revision's package is being prepared", async () => {
    const { user } = renderTab(
      [revision("rev-new", { state: "Pending" })],
      [detail("rev-new", { state: "Pending" })],
    )
    await openRevision(user, "rev-new")
    expect(await screen.findByText(/preparing this revision/)).toBeInTheDocument()
  })

  describe("while a revision is Pending", () => {
    useFakeTimers()

    it("polls until it is Active", async () => {
      server.use(
        http.post("*/2025-03-07/web-functions/shop/list-revisions", () =>
          HttpResponse.json({
            revisions: [
              { ...revision("rev-new", { state: "Active" }), createdAt: "2026-10-01T10:00:00Z" },
            ],
          }),
        ),
      )
      renderTab([revision("rev-new", { state: "Pending" })])
      expect(await screen.findByText("Pending")).toBeInTheDocument()
      await act(() => vi.advanceTimersByTimeAsync(TRANSITION_POLL_MS + 100))
      expect(await screen.findByText("Active")).toBeInTheDocument()
      expect(screen.queryByText("Pending")).not.toBeInTheDocument()
    })
  })
})

describe("revision logs", () => {
  it("links the default log group and its system twin from a revision", async () => {
    const { user } = renderTab([revision("rev-ab12c")], [detail("rev-ab12c")])
    await openRevision(user, "rev-ab12c")
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByRole("link", { name: "/aws/lambda/web/shop" })).toHaveAttribute(
      "href",
      expect.stringContaining("groupName=%2Faws%2Flambda%2Fweb%2Fshop"),
    )
    expect(
      within(dialog).getByRole("link", { name: "/aws/lambda/web/shop.local" }),
    ).toHaveAttribute(
      "href",
      expect.stringContaining("groupName=%2Faws%2Flambda%2Fweb%2Fshop.local"),
    )
  })

  it("links the revision's own log group, and shows its log levels", async () => {
    const { user } = renderTab(
      [revision("rev-ab12c")],
      [
        detail("rev-ab12c", {
          serviceConfig: {
            executionRoleArn: ROLE,
            telemetryConfig: {
              loggingConfig: {
                logGroup: "/custom/shop",
                applicationLogLevel: "DEBUG",
                systemLogLevel: "WARN",
              },
            },
          },
        }),
      ],
    )
    await openRevision(user, "rev-ab12c")
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByRole("link", { name: "/custom/shop" })).toBeInTheDocument()
    expect(within(dialog).getByRole("link", { name: "/custom/shop.local" })).toBeInTheDocument()
    expect(
      within(dialog).queryByRole("link", { name: "/aws/lambda/web/shop" }),
    ).not.toBeInTheDocument()
    expect(within(dialog).getByText("DEBUG")).toBeInTheDocument()
    expect(within(dialog).getByText("WARN")).toBeInTheDocument()
  })
})

describe("creating a revision", () => {
  async function openForm() {
    const rendered = renderTab([])
    await rendered.user.click(
      (await screen.findAllByRole("button", { name: "Create revision" }))[0],
    )
    await screen.findByText("The package is read from S3 now")
    return rendered
  }

  async function fillRequired(user: ReturnType<typeof renderTab>["user"]) {
    await user.type(screen.getByLabelText("S3 bucket"), "code")
    await user.type(screen.getByLabelText(/Object key/), "app.zip")
    await user.type(screen.getByLabelText(/Execution role ARN/), `${ROLE}{Enter}`)
  }

  it("says the bucket must be versioned and the object must exist", async () => {
    await openForm()
    expect(screen.getByText("The package is read from S3 now")).toBeInTheDocument()
    expect(screen.getByText(/versioning enabled and the object must exist/)).toBeInTheDocument()
  })

  it("shows the backend's ValidationException message inline and keeps what was typed", async () => {
    server.use(
      http.post("*/2025-03-07/web-functions/shop/revisions", () =>
        HttpResponse.json(
          { message: 'The S3 bucket "code" must have versioning enabled.' },
          { status: 400, headers: { "x-amzn-errortype": "ValidationException" } },
        ),
      ),
    )
    const { user } = await openForm()
    await fillRequired(user)
    await user.click(screen.getByRole("button", { name: "Create revision", hidden: false }))
    expect(
      await screen.findByText('The S3 bucket "code" must have versioning enabled.'),
    ).toBeInTheDocument()
    expect(screen.getByLabelText(/Object key/)).toHaveValue("app.zip")
  })

  it("sends the logging configuration it was given, and none when left alone", async () => {
    const seen: { body?: Record<string, unknown> } = {}
    server.use(
      http.post("*/2025-03-07/web-functions/shop/revisions", async ({ request }) => {
        seen.body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ revisionId: "rev-new" })
      }),
    )
    const { user } = await openForm()
    await fillRequired(user)
    await user.type(screen.getByLabelText("Log group"), "/custom/shop")
    await user.selectOptions(screen.getByLabelText("Application log level"), "DEBUG")
    await user.selectOptions(screen.getByLabelText("System log level"), "WARN")
    await user.click(screen.getByRole("button", { name: "Create revision", hidden: false }))
    await waitFor(() => expect(seen.body).toBeDefined(), { timeout: 4_000 })
    expect(seen.body?.serviceConfig).toMatchObject({
      executionRoleArn: ROLE,
      telemetryConfig: {
        loggingConfig: {
          logGroup: "/custom/shop",
          applicationLogLevel: "DEBUG",
          systemLogLevel: "WARN",
        },
      },
    })
  })

  it("leaves telemetryConfig out when the logging fields are blank", async () => {
    const seen: { body?: Record<string, unknown> } = {}
    server.use(
      http.post("*/2025-03-07/web-functions/shop/revisions", async ({ request }) => {
        seen.body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ revisionId: "rev-new" })
      }),
    )
    const { user } = await openForm()
    await fillRequired(user)
    await user.click(screen.getByRole("button", { name: "Create revision", hidden: false }))
    await waitFor(() => expect(seen.body).toBeDefined(), { timeout: 4_000 })
    expect(seen.body?.serviceConfig).not.toHaveProperty("telemetryConfig")
  })

  it("refuses a log group name CloudWatch Logs would not accept", async () => {
    const { user } = await openForm()
    await fillRequired(user)
    await user.type(screen.getByLabelText("Log group"), "has spaces")
    await user.click(screen.getByRole("button", { name: "Create revision", hidden: false }))
    expect(await screen.findByText(/up to 512 characters/)).toBeInTheDocument()
  })
})
