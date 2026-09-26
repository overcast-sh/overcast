import { describe, expect, it } from "vitest"
import { ServiceIconColorProvider } from "@/hooks/use-service-icon-color"
import { renderWithRouter, screen } from "@/test/render"
import { ConsolePendingPage, type ConsolePendingPageProps } from "./console-pending-page"

/** The page as the app shell hosts it, inside the icon-colour setting. */
function renderPage(props: ConsolePendingPageProps) {
  return renderWithRouter(() => (
    <ServiceIconColorProvider>
      <ConsolePendingPage {...props} />
    </ServiceIconColorProvider>
  ))
}

describe("ConsolePendingPage > a service home", () => {
  it("is titled with the service's name", async () => {
    renderPage({ service: "glue" })

    expect(await screen.findByRole("heading", { name: "Glue" })).toBeInTheDocument()
  })

  it("says the service is emulated before saying its page is not built", async () => {
    renderPage({ service: "athena" })

    expect(await screen.findByText("No console page yet")).toBeInTheDocument()
    expect(screen.getByText(/Athena is emulated and answers the AWS API now/)).toBeInTheDocument()
  })

  it("offers the service's docs from the header and the empty state", async () => {
    renderPage({ service: "glue" })

    expect(await screen.findByRole("button", { name: "Docs" })).toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Read the Glue docs" }),
    ).toBeInTheDocument()
  })
})

describe("ConsolePendingPage > a resource an ArnLink or search result named", () => {
  function renderTable() {
    return renderPage({
      service: "glue",
      resource: { name: "orders", kind: "Table in database sales" },
    })
  }

  it("is titled with the resource's name", async () => {
    renderTable()

    expect(await screen.findByRole("heading", { name: "orders" })).toBeInTheDocument()
  })

  it("says what the resource is and where", async () => {
    renderTable()

    expect(await screen.findByText("Table in database sales")).toBeInTheDocument()
  })

  it("lets the name be copied", async () => {
    renderTable()

    expect(await screen.findByRole("button", { name: "Copy name" })).toBeInTheDocument()
  })
})
