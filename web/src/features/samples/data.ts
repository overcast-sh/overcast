import { mutationOptions } from "@tanstack/react-query"
import { athenaKeys } from "@/features/athena/data"
import { glueKeys } from "@/features/glue/data"
import { s3Keys } from "@/features/s3/data"
import { samples } from "@/services/api"
import type { SampleDataset } from "@/services/api/samples"
import type { SampleDatasetReport } from "@/types"

/** The services a load goes through; the endpoint refuses without any of them. */
export const SAMPLE_DATASET_SERVICES = ["athena", "glue", "s3"] as const

/** Whether an emulator enabling `services` can load a sample dataset. */
export function canLoadSampleDataset(services: readonly string[] | undefined): boolean {
  return SAMPLE_DATASET_SERVICES.every((s) => services?.includes(s))
}

/** Loads a sample dataset through POST /_overcast/samples/{dataset}. */
export function loadSampleDatasetMutationOptions() {
  return mutationOptions({
    mutationFn: (dataset: SampleDataset) => samples.load(dataset),
  })
}

/**
 * What a load changes: a bucket and its objects, a Glue database and its
 * tables, and the Athena queries that created them.
 */
export function sampleDatasetKeys() {
  return [s3Keys.all(), glueKeys.all(), athenaKeys.all()]
}

/** The toast line for a load: the database, its tables and any Iceberg caveat. */
export function describeSampleDataset(report: SampleDatasetReport): string {
  const tables = report.tables.map((t) => t.name).join(", ")
  const loaded = `${report.database}: ${tables}.`
  return report.icebergSkipped ? `${loaded} No Iceberg copy: ${report.icebergSkipped}` : loaded
}
