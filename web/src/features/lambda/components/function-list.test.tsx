import { renderWithData, screen, within } from "@/test/render"
import { lambdaFunctionsQueryOptions } from "@/features/lambda/data"
import { preflightRegionQueryOptions } from "@/features/preflight/data"
import { debuggerTargetsQueryOptions } from "@/features/debugger/data"
import type { DebuggerTarget, LambdaFunction } from "@/types"
import { FunctionList } from "./function-list"

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
}))

vi.mock("@/features/docs/service-docs-modal", () => ({
  ServiceDocsButton: () => <button type="button">Docs</button>,
  useDocsFromHash: () => [false, vi.fn(), vi.fn()],
}))

vi.mock("./create-wizard", () => ({
  CreateFunctionWizard: () => null,
}))

vi.mock("@/hooks/use-resource-mutation", () => ({
  useResourceMutation: () => ({ mutate: vi.fn(), isPending: false }),
}))

function fn(name: string, memory: number): LambdaFunction {
  return {
    FunctionName: name,
    FunctionArn: `arn:aws:lambda:us-east-1:000000000000:function:${name}`,
    Runtime: "nodejs22.x",
    Handler: "index.handler",
    MemorySize: memory,
    Timeout: 3,
    State: "Active",
  }
}

/** Function names in the order the table currently renders them. */
function renderedNames(): (string | null)[] {
  return screen
    .getAllByRole("row")
    .slice(1)
    .map((row) => within(row).getAllByRole("cell")[0].textContent)
}

const functions = [fn("beta", 512), fn("alpha", 128), fn("gamma", 256)]

describe("FunctionList", () => {
  it("sorts by name on a header click", async () => {
    const { user } = renderWithData(<FunctionList />, [
      [lambdaFunctionsQueryOptions().queryKey, functions],
    ])

    expect(renderedNames()).toEqual(["beta", "alpha", "gamma"])

    await user.click(screen.getByRole("button", { name: /Name/ }))
    expect(renderedNames()).toEqual(["alpha", "beta", "gamma"])
  })

  it("sorts by memory, largest first", async () => {
    const { user } = renderWithData(<FunctionList />, [
      [lambdaFunctionsQueryOptions().queryKey, functions],
    ])

    await user.click(screen.getByRole("button", { name: /Memory/ }))
    expect(renderedNames()).toEqual(["beta", "gamma", "alpha"])
  })

  it("names the function in the delete confirmation", async () => {
    const { user } = renderWithData(<FunctionList />, [
      [lambdaFunctionsQueryOptions().queryKey, functions],
    ])

    await user.click(screen.getByRole("button", { name: "Delete beta" }))

    const dialog = screen.getByRole("dialog")
    expect(within(dialog).getByText("Delete function")).toBeInTheDocument()
    expect(within(dialog).getByText("beta")).toBeInTheDocument()
  })

  it("keeps the region advisory with the empty state", async () => {
    // The advisory explains an empty list, so it has to render *under* that
    // empty state — which is why the table is embedded in this page's own card
    // rather than using ResourceTable's card variant.
    renderWithData(<FunctionList />, [
      [lambdaFunctionsQueryOptions().queryKey, []],
      [
        preflightRegionQueryOptions("lambda-functions").queryKey,
        {
          kind: "lambda-functions",
          region: "us-east-1",
          count: 0,
          elsewhere: [{ region: "ap-southeast-2", count: 3 }],
        },
      ],
    ])

    const empty = await screen.findByText("No functions yet")
    const advisory = screen.getByText(/No functions in/)
    expect(advisory).toHaveTextContent("No functions in us-east-1. There are 3 in ap-southeast-2.")
    // Same card, advisory after the empty state.
    expect(empty.compareDocumentPosition(advisory)).toBe(Node.DOCUMENT_POSITION_FOLLOWING)
  })
})

// The route owns the sort now (#1327): `routes/lambda/index.tsx` validates
// `sort` and hands `useSortSearchParam`'s pair down. What this page has to get
// right is the token it reports and the order it renders for one it is given —
// the `?sort=` contract, not the sorting itself, which the engine's own tests
// cover.
describe("FunctionList — sort bound to the route", () => {
  it("reports the column's own id rather than a slug of the header", async () => {
    const onSortChange = vi.fn()
    const { user } = renderWithData(<FunctionList sort={undefined} onSortChange={onSortChange} />, [
      [lambdaFunctionsQueryOptions().queryKey, functions],
    ])

    await user.click(screen.getByRole("button", { name: /Name/ }))
    expect(onSortChange).toHaveBeenCalledWith({ id: "name", desc: false })
  })

  it("renders the order the route asks for, before any click", () => {
    renderWithData(<FunctionList sort={{ id: "name", desc: true }} onSortChange={vi.fn()} />, [
      [lambdaFunctionsQueryOptions().queryKey, functions],
    ])
    expect(renderedNames()).toEqual(["gamma", "beta", "alpha"])
  })
})

// The debug badge reads one cached list query (docs/plans/compute-debugger.md
// § 7) rather than asking once per row, so the seed is the list.
describe("FunctionList — debugger badge", () => {
  function debugTarget(resource: string, state: string): DebuggerTarget {
    return {
      id: `lambda/${resource}`,
      service: "lambda",
      resource,
      container: "",
      enabled: true,
      reason: "",
      protocol: "inspector",
      protocolSource: "runtime",
      listen: { host: "127.0.0.1", port: 9229 },
      state,
      attachedSince: "",
      pausedSince: "",
      containerId: "",
      upstream: "",
      remoteRoot: "/var/task",
      localRoot: "",
      timeoutPolicy: "attached",
      setup: { flag: "", tagCli: "", tagCdk: "" },
      editors: [],
    }
  }

  it("marks only the rows that have a target, with the target's state", () => {
    renderWithData(<FunctionList />, [
      [lambdaFunctionsQueryOptions().queryKey, functions],
      [debuggerTargetsQueryOptions().queryKey, [debugTarget("beta", "attached")]],
    ])

    const rows = screen.getAllByRole("row").slice(1)
    const beta = rows.find((row) => within(row).getAllByRole("cell")[0].textContent === "beta")
    expect(within(beta!).getByTitle("Debugger: attached")).toHaveTextContent("attached")
    expect(screen.getAllByTitle(/^Debugger: /)).toHaveLength(1)
  })

  it("renders no badge at all when there are no targets", () => {
    renderWithData(<FunctionList />, [
      [lambdaFunctionsQueryOptions().queryKey, functions],
      [debuggerTargetsQueryOptions().queryKey, []],
    ])

    expect(screen.queryByTitle(/^Debugger: /)).not.toBeInTheDocument()
  })
})
