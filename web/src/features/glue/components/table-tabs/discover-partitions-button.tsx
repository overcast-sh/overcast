import { useQuery } from "@tanstack/react-query"
import { ScanSearch } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useToast } from "@/components/ui/toast"
import { engineStatusQueryOptions } from "@/features/athena/data"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { hiveIdentifier } from "@/lib/sql-quote"
import { queryFailure } from "../../athena-query"
import { glueKeys, repairTableMutationOptions } from "../../data"

interface DiscoverPartitionsButtonProps {
  database: string
  table: string
}

/**
 * *Discover partitions*: `MSCK REPAIR TABLE`, run through Athena exactly as
 * a developer would run it. It is DDL, so it works with the query engine off
 * too — the catalog is updated either way — and the result says which.
 */
export function DiscoverPartitionsButton({ database, table }: DiscoverPartitionsButtonProps) {
  const { toast } = useToast()
  const engine = useQuery(engineStatusQueryOptions())
  const engineOff = engine.data?.state === "off"
  const repair = useResourceMutation({
    options: repairTableMutationOptions(database),
    invalidateKeys: [glueKeys.partitions()],
    errorTitle: "Could not start MSCK REPAIR TABLE",
    onSuccess: (execution) => {
      const failure = queryFailure(execution)
      toast(
        failure
          ? { title: "MSCK REPAIR TABLE failed", description: failure, variant: "danger" }
          : {
              title: "Partitions discovered",
              description: engineOff
                ? "Ran as DDL against the catalog: the query engine is off, but partition discovery does not need it."
                : `Every key=value/ folder under ${table}'s location is registered.`,
              variant: "success",
            },
      )
    },
  })

  return (
    <Button
      size="sm"
      variant="ghost"
      busy={repair.isPending}
      busyLabel="Discovering"
      title="Run MSCK REPAIR TABLE through Athena"
      onClick={() =>
        repair.mutate(`MSCK REPAIR TABLE ${hiveIdentifier(database)}.${hiveIdentifier(table)}`)
      }
    >
      <ScanSearch className="h-3.5 w-3.5" />
      Discover partitions
    </Button>
  )
}
