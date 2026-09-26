import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import type { Partition, Table } from "@aws-sdk/client-glue"
import { FolderTree, Layers } from "lucide-react"
import { EmptyState } from "@/components/ui/primitives"
import { CreateAction, ResourceListCard } from "@/components/ui/resource-list-page"
import { ResourceTable } from "@/components/ui/resource-table"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { formatDate } from "@/lib/format"
import { parseS3Uri } from "@/lib/s3-uri"
import { gluePartitionsQueryOptions } from "../../data"
import { tableLocation } from "../../table-format"
import { AddPartitionDialog } from "./add-partition-dialog"
import { DiscoverPartitionsButton } from "./discover-partitions-button"
import { ExpressionFilter } from "./expression-filter"
import { ResultLocationAdvisory } from "./result-location-advisory"

interface PartitionsTabProps {
  table: Table
  /** The Glue `Expression`, owned by the route's `q` search param. */
  expression: string
  onExpressionChange: (expression: string) => void
}

/**
 * The partitions GetPartitions returns for the expression as typed — the
 * service parses it, and its parse errors show under the box word for word.
 * Partitions are added one by one, or discovered with `MSCK REPAIR TABLE`.
 */
export function PartitionsTab({ table, expression, onExpressionChange }: PartitionsTabProps) {
  const database = table.DatabaseName ?? ""
  const name = table.Name ?? ""
  const keys = (table.PartitionKeys ?? []).map((k) => k.Name ?? "")
  const [adding, setAdding] = useState(false)
  const partitions = useQuery({
    ...gluePartitionsQueryOptions(database, name, expression),
    enabled: keys.length > 0,
  })

  if (keys.length === 0) {
    return (
      <ResourceListCard>
        <EmptyState
          icon={<Layers className="h-10 w-10" />}
          title="Not partitioned"
          description="This table declares no partition keys, so every query reads all of its location."
        />
      </ResourceListCard>
    )
  }

  const add = <CreateAction onClick={() => setAdding(true)}>Add partition</CreateAction>
  return (
    <div className="flex flex-col gap-3">
      <ResultLocationAdvisory suggestedBucket={parseS3Uri(tableLocation(table) ?? "")?.bucket} />
      <div className="flex flex-wrap items-start gap-2">
        <ExpressionFilter
          // Remounts on a change from outside (Clear filter, Back), so the box shows it.
          key={expression}
          className="min-w-64 flex-1"
          value={expression}
          onSubmit={onExpressionChange}
          error={expression ? partitions.error : undefined}
          example={`${keys[0]} = '…'`}
        />
        <DiscoverPartitionsButton database={database} table={name} />
        {add}
      </div>
      {/* An expression the service could not parse has its error under the box,
          where it was typed; a table beneath it would only repeat it as "no matches". */}
      {!(expression && partitions.error) && (
        <ResourceTable<Partition>
          query={{
            data: partitions.data,
            isLoading: partitions.isLoading,
            error: partitions.error,
          }}
          noun="partitions"
          rowKey={(p) => (p.Values ?? []).join("\u0000")}
          defaultSort={{ id: "values", desc: false }}
          isFiltered={expression !== ""}
          onClearFilter={() => onExpressionChange("")}
          filteredEmptyTitle="No matching partitions"
          filteredEmptyDescription="No partition satisfies the expression."
          emptyIcon={FolderTree}
          emptyTitle="No partitions yet"
          emptyDescription="Add one, or discover them: MSCK REPAIR TABLE registers every key=value/ folder under the location."
          emptyAction={add}
          columns={[
            {
              id: "values",
              header: "Partition",
              sortValue: (p) => (p.Values ?? []).join("/"),
              cell: (p) => keys.map((k, i) => `${k}=${p.Values?.[i] ?? ""}`).join(" / "),
            },
            {
              id: "location",
              header: "Location",
              interactive: true,
              cell: (p) =>
                p.StorageDescriptor?.Location ? (
                  <S3UriLink uri={p.StorageDescriptor.Location} />
                ) : (
                  <span className="text-fg-subtle">—</span>
                ),
            },
            {
              id: "created",
              header: "Created",
              sortValue: (p) => p.CreationTime,
              cell: (p) => formatDate(p.CreationTime),
            },
          ]}
        />
      )}
      <AddPartitionDialog table={table} open={adding} onOpenChange={setAdding} />
    </div>
  )
}
