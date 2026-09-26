import { renderWithRouter, screen } from "@/test/render"
import { AthenaQuery } from "./athena-query"

describe("AthenaQuery", () => {
  it("opens its SQL in the Athena editor", async () => {
    const { user } = renderWithRouter(
      () => (
        <AthenaQuery
          bucket="lake"
          objectKey="raw/orders.csv"
          format="csv"
          columns={[{ name: "id", numeric: true }]}
        />
      ),
      { path: "/" },
    )
    await user.click(await screen.findByRole("button", { name: "Query with Athena" }))
    const link = screen.getByRole("link", { name: /Open in the Athena editor/ })
    const url = new URL(link.getAttribute("href") ?? "", "http://console")
    expect(url.pathname).toBe("/athena")
    expect(url.searchParams.get("tab")).toBe("editor")
    expect(url.searchParams.get("sql")).toContain("CREATE EXTERNAL TABLE orders")
  })
})
