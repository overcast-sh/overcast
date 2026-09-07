import { http, HttpResponse } from "msw"
import { render, renderWithData, screen, within } from "@/test/render"
import { server } from "@/test/server"
import { debuggerTargetQueryOptions } from "@/features/debugger/data"
import type { DebuggerTarget } from "@/types"
import { DebugPanel, DebugTargetPanel } from "./debug-panel"
import { DebugStateBadge } from "./debug-state-badge"

/** A registered, enabled inspector target — the shape from plan § 6. */
function target(overrides: Partial<DebuggerTarget> = {}): DebuggerTarget {
  return {
    id: "lambda/my-fn",
    service: "lambda",
    resource: "my-fn",
    container: "",
    enabled: true,
    reason: "",
    protocol: "inspector",
    protocolSource: "runtime",
    listen: { host: "127.0.0.1", port: 9229 },
    state: "listening",
    attachedSince: "",
    pausedSince: "",
    containerId: "0a1b2c3d4e5f6a7b8c9d",
    upstream: "127.0.0.1:55012",
    remoteRoot: "/var/task",
    localRoot: "",
    timeoutPolicy: "attached",
    setup: {
      flag: "OVERCAST_DEBUGGER=true",
      tagCli:
        "aws lambda tag-resource --resource arn:aws:lambda:us-east-1:000000000000:function:my-fn --tags overcast:debug=true",
      tagCdk: 'cdk.Tags.of(fn).add("overcast:debug", "true")',
    },
    editors: [
      {
        id: "vscode",
        label: "VS Code",
        kind: "json",
        body: '{\n  "type": "node",\n  "port": 9229\n}',
        verified: true,
      },
      {
        id: "jetbrains",
        label: "JetBrains",
        kind: "steps",
        body: "1. Run → Edit Configurations…\n2. Host: 127.0.0.1   Port: 9229",
        verified: false,
      },
      {
        id: "cli",
        label: "Command line",
        kind: "shell",
        body: "node inspect 127.0.0.1:9229",
        verified: true,
      },
    ],
    ...overrides,
  }
}

/** Installs an async clipboard; must run after `render` (see copy-button.test.tsx). */
function secureContext() {
  const writeText = vi.fn<(text: string) => Promise<void>>().mockResolvedValue(undefined)
  Object.defineProperty(globalThis.navigator, "clipboard", {
    value: { writeText },
    configurable: true,
  })
  return writeText
}

function badge() {
  return screen.getByTitle(/^Debugger: /)
}

describe("DebugPanel > states", () => {
  it("inert: shows off, the reason, and the setup block instead of the target", () => {
    render(
      <DebugPanel
        target={target({
          enabled: false,
          reason: "not tagged",
          state: "inert",
          protocol: "",
          editors: [],
        })}
      />,
    )

    expect(badge()).toHaveTextContent("off")
    expect(screen.getByText("not tagged")).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Turn it on" })).toBeInTheDocument()
    expect(screen.queryByText("Listen")).not.toBeInTheDocument()
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument()
  })

  it("unbound: says the port is listening for a container", () => {
    render(<DebugPanel target={target({ state: "unbound", containerId: "", upstream: "" })} />)

    expect(badge()).toHaveTextContent("unbound")
    expect(
      screen.getByText(/Listening on 127\.0\.0\.1:9229 — no container is bound yet/),
    ).toBeInTheDocument()
  })

  it("listening: says no debugger is attached", () => {
    render(<DebugPanel target={target()} />)

    expect(badge()).toHaveTextContent("listening")
    expect(
      screen.getByText(/Listening on 127\.0\.0\.1:9229 — no debugger attached/),
    ).toBeInTheDocument()
  })

  it("attached: shows a relative attached-since time and the suspended clock", () => {
    const attachedSince = new Date(Date.now() - 65_000).toISOString()
    render(<DebugPanel target={target({ state: "attached", attachedSince })} />)

    expect(badge()).toHaveTextContent("attached")
    const line = screen.getByText(/Attached since/)
    expect(line).toHaveTextContent(/Attached since 1m ago\. The timeout clock is suspended\./)
    expect(within(line).getByText("1m ago")).toHaveAttribute("datetime", attachedSince)
  })

  it("paused: shows paused-since beside attached-since", () => {
    const attachedSince = new Date(Date.now() - 120_000).toISOString()
    const pausedSince = new Date(Date.now() - 5_000).toISOString()
    render(<DebugPanel target={target({ state: "paused", attachedSince, pausedSince })} />)

    expect(badge()).toHaveTextContent("paused")
    expect(screen.getByText(/Paused since/)).toHaveTextContent(
      "Paused since 5s ago (attached 2m ago).",
    )
  })

  it("error: shows the reason in the state line", () => {
    render(
      <DebugPanel
        target={target({
          state: "error",
          reason: "cannot listen on 127.0.0.1:9229: address in use",
        })}
      />,
    )

    expect(badge()).toHaveTextContent("error")
    expect(screen.getByText("cannot listen on 127.0.0.1:9229: address in use")).toBeInTheDocument()
  })
})

