/**
 * LoadSampleDatasetAction — the *Load sample dataset* button for data-lake
 * empty states (Glue, the Athena rail). It loads the analytics dataset: a
 * bucket of Hive-partitioned CSV and Parquet, a Glue database with a table
 * over each, and an Iceberg copy when the Athena engine is running — the
 * same load `overcast samples load analytics` runs, and as safe to repeat.
 * It renders nothing on an emulator without Athena, Glue or S3 enabled.
 */
import { useQuery } from "@tanstack/react-query"
import { DatabaseZap } from "lucide-react"
import { Button } from "@/components/ui/button"
import { healthQueryOptions } from "@/hooks/use-health"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import type { SampleDatasetReport } from "@/types"
import {
  canLoadSampleDataset,
  describeSampleDataset,
  loadSampleDatasetMutationOptions,
  sampleDatasetKeys,
} from "./data"

interface LoadSampleDatasetActionProps {
  /** Runs once the dataset is loaded — to open its database, say. */
  onLoaded?: (report: SampleDatasetReport) => void
  className?: string
}

export function LoadSampleDatasetAction({ onLoaded, className }: LoadSampleDatasetActionProps) {
  const { data: health } = useQuery(healthQueryOptions)
  const load = useResourceMutation({
    options: loadSampleDatasetMutationOptions(),
    invalidateKeys: sampleDatasetKeys(),
    successTitle: "Sample dataset loaded",
    successDescription: (_dataset, report) => describeSampleDataset(report),
    errorTitle: "Could not load the sample dataset",
    onSuccess: (report) => onLoaded?.(report),
  })
  if (!canLoadSampleDataset(health?.services)) return null
  return (
    <Button
      size="sm"
      variant="outline"
      className={className}
      busy={load.isPending}
      busyLabel="Loading sample dataset"
      onClick={() => load.mutate("analytics")}
    >
      <DatabaseZap className="h-3.5 w-3.5" />
      Load sample dataset
    </Button>
  )
}
