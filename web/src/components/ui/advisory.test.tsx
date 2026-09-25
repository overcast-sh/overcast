import { renderWithRouter, screen } from "@/test/render"
import { Advisory } from "./advisory"

describe("Advisory", () => {
  it("links to the docs page and section it names", async () => {
    renderWithRouter(
      () => (
        <Advisory
          title="The query engine is off"
          docsPath="services/athena/limitations.md#the-engine"
        >
          Queries succeed with empty results.
        </Advisory>
      ),
      { path: "/" },
    )
    const link = await screen.findByRole("link", { name: /Read more/ })
    expect(link).toHaveAttribute("href", "/docs?path=services%2Fathena%2Flimitations.md#the-engine")
  })

  it("is a note with its title", async () => {
    renderWithRouter(() => <Advisory title="The query engine is off" />, { path: "/" })
    expect(await screen.findByRole("note")).toHaveTextContent("The query engine is off")
  })
})
