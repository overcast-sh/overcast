import type { SampleDatasetReport } from "@/types"
import { overcastFetch } from "./base"

/** A dataset POST /_overcast/samples/{dataset} can load. */
export type SampleDataset = "analytics"

/**
 * The emulator's sample datasets, loaded by the same loader as `overcast
 * samples load`, through the AWS API.
 */
export const samples = {
  /** Loads a dataset. Loading again changes nothing, so it is safe to retry. */
  load: (dataset: SampleDataset): Promise<SampleDatasetReport> =>
    overcastFetch<SampleDatasetReport>(`/_overcast/samples/${dataset}`, { method: "POST" }),
}