describe("DebugPanel > setup block", () => {
  it("offers the flag, the CLI tag and the CDK line, each with a copy button", async () => {
    const t = target({ enabled: false, reason: "not tagged", state: "inert", editors: [] })
    const { user } = render(<DebugPanel target={t} />)
    const writeText = secureContext()

    expect(screen.getByText(t.setup.flag)).toBeInTheDocument()
    expect(screen.getByText(t.setup.tagCli)).toBeInTheDocument()
    expect(screen.getByText(t.setup.tagCdk)).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Copy flag" }))
    expect(writeText).toHaveBeenLastCalledWith(t.setup.flag)
    await user.click(screen.getByRole("button", { name: "Copy CLI tag" }))
    expect(writeText).toHaveBeenLastCalledWith(t.setup.tagCli)
    await user.click(screen.getByRole("button", { name: "Copy CDK line" }))
    expect(writeText).toHaveBeenLastCalledWith(t.setup.tagCdk)
  })
})

describe("DebugPanel > resolved target", () => {
  it("shows the protocol with its source, the address with copy, and the short container id", async () => {
    const { user } = render(<DebugPanel target={target()} />)
    const writeText = secureContext()

    expect(screen.getByText("inspector")).toBeInTheDocument()
    expect(screen.getByText("runtime")).toBeInTheDocument()
    expect(screen.getByText("0a1b2c3d4e5f")).toBeInTheDocument()
    expect(screen.getByText("/var/task")).toBeInTheDocument()
    expect(screen.getByText("attached")).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Copy listen" }))
    expect(writeText).toHaveBeenLastCalledWith("127.0.0.1:9229")
    await user.click(screen.getByRole("button", { name: "Copy container" }))
    expect(writeText).toHaveBeenLastCalledWith("0a1b2c3d4e5f6a7b8c9d")
  })

  it("names the tags that fill in an unknown local root", () => {
    render(<DebugPanel target={target({ localRoot: "" })} />)

    expect(screen.getByText("overcast:source-path")).toBeInTheDocument()
    expect(screen.getByText("overcast:hot-reload-path")).toBeInTheDocument()
  })

  it("shows the local root as the user wrote it", () => {
    render(<DebugPanel target={target({ localRoot: "./src" })} />)

    expect(screen.getByText("./src")).toBeInTheDocument()
    expect(screen.queryByText("overcast:source-path")).not.toBeInTheDocument()
  })
})

describe("DebugPanel > editors", () => {
  it("renders one tab per editor and switches between kinds", async () => {
    const { user } = render(<DebugPanel target={target()} />)

    const tabs = screen.getByRole("tablist", { name: "Editor" })
    expect(
      within(tabs)
        .getAllByRole("tab")
        .map((t) => t.textContent),
    ).toEqual(["VS Code", "JetBrains", "Command line"])
    // json: a code block, verified.
    expect(screen.getByText(/"type": "node"/)).toBeInTheDocument()
    expect(screen.queryByText(/Unverified/)).not.toBeInTheDocument()

    // steps: numbered items with the server's numbering stripped, unverified note.
    await user.click(within(tabs).getByRole("tab", { name: "JetBrains" }))
    const items = screen.getAllByRole("listitem").map((li) => li.textContent)
    expect(items).toEqual(["Run → Edit Configurations…", "Host: 127.0.0.1   Port: 9229"])
    expect(screen.getByText(/Unverified/)).toBeInTheDocument()

    // shell: a code block.
    await user.click(within(tabs).getByRole("tab", { name: "Command line" }))
    expect(screen.getByText("node inspect 127.0.0.1:9229")).toBeInTheDocument()
  })

  it("copies the selected editor's body verbatim", async () => {
    const t = target()
    const { user } = render(<DebugPanel target={t} />)
    const writeText = secureContext()

    await user.click(screen.getByRole("button", { name: "Copy VS Code configuration" }))
    expect(writeText).toHaveBeenCalledWith(t.editors[0].body)
  })

  it("renders no tab strip when the server sends no editors", () => {
    render(<DebugPanel target={target({ editors: [] })} />)

    expect(screen.queryByRole("tablist")).not.toBeInTheDocument()
  })
})

