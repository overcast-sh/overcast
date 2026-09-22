import { beforeEach, describe, expect, it, vi } from "vitest"
import type React from "react"
import { InboxPage } from "@/features/mail/mail-page"
import { inboxMessagesQueryOptions } from "@/features/mail/data"
import { ToastContextProvider } from "@/components/ui/toast"
import { TooltipProvider } from "@/components/ui/tooltip"
import { FavouritesProvider } from "@/hooks/use-favourites"
import { ServiceIconColorProvider } from "@/hooks/use-service-icon-color"
import { createTestQueryClient, renderWithRouter, screen, seedServerInfo } from "@/test/render"
import { Sidebar } from "./sidebar"
import {
  NARROW_SIDEBAR_QUERY,
  SIDEBAR_COLLAPSED_NARROW_STORAGE_KEY,
  SIDEBAR_COLLAPSED_WIDE_STORAGE_KEY,
  SidebarCollapseProvider,
} from "../use-sidebar-collapse"
import type { CapturedMessage, HealthResponse } from "@/types"

const messages: CapturedMessage[] = [
  {
    id: "msg-1",
    kind: "email",
    from: "sender@example.com",
    to: ["reader@example.com"],
    subject: "First unread",
    textBody: "Body one",
    receivedAt: "2026-05-30T12:00:00Z",
  },
  {
    id: "msg-2",
    kind: "sms",
    from: "+15550000000",
    to: ["+15551112222"],
    textBody: "Second unread",
    receivedAt: "2026-05-30T12:01:00Z",
  },
]

function renderScreen(
  component: React.FC,
  { debug = false, services, route }: { debug?: boolean; services?: string[]; route?: string } = {},
) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(inboxMessagesQueryOptions().queryKey, messages)
  seedServerInfo(queryClient, { debug })
  if (services) {
    queryClient.setQueryData(["health"], {
      status: "ok",
      timestamp: "2026-07-29T00:00:00Z",
      version: "0.1.0-test",
      services,
      serviceTiers: {},
      serviceGoalTiers: {},
      storage: { default: "memory" },
    } satisfies HealthResponse)
  }

  return renderWithRouter(component, {
    queryClient,
    ...(route ? { path: route, initialEntry: route } : {}),
  })
}

function mockNarrowViewport(isNarrow: boolean) {
  return vi.spyOn(window, "matchMedia").mockImplementation((query: string) => ({
    matches: isNarrow && query === NARROW_SIDEBAR_QUERY,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }))
}

function SidebarOnly() {
  return (
    <TooltipProvider>
      <ServiceIconColorProvider>
        <FavouritesProvider>
          <SidebarCollapseProvider>
            <Sidebar />
          </SidebarCollapseProvider>
        </FavouritesProvider>
      </ServiceIconColorProvider>
    </TooltipProvider>
  )
}

function SidebarWithInbox() {
  return (
    <ToastContextProvider>
      <TooltipProvider>
        <ServiceIconColorProvider>
          <FavouritesProvider>
            <SidebarCollapseProvider>
              <div className="flex">
                <Sidebar />
                <InboxPage />
              </div>
            </SidebarCollapseProvider>
          </FavouritesProvider>
        </ServiceIconColorProvider>
      </TooltipProvider>
    </ToastContextProvider>
  )
}

function InboxOnly() {
  return (
    <ToastContextProvider>
      <InboxPage />
    </ToastContextProvider>
  )
}

describe("Sidebar inbox badge", () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it("shows the unread inbox count", async () => {
    renderScreen(SidebarOnly)

    expect(await screen.findByLabelText("2 unread inbox messages")).toHaveTextContent("2")
  })

  it("updates when an inbox message is read", async () => {
    const { user } = renderScreen(SidebarWithInbox)

    await user.click(await screen.findByRole("button", { name: /First unread/ }))

    expect(await screen.findByLabelText("1 unread inbox message")).toHaveTextContent("1")
  })

  it("filters the inbox to unread messages", async () => {
    const { user } = renderScreen(InboxOnly)

    await user.click(await screen.findByRole("button", { name: /First unread/ }))
    await user.click(screen.getByRole("button", { name: /Second unread/ }))
    await user.click(screen.getByRole("button", { name: "Unread" }))

    expect(screen.queryByRole("button", { name: /First unread/ })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Second unread/ })).toBeInTheDocument()
  })

  it("keeps the selected message visible while filtering unread messages", async () => {
    const { user } = renderScreen(InboxOnly)

    await user.click(await screen.findByRole("button", { name: "Unread 2" }))
    await user.click(screen.getByRole("button", { name: /First unread/ }))

    expect(screen.getByRole("button", { name: /First unread/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Second unread/ })).toBeInTheDocument()
  })
})

describe("Sidebar debug navigation", () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it("hides the debug link when debug mode is disabled", async () => {
    renderScreen(SidebarOnly)

    await screen.findByRole("link", { name: "Dashboard" })
    expect(screen.queryByRole("link", { name: "Debug" })).not.toBeInTheDocument()
  })

  it("shows the debug link when debug mode is enabled", async () => {
    renderScreen(SidebarOnly, { debug: true })

    expect(await screen.findByRole("link", { name: "Debug" })).toBeInTheDocument()
  })
})

