import { beforeEach, describe, expect, it } from "vitest"
import { TooltipProvider } from "@/components/ui/tooltip"
import { FavouritesProvider } from "@/hooks/use-favourites"
import { ServiceIconColorProvider } from "@/hooks/use-service-icon-color"
import { createTestQueryClient, renderWithRouter, screen, waitFor, within } from "@/test/render"
import type { HealthResponse } from "@/types/common"
import { registerContributor } from "@/lib/search"
import { GlobalSearch } from "./global-search"

/**
 * S3 and ECR are enabled, DynamoDB is not. ECR deliberately has no tier entry
 * — a missing tier must never grey a card, only the enabled list may.
 */
const HEALTH: HealthResponse = {
  status: "ok",
  timestamp: "2026-07-27T00:00:00Z",
  version: "0.1.0-test",
  services: ["s3", "sqs", "ecr"],
  serviceTiers: { s3: "full", sqs: "full" },
  serviceGoalTiers: {},
  storage: { default: "memory" },
}

const FAVOURITES_KEY = "overcast-favourites"

function SearchDialog() {
  return (
    <TooltipProvider>
      <ServiceIconColorProvider>
        <FavouritesProvider>
          <GlobalSearch open onOpenChange={() => {}} />
        </FavouritesProvider>
      </ServiceIconColorProvider>
    </TooltipProvider>
  )
}

function renderSearch() {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(["health"], HEALTH)
  return renderWithRouter(SearchDialog, { queryClient })
}

function findCard(label: string) {
  return screen.findByRole("group", { name: label })
}

describe("GlobalSearch mega menu", () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it("navigates to a service when its card is clicked", async () => {
    const { user, router } = renderSearch()

    const card = await findCard("S3")
    await user.click(within(card).getByRole("button", { name: "S3" }))

    await waitFor(() => expect(router.state.location.pathname).toBe("/s3"))
  })

  it("shows a filled star on a favourited service without hovering the card", async () => {
    localStorage.setItem(FAVOURITES_KEY, JSON.stringify(["/s3"]))
    renderSearch()

    const star = within(await findCard("S3")).getByRole("button", {
      name: "Unpin S3 from sidebar",
    })
    expect(star.className).toContain("text-accent")
    expect(star.className).not.toMatch(/opacity-0|group-hover:opacity/)
  })

  it("shows an outline star on an unfavourited service without hovering the card", async () => {
    renderSearch()

    const star = within(await findCard("DynamoDB")).getByRole("button", {
      name: "Pin DynamoDB to sidebar",
    })
    expect(star.className).not.toMatch(/opacity-0|group-hover:opacity/)
  })

  it("renders the star as a sibling of the card's navigation target, not nested inside it", async () => {
    renderSearch()

    const card = await findCard("DynamoDB")
    expect(within(card).getByRole("button", { name: "DynamoDB" })).not.toContainElement(
      within(card).getByRole("button", { name: "Pin DynamoDB to sidebar" }),
    )
  })

  it("pins a service to the sidebar when its star is clicked", async () => {
    const { user } = renderSearch()

    const card = await findCard("DynamoDB")
    await user.click(within(card).getByRole("button", { name: "Pin DynamoDB to sidebar" }))

    expect(
      within(await findCard("DynamoDB")).getByRole("button", {
        name: "Unpin DynamoDB from sidebar",
      }),
    ).toBeInTheDocument()
  })

  it("shows an esc hint in the search input row", async () => {
    renderSearch()

    expect(await screen.findByText("esc")).toBeInTheDocument()
  })

  it("hides the clear button until a query is entered", async () => {
    renderSearch()

    await screen.findByPlaceholderText("Search services and resources…")
    expect(screen.queryByRole("button", { name: "Clear search" })).not.toBeInTheDocument()
  })

  // #2062: Athena, Glue, Firehose and OpenSearch were offered as greyed-out
  // "Unsupported" chips while the backend served all four. "amazon" matches
  // Redshift's and Athena's catalogue labels alike, so both would render in the
  // same pass.
  it("offers no unsupported chip for a service the backend emulates", async () => {
    const { user } = renderSearch()

    await user.type(await screen.findByPlaceholderText("Search services and resources…"), "amazon")

    expect(await screen.findByRole("button", { name: /Amazon Redshift/ })).toHaveTextContent(
      "Unsupported",
    )
    expect(screen.queryByRole("button", { name: /Amazon Athena/ })).not.toBeInTheDocument()
    // #2081
    expect(screen.queryByRole("button", { name: /Amazon Route 53/ })).not.toBeInTheDocument()
  })

  // #2081: the same for five more inert services. Their labels start "AWS", as
  // AWS Config's does, so one query would render them all alongside it.
  it("offers no unsupported chip for the other backend-emulated services", async () => {
    const { user } = renderSearch()

    await user.type(await screen.findByPlaceholderText("Search services and resources…"), "aws")

    expect(await screen.findByRole("button", { name: /AWS Config/ })).toHaveTextContent(
      "Unsupported",
    )
    for (const label of [
      "AWS Certificate Manager",
      "AWS Backup",
      "AWS CloudTrail",
      "AWS Organizations",
      "AWS Transfer Family",
    ]) {
      expect(screen.queryByRole("button", { name: new RegExp(label) })).not.toBeInTheDocument()
    }
  })

  // #2081: Bedrock is registered as a stub, so its chip says so.
  it("labels bedrock's chip as a stub", async () => {
    const { user } = renderSearch()

    await user.type(await screen.findByPlaceholderText("Search services and resources…"), "bedrock")

    expect(await screen.findByRole("button", { name: /Amazon Bedrock/ })).toHaveTextContent("Stub")
  })
})

describe("GlobalSearch results", () => {
  // #2085: a result can link to a tab and a filter, which only survive the
  // navigation as a search string, not as part of the path.
  registerContributor({
    id: "test:query-string-result",
    search: (query) =>
      Promise.resolve(
        query === "etl-workgroup"
          ? [
              {
                id: "test:etl",
                label: "etl-workgroup",
                service: "Athena",
                serviceKey: "/athena",
                type: "Workgroup",
                href: "/athena?tab=workgroups&q=etl",
              },
            ]
          : [],
      ),
  })

  it("navigates to a result's path with its query string as the search", async () => {
    const { user, router } = renderSearch()

    await user.type(
      await screen.findByPlaceholderText("Search services and resources…"),
      "etl-workgroup",
    )
    await user.click(await screen.findByRole("option", { name: /etl-workgroup/ }))

    await waitFor(() => expect(router.state.location.pathname).toBe("/athena"))
    expect(router.state.location.search).toEqual({ tab: "workgroups", q: "etl" })
  })
})
