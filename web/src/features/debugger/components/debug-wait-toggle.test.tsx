import { http, HttpResponse } from "msw"
import { createTestQueryClient, render, screen, waitFor } from "@/test/render"
import { server } from "@/test/server"
import { debugTarget } from "@/test/debug-target"
import type * as ApiModule from "@/services/api"
import { lambda } from "@/services/api"
import { lambdaKeys } from "@/features/lambda/data"
import { debuggerTargetQueryOptions } from "@/features/debugger/data"
import { withWaitForDebugger } from "@/features/debugger/target"
import type { DebuggerTarget } from "@/types"
import { DebugWaitToggle } from "./debug-wait-toggle"

vi.mock("@/services/api", async (importOriginal) => {
  const actual = await importOriginal<typeof ApiModule>()
  return {
    ...actual,
    lambda: { ...actual.lambda, tagResource: vi.fn(), untagResource: vi.fn() },
  }
})

const ARN = "arn:aws:lambda:us-east-1:000000000000:function:my-fn"
const KEY = debuggerTargetQueryOptions("lambda", "my-fn").queryKey

/**
 * The descriptor seeded in the cache reads `on`; what the server answers when
 * the toggle asks again after its call is `served` — the state the tag call
 * left, as the real server reports it — and `fetches` counts those asks.
 */
function toggle(on: boolean, served: DebuggerTarget = withWaitForDebugger(debugTarget(), on)) {
  const fetches = { count: 0 }
  server.use(
    http.get("/api/debugger/targets/lambda/my-fn", () => {
      fetches.count += 1
      return HttpResponse.json(served)
    }),
  )
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(KEY, withWaitForDebugger(debugTarget(), on))
  queryClient.setQueryData(lambdaKeys.tags(ARN), on ? { "overcast:debug-wait": "true" } : {})
  const view = render(
    <DebugWaitToggle service="lambda" resource="my-fn" resourceArn={ARN} />,
    { queryClient },
  )
  return { ...view, queryClient, fetches }
}

const waitSwitch = () =>
  screen.getByRole("switch", { name: "Wait for a debugger before the first invocation" })

describe("DebugWaitToggle", () => {
  it("reads the descriptor's flag and says what the wait does", () => {
    toggle(true)
    expect(waitSwitch()).toHaveAttribute("aria-checked", "true")
    expect(screen.getByText(/Invocations wait until a debugger attaches/)).toBeInTheDocument()
  })

  it("tags the function through TagResource, flipping at once, and asks for the descriptor again after", async () => {
    let resolveTag: () => void = () => {}
    vi.mocked(lambda.tagResource).mockReturnValue(
      new Promise<void>((resolve) => {
        resolveTag = resolve
      }),
    )
    const { queryClient, fetches, user } = toggle(false, withWaitForDebugger(debugTarget(), true))
    expect(waitSwitch()).toHaveAttribute("aria-checked", "false")

    await user.click(waitSwitch())
    expect(lambda.tagResource).toHaveBeenCalledWith(ARN, { "overcast:debug-wait": "true" })
    // Flipped before the call has answered.
    await waitFor(() => expect(waitSwitch()).toHaveAttribute("aria-checked", "true"))
    expect(fetches.count).toBe(0)

    resolveTag()
    // Once the call has landed the descriptor is asked for again, and the
    // tag list — which the Configuration tab reads — is marked stale.
    await waitFor(() => expect(fetches.count).toBe(1))
    expect(waitSwitch()).toHaveAttribute("aria-checked", "true")
    expect(queryClient.getQueryState(lambdaKeys.tags(ARN))?.isInvalidated).toBe(true)
  })

  it("untags through UntagResource", async () => {
    vi.mocked(lambda.untagResource).mockResolvedValue(undefined)
    const { fetches, user } = toggle(true, withWaitForDebugger(debugTarget(), false))
    await user.click(waitSwitch())
    expect(lambda.untagResource).toHaveBeenCalledWith(ARN, ["overcast:debug-wait"])
    await waitFor(() => expect(waitSwitch()).toHaveAttribute("aria-checked", "false"))
    await waitFor(() => expect(fetches.count).toBe(1))
    expect(waitSwitch()).toHaveAttribute("aria-checked", "false")
  })

  it("flips back and says why when the tag call fails", async () => {
    vi.mocked(lambda.tagResource).mockRejectedValue(new Error("AccessDenied: no"))
    const { queryClient, user } = toggle(false)
    await user.click(waitSwitch())
    expect(await screen.findByText("Could not change the wait for a debugger")).toBeInTheDocument()
    expect(screen.getByText("AccessDenied: no")).toBeInTheDocument()
    await waitFor(() => expect(waitSwitch()).toHaveAttribute("aria-checked", "false"))
    expect(queryClient.getQueryData(KEY)).toEqual(withWaitForDebugger(debugTarget(), false))
  })
})