describe("Sidebar pins", () => {
  beforeEach(() => {
    localStorage.clear()
  })

  // Availability is gone: every service always runs, so a pinned service is
  // simply a link. Nothing is dimmed and nothing is inferred from /_overcast/health.
  it("renders the pinned services as plain links", async () => {
    localStorage.setItem("overcast-favourites", JSON.stringify(["/s3", "/dynamodb"]))

    renderScreen(SidebarOnly, {})

    expect(await screen.findByRole("link", { name: "S3" })).not.toHaveClass("opacity-50")
    expect(screen.getByRole("link", { name: "DynamoDB" })).not.toHaveClass("opacity-50")
  })

  it("unpins a service from its own sidebar row", async () => {
    localStorage.setItem("overcast-favourites", JSON.stringify(["/s3", "/dynamodb"]))
    const { user } = renderScreen(SidebarOnly, {})

    await user.click(await screen.findByRole("button", { name: "Unpin S3 from sidebar" }))

    expect(screen.queryByRole("link", { name: "S3" })).not.toBeInTheDocument()
  })

  it("pins the service being viewed from its sidebar row", async () => {
    const { user } = renderScreen(SidebarOnly, { route: "/s3" })

    await user.click(await screen.findByRole("button", { name: "Pin S3 to sidebar" }))

    expect(screen.getByRole("button", { name: "Unpin S3 from sidebar" })).toBeInTheDocument()
  })

  // The viewed service's row sits above the pins, so pinning it must not move it.
  it("pins the service being viewed above the existing pins", async () => {
    localStorage.setItem("overcast-favourites", JSON.stringify(["/dynamodb"]))
    const { user } = renderScreen(SidebarOnly, { route: "/s3" })

    await user.click(await screen.findByRole("button", { name: "Pin S3 to sidebar" }))

    expect(JSON.parse(localStorage.getItem("overcast-favourites") ?? "[]")).toEqual([
      "/s3",
      "/dynamodb",
    ])
  })

  it("keeps the pin toggle out of the pinned row's link", async () => {
    localStorage.setItem("overcast-favourites", JSON.stringify(["/s3"]))
    renderScreen(SidebarOnly, {})

    expect(await screen.findByRole("link", { name: "S3" })).not.toContainElement(
      screen.getByRole("button", { name: "Unpin S3 from sidebar" }),
    )
  })

  it("offers no pin toggle on the collapsed rail", async () => {
    localStorage.setItem("overcast-favourites", JSON.stringify(["/s3"]))
    localStorage.setItem(SIDEBAR_COLLAPSED_WIDE_STORAGE_KEY, "true")
    renderScreen(SidebarOnly, {})

    await screen.findByRole("link", { name: "S3" })
    expect(screen.queryByRole("button", { name: "Unpin S3 from sidebar" })).not.toBeInTheDocument()
  })

  it("pins nothing by default", async () => {
    renderScreen(SidebarOnly, {})

    await screen.findByRole("link", { name: "Dashboard" })
    expect(screen.queryByRole("link", { name: "SQS" })).not.toBeInTheDocument()
  })
})

describe("Sidebar collapse state", () => {
  beforeEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it("starts collapsed in narrow viewports when no preference is saved", async () => {
    mockNarrowViewport(true)

    renderScreen(SidebarOnly)

    expect(await screen.findByRole("button", { name: "Expand sidebar" })).toBeInTheDocument()
  })

  it("persists the collapse state across refreshes", async () => {
    const { user, unmount } = renderScreen(SidebarOnly)

    await user.click(await screen.findByRole("button", { name: "Collapse sidebar" }))
    expect(localStorage.getItem(SIDEBAR_COLLAPSED_WIDE_STORAGE_KEY)).toBe("true")
    expect(localStorage.getItem(SIDEBAR_COLLAPSED_NARROW_STORAGE_KEY)).toBeNull()

    unmount()
    renderScreen(SidebarOnly)

    expect(await screen.findByRole("button", { name: "Expand sidebar" })).toBeInTheDocument()
  })

  it("stores narrow viewport preferences separately from wide preferences", async () => {
    const matchMedia = mockNarrowViewport(true)
    const { user, unmount } = renderScreen(SidebarOnly)

    await user.click(await screen.findByRole("button", { name: "Expand sidebar" }))
    expect(localStorage.getItem(SIDEBAR_COLLAPSED_NARROW_STORAGE_KEY)).toBe("false")
    expect(localStorage.getItem(SIDEBAR_COLLAPSED_WIDE_STORAGE_KEY)).toBeNull()

    unmount()
    matchMedia.mockRestore()
    mockNarrowViewport(false)
    renderScreen(SidebarOnly)

    expect(await screen.findByRole("button", { name: "Collapse sidebar" })).toBeInTheDocument()
  })

  it("shows immediate UI tooltips for collapsed icon links", async () => {
    const { user } = renderScreen(SidebarOnly)

    await user.click(await screen.findByRole("button", { name: "Collapse sidebar" }))
    const dashboardLink = await screen.findByRole("link", { name: "Dashboard" })
    expect(dashboardLink).not.toHaveAttribute("title")

    await user.hover(dashboardLink)

    expect(await screen.findAllByText("Dashboard")).not.toHaveLength(0)
  })
})
