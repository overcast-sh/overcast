import { renderWithRouter, screen } from "@/test/render"
import { S3UriLink } from "./s3-uri-link"

describe("S3UriLink", () => {
  it("links an object URI to the S3 browser at that object", async () => {
    renderWithRouter(() => <S3UriLink uri="s3://results/athena/q-1.csv" />, { path: "/" })
    expect(
      await screen.findByRole("link", { name: "s3://results/athena/q-1.csv" }),
    ).toHaveAttribute("href", "/s3/results/objects/athena/q-1.csv")
  })

  it("links a folder URI to that folder", async () => {
    renderWithRouter(() => <S3UriLink uri="s3://lake/raw/" />, { path: "/" })
    expect(await screen.findByRole("link")).toHaveAttribute("href", "/s3/lake/objects/raw/")
  })

  it("renders anything else as plain text", async () => {
    renderWithRouter(() => <S3UriLink uri="https://example.com/a" />, { path: "/" })
    expect(await screen.findByText("https://example.com/a")).toBeInTheDocument()
    expect(screen.queryByRole("link")).not.toBeInTheDocument()
  })
})
