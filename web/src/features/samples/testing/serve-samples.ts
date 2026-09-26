import { http, HttpResponse } from "msw"
import { server } from "@/test/server"
import type { SampleDatasetReport } from "@/types"
import { SAMPLE_DATASET_SERVICES } from "../data"

const SAMPLES_URL = "http://localhost:4566/_overcast/samples/analytics"

/**
 * An emulator that can load the sample dataset: health lists the services
 * the load needs, and POST /_overcast/samples/analytics answers `report`, or
 * `error` with `status` when given. Returns whether the load was asked for.
 */
export function serveSamples(
  answer: { report: SampleDatasetReport } | { error: string; status: number },
): () => boolean {
  let posted = false
  server.use(
    http.get("/api/health", () =>
      HttpResponse.json({ status: "ok", services: [...SAMPLE_DATASET_SERVICES] }),
    ),
    http.post(SAMPLES_URL, () => {
      posted = true
      return "report" in answer
        ? HttpResponse.json(answer.report)
        : HttpResponse.json({ error: answer.error }, { status: answer.status })
    }),
  )
  return () => posted
}

/** The report of a load that made `database` and no Iceberg copy. */
export function sampleReport(database = "sample_analytics"): SampleDatasetReport {
  return {
    dataset: "analytics",
    bucket: "overcast-sample-analytics",
    database,
    tables: [
      { name: "orders_csv", format: "CSV", location: "s3://b/csv/", partitions: 3, rows: 300 },
      { name: "orders_parquet", format: "PARQUET", location: "s3://b/parquet/", rows: 300 },
    ],
    icebergSkipped: "the Athena engine is off.",
  }
}
