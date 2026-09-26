import { useQuery } from "@tanstack/react-query"
import { Wrench } from "lucide-react"
import { Advisory } from "@/components/ui/advisory"
import { Badge } from "@/components/ui/badge"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import { QueryListState } from "@/components/ui/primitives"
import { toTitleCase } from "@/lib/format"
import { isRecord } from "@/lib/utils"
import { maintenanceQueryOptions, type ConfigTarget } from "../data"

/** Each maintenance type as the console names it. */
const TYPE_LABELS: Record<string, string> = {
  icebergCompaction: "Compaction",
  icebergSnapshotManagement: "Snapshot management",
  icebergUnreferencedFileRemoval: "Unreferenced file removal",
}

/**
 * A setting's value flattened to `name → value` pairs. The API nests each
 * type's settings under the type's own name
 * (`settings.icebergCompaction.targetFileSizeMB`); the extra level says
 * nothing a reader needs.
 */
function settingPairs(settings: unknown): [string, string][] {
  if (!isRecord(settings)) return []
  return Object.values(settings).flatMap((inner) =>
    isRecord(inner) ? Object.entries(inner).map(([k, v]): [string, string] => [k, String(v)]) : [],
  )
}

/** The stored maintenance configuration, with AWS's defaults for anything not set. */
export function MaintenanceTab({ target }: { target: ConfigTarget }) {
  const { data, isLoading, error } = useQuery(maintenanceQueryOptions(target))
  const types = Object.entries(data ?? {})
  return (
    <div className="flex flex-col gap-3">
      <Advisory
        title="Nothing is compacted or expired"
        docsPath="services/s3tables/limitations.md#stored-never-run"
      >
        Overcast stores this configuration and reports AWS's defaults, but runs no maintenance jobs:
        small files are never compacted and old snapshots stay until you expire them yourself.
      </Advisory>
      <QueryListState
        isLoading={isLoading}
        error={error}
        isEmpty={types.length === 0}
        loadingNoun="maintenance settings"
        errorTitle="Could not read the maintenance configuration"
        emptyIcon={<Wrench className="size-8" />}
        emptyTitle="No maintenance configuration"
      />
      <div className="grid gap-3 lg:grid-cols-2">
        {types.map(([type, value]) => (
          <DefinitionCard
            key={type}
            title={TYPE_LABELS[type] ?? toTitleCase(type)}
            actions={
              <Badge variant={value?.status === "enabled" ? "success" : "default"}>
                {value?.status ?? "unknown"}
              </Badge>
            }
            columns={2}
          >
            {settingPairs(value?.settings).map(([name, setting]) => (
              <Definition key={name} label={toTitleCase(name)} value={setting} />
            ))}
          </DefinitionCard>
        ))}
      </div>
    </div>
  )
}