describe("DebugPanel > container replaced", () => {
  const attachedSince = "2026-09-07T10:00:00Z"

  it("says so when the container changes under an attached editor", () => {
    const { rerender } = render(
      <DebugPanel
        target={target({ state: "attached", attachedSince, containerId: "aaaaaaaaaaaa1111" })}
      />,
    )
    expect(screen.queryByRole("status")).not.toBeInTheDocument()

    rerender(
      <DebugPanel
        target={target({ state: "attached", attachedSince, containerId: "bbbbbbbbbbbb2222" })}
      />,
    )
    expect(screen.getByRole("status")).toHaveTextContent(
      "Container replaced — your editor should reconnect. (was aaaaaaaaaaaa)",
    )
  })

  it("keeps the notice while the editor has yet to reconnect, then clears it", () => {
    const { rerender } = render(
      <DebugPanel
        target={target({ state: "attached", attachedSince, containerId: "aaaaaaaaaaaa1111" })}
      />,
    )

    // The old splice dropped with the old container; the new one is bound, nobody attached.
    rerender(
      <DebugPanel
        target={target({ state: "listening", attachedSince: "", containerId: "bbbbbbbbbbbb2222" })}
      />,
    )
    expect(screen.getByRole("status")).toHaveTextContent(/Container replaced/)

    // A new attach session on the new container: the editor is back.
    rerender(
      <DebugPanel
        target={target({
          state: "attached",
          attachedSince: "2026-09-07T10:00:30Z",
          containerId: "bbbbbbbbbbbb2222",
        })}
      />,
    )
    expect(screen.queryByRole("status")).not.toBeInTheDocument()
  })

  it("says nothing when the container changes with no editor attached", () => {
    const { rerender } = render(
      <DebugPanel target={target({ state: "listening", containerId: "aaaaaaaaaaaa1111" })} />,
    )
    rerender(
      <DebugPanel target={target({ state: "listening", containerId: "bbbbbbbbbbbb2222" })} />,
    )

    expect(screen.queryByRole("status")).not.toBeInTheDocument()
  })
})

describe("DebugTargetPanel", () => {
  it("renders the descriptor the server answers with", () => {
    renderWithData(<DebugTargetPanel service="lambda" resource="my-fn" />, [
      [debuggerTargetQueryOptions("lambda", "my-fn").queryKey, target()],
    ])

    expect(badge()).toHaveTextContent("listening")
  })

  it("renders a 404 as off rather than as an error", async () => {
    // The default handler in src/test/handlers.ts answers 404 — the case of a
    // service with no describer yet, or a resource that is gone.
    render(<DebugTargetPanel service="ecs" resource="task-1" container="app" />)

    expect(await screen.findByTitle("Debugger: off")).toBeInTheDocument()
    expect(screen.getByText("No debug target for this container.")).toBeInTheDocument()
    expect(screen.queryByText(/unavailable/)).not.toBeInTheDocument()
  })

  it("reports any other failure without throwing", async () => {
    server.use(
      http.get("/api/debugger/targets/:service/:resource", () =>
        HttpResponse.json({ error: "boom" }, { status: 500 }),
      ),
    )
    render(<DebugTargetPanel service="lambda" resource="my-fn" />)

    expect(await screen.findByText("Debugger state unavailable — boom")).toBeInTheDocument()
  })
})

describe("DebugStateBadge", () => {
  it.each([
    ["inert", "off"],
    ["unbound", "unbound"],
    ["listening", "listening"],
    ["attached", "attached"],
    ["paused", "paused"],
    ["error", "error"],
  ])("labels %s as %s and names the debugger for a screen reader", (state, label) => {
    render(<DebugStateBadge state={state} />)

    const pill = screen.getByTitle(`Debugger: ${label}`)
    expect(pill).toHaveTextContent(`Debugger ${label}`)
    expect(pill).toHaveAttribute("data-state", state)
  })
})
