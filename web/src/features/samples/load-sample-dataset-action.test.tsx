import { http, HttpResponse } from "msw"
import { describe, expect, it, vi } from "vitest"
import { render, screen, userEvent } from "@/test/render"
import { server } from "@/test/server"
import { canLoadSampleDataset, describeSampleDataset } from "./data"
import { LoadSampleDatasetAction } from "./load-sample-dataset-action"
import { sampleReport, serveSamples } from "./testing/serve-samples"

describe("LoadSampleDatasetAction", () => {
  it("loads the analytics dataset and hands over what it loaded", async () => {
    // Given: an emulator that loads the dataset
    const report = sampleReport()
    const posted = serveSamples({ report })
    const onLoaded = vi.fn()
    render(<LoadSampleDatasetAction onLoaded={onLoaded} />)

    // When: the action is clicked
    await userEvent.click(await screen.findByRole("button", { name: "Load sample dataset" }))

    // Then: the dataset is loaded, and the toast says what landed
    expect(await screen.findByText("Sample dataset loaded")).toBeInTheDocument()
    expect(posted()).toBe(true)
    expect(onLoaded).toHaveBeenCalledWith(report)
    expect(screen.getByText(describeSampleDataset(report))).toBeInTheDocument()
  })

  it("says why a load failed", async () => {
    serveSamples({ error: "create bucket: AccessDenied", status: 500 })
    render(<LoadSampleDatasetAction />)

    await userEvent.click(await screen.findByRole("button", { name: "Load sample dataset" }))

    expect(await screen.findByText("Could not load the sample dataset")).toBeInTheDocument()
    expect(screen.getByText("create bucket: AccessDenied")).toBeInTheDocument()
  })

  it("is absent from an emulator without Athena", async () => {
    server.use(
      http.get("/api/health", () => HttpResponse.json({ status: "ok", services: ["glue", "s3"] })),
    )
    const { queryClient } = render(<LoadSampleDatasetAction />)

    await vi.waitFor(() => expect(queryClient.getQueryData(["health"])).toBeDefined())
    expect(screen.queryByRole("button", { name: "Load sample dataset" })).not.toBeInTheDocument()
  })
})

describe("describeSampleDataset", () => {
  it("names the database and tables, then any Iceberg caveat", () => {
    const report = sampleReport()
    expect(describeSampleDataset(report)).toBe(
      "sample_analytics: orders_csv, orders_parquet. No Iceberg copy: the Athena engine is off.",
    )
    expect(describeSampleDataset({ ...report, icebergSkipped: undefined })).toBe(
      "sample_analytics: orders_csv, orders_parquet.",
    )
  })
})

describe("canLoadSampleDataset", () => {
  it("needs Athena, Glue and S3", () => {
    expect(canLoadSampleDataset(["s3", "glue", "athena", "sqs"])).toBe(true)
    expect(canLoadSampleDataset(["s3", "glue"])).toBe(false)
    expect(canLoadSampleDataset(undefined)).toBe(false)
  })
})
