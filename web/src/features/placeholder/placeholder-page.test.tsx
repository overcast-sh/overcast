import { describe, expect, it } from "vitest"
import { TooltipProvider } from "@/components/ui/tooltip"
import { CATALOG_BY_ID } from "@/lib/unsupported-services"
import { render, screen } from "@/test/render"
import { PlaceholderPage } from "./placeholder-page"

function renderEntry(id: string) {
  const entry = CATALOG_BY_ID[id]
  if (!entry) throw new Error(`no catalogue entry ${id}`)
  return render(
    <TooltipProvider>
      <PlaceholderPage
        serviceName={entry.label}
        description={entry.description}
        docsUrl={entry.awsDocsUrl}
        tier={entry.tier}
        goalTier={entry.goalTier}
        reason={entry.reason}
      />
    </TooltipProvider>,
  )
}

describe("PlaceholderPage", () => {
  // #2081: the stub copy said every operation returns 501, which was false for
  // Shield and Bedrock alike. Each answers a different subset, so the page
  // shows the entry's reason, which names that subset.
  it("says what a stub answers instead of claiming every operation returns 501", () => {
    renderEntry("bedrock")

    expect(screen.getByText("Stub — most operations return 501")).toBeInTheDocument()
    expect(screen.getByText(/Converse and InvokeModel return a canned response/)).toBeInTheDocument()
    expect(screen.queryByText(/every\s+operation returns/)).not.toBeInTheDocument()
  })

  it("explains why an unsupported service is not emulated", () => {
    renderEntry("redshift")

    expect(screen.getByText("Not supported in Overcast")).toBeInTheDocument()
    expect(screen.getByText(CATALOG_BY_ID.redshift?.reason ?? "")).toBeInTheDocument()
  })
})
