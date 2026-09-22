import { beforeEach, describe, expect, it } from "vitest"
import { createTestQueryClient, renderWithRouter, screen, within } from "@/test/render"
import { TooltipProvider } from "@/components/ui/tooltip"
import { FavouritesProvider } from "@/hooks/use-favourites"
import { ServiceIconColorProvider } from "@/hooks/use-service-icon-color"
import type { HealthResponse } from "@/types/common"
import { Dashboard } from "./dashboard"

const HEALTH: HealthResponse = {
  status: "ok",
  timestamp: "2026-07-26T00:00:00Z",
  version: "0.1.0-test",
  services: ["s3", "sqs", "dynamodb", "ecr", "sns", "lambda"],
  serviceTiers: {
    s3: "full",
    sqs: "full",
    dynamodb: "full",
    ecr: "partial",
    // Registered but unimplemented — not emulated, still reachable.
    sns: "stub",
    lambda: "unsupported",
  },
  // The server always sends goal tiers alongside current ones; the dashboard
  // reads only the current tier, so the values here do not matter.
  serviceGoalTiers: {},
  storage: { default: "memory" },
}

function DashboardOnly() {
  return (
    <TooltipProvider>
      <ServiceIconColorProvider>
        <FavouritesProvider>
          <Dashboard />
        </FavouritesProvider>
      </ServiceIconColorProvider>
    </TooltipProvider>
  )
}

function renderDashboard() {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(["health"], HEALTH)
  return renderWithRouter(DashboardOnly, { queryClient })
}

function findSection(name: string) {
  return screen.findByRole("region", { name })
}

describe("Dashboard", () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it("lists a fully emulated service in the fully-emulated section", async () => {
    renderDashboard()

    expect(within(await findSection("fully emulated")).getByText("S3")).toBeInTheDocument()
  })

  it("renders the services table once the list view is selected", async () => {
    const { user } = renderDashboard()

    await user.click(await screen.findByRole("button", { name: "List view" }))

    expect(screen.getByRole("table", { name: "Services" })).toBeInTheDocument()
  })

  // #1611: the Scope and Tier columns ran off the right of a card with
  // `overflow-hidden` and nothing scrolled to reach them, so at ~400px the data
  // was simply unreachable. The table now sits on the same focusable scroller
  // every ResourceTable list page uses.
  it("puts the services table on a focusable horizontal scroller", async () => {
    const { user } = renderDashboard()

    await user.click(await screen.findByRole("button", { name: "List view" }))

    const scroller = screen.getByRole("table", { name: "Services" }).parentElement
    expect(scroller).toHaveAttribute("tabindex", "0")
    expect(scroller).toHaveClass("overflow-auto")
  })

  it("groups a partially emulated service by its tier", async () => {
    renderDashboard()

    expect(within(await findSection("partially emulated")).getByText("ECR")).toBeInTheDocument()
    expect(within(await findSection("not emulated")).queryByText("ECR")).not.toBeInTheDocument()
  })

  // Sections track emulation tier and nothing else, so a fully emulated
  // service belongs in "fully emulated" regardless of anything else about it.
  it("files a fully emulated service by its tier alone", async () => {
    renderDashboard()

    expect(within(await findSection("fully emulated")).getByText("DynamoDB")).toBeInTheDocument()
    expect(
      within(await findSection("not emulated")).queryByText("DynamoDB"),
    ).not.toBeInTheDocument()
  })

  // Every service runs, so every tile is a link — there is no inert state.
  it("renders every tile as a link", async () => {
    renderDashboard()

    const section = await findSection("fully emulated")
    expect(within(section).getByText("DynamoDB").closest("a")).not.toBeNull()
    expect(within(section).getByText("S3").closest("a")).not.toBeNull()
  })

  it("files an unimplemented service under not emulated", async () => {
    renderDashboard()

    expect(within(await findSection("not emulated")).getByText("Lambda")).toBeInTheDocument()
  })

  it("keeps a not-emulated service reachable from both views", async () => {
    const { user } = renderDashboard()

    expect(
      within(await findSection("not emulated"))
        .getByText("SNS")
        .closest("a"),
    ).not.toBeNull()

    await user.click(await screen.findByRole("button", { name: "List view" }))

    const table = screen.getByRole("table", { name: "Services" })
    expect(within(table).getByText("SNS").closest("a")).not.toBeNull()
  })
})

describe("Dashboard > pinning", () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it("pins a service to the sidebar from its card", async () => {
    const { user } = renderDashboard()

    const section = await findSection("fully emulated")
    await user.click(within(section).getByRole("button", { name: "Pin S3 to sidebar" }))

    expect(JSON.parse(localStorage.getItem("overcast-favourites") ?? "[]")).toEqual(["/s3"])
  })

  // CloudWatch's card opens /cloudwatch/logs; the sidebar lists the /cloudwatch group.
  it("pins the sidebar service that owns a card's route", async () => {
    const { user } = renderDashboard()

    await user.click(await screen.findByRole("button", { name: "Pin CloudWatch to sidebar" }))

    expect(JSON.parse(localStorage.getItem("overcast-favourites") ?? "[]")).toEqual(["/cloudwatch"])
  })

  it("pins a partially emulated service from its card", async () => {
    const { user } = renderDashboard()

    const section = await findSection("partially emulated")
    await user.click(within(section).getByRole("button", { name: "Pin ECR to sidebar" }))

    expect(
      within(section).getByRole("button", { name: "Unpin ECR from sidebar" }),
    ).toBeInTheDocument()
  })

  it("keeps a pinned service's star visible without hovering the card", async () => {
    localStorage.setItem("overcast-favourites", JSON.stringify(["/s3"]))
    renderDashboard()

    const star = within(await findSection("fully emulated")).getByRole("button", {
      name: "Unpin S3 from sidebar",
    })
    expect(star).not.toHaveClass("opacity-0")
  })

  it("reveals an unpinned service's star only on hover or focus", async () => {
    renderDashboard()

    const star = within(await findSection("fully emulated")).getByRole("button", {
      name: "Pin S3 to sidebar",
    })
    expect(star).toHaveClass("opacity-0", "group-hover:opacity-100", "focus-visible:opacity-100")
  })

  it("keeps the pin out of the card's link", async () => {
    renderDashboard()

    const section = await findSection("fully emulated")
    const link = within(section).getByText("S3").closest("a")
    expect(link).not.toContainElement(
      within(section).getByRole("button", { name: "Pin S3 to sidebar" }),
    )
  })

  it("does not navigate when the pin is clicked", async () => {
    const { user, router } = renderDashboard()

    const section = await findSection("fully emulated")
    await user.click(within(section).getByRole("button", { name: "Pin S3 to sidebar" }))

    expect(router.state.location.pathname).toBe("/")
  })

  it("pins a service from the list view", async () => {
    const { user } = renderDashboard()

    await user.click(await screen.findByRole("button", { name: "List view" }))
    const table = screen.getByRole("table", { name: "Services" })
    await user.click(within(table).getByRole("button", { name: "Pin DynamoDB to sidebar" }))

    expect(
      within(table).getByRole("button", { name: "Unpin DynamoDB from sidebar" }),
    ).toBeInTheDocument()
  })
})
