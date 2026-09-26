import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, FileCode, Pencil } from "lucide-react"
import { Advisory } from "@/components/ui/advisory"
import { Button } from "@/components/ui/button"
import { DefinitionCard, Definition } from "@/components/ui/definition-card"
import { EmptyState, SectionLabel } from "@/components/ui/primitives"
import { ResourceTable } from "@/components/ui/resource-table"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { SkeletonCards } from "@/components/ui/skeleton"
import { formatBytes, formatDate } from "@/lib/format"
import { preparedStatementsQueryOptions, workGroupQueryOptions } from "../data"
import { WorkGroupFormDialog } from "./workgroup-form-dialog"

/**
 * One workgroup: its configuration, which applies to every query run in it,
 * and its prepared statements.
 */
export function WorkGroupDetail({ name, onBack }: { name: string; onBack: () => void }) {
  const { data: workGroup, isLoading, error } = useQuery(workGroupQueryOptions(name))
  const prepared = useQuery(preparedStatementsQueryOptions(name))
  const [editing, setEditing] = useState(false)
  const config = workGroup?.Configuration
  const location = config?.ResultConfiguration?.OutputLocation
  const cutoff = config?.BytesScannedCutoffPerQuery

  return (
    <div className="flex flex-col gap-4 pt-4">
      <div className="flex items-center gap-2">
        <Button variant="ghost" size="sm" onClick={onBack}>
          <ArrowLeft aria-hidden className="size-3.5" />
          Workgroups
        </Button>
        <h2 className="font-mono text-sm font-bold text-fg">{name}</h2>
        {workGroup && (
          <Button variant="outline" size="sm" className="ml-auto" onClick={() => setEditing(true)}>
            <Pencil aria-hidden className="size-3.5" />
            Edit
          </Button>
        )}
      </div>
      {isLoading ? (
        <SkeletonCards cards={3} noun="workgroup" />
      ) : error ? (
        <EmptyState title="Could not load the workgroup" description={error.message} />
      ) : (
        <>
          {!location && (
            <Advisory
              title="This workgroup has no query result location"
              action={
                <Button size="sm" variant="outline" onClick={() => setEditing(true)}>
                  Set a location
                </Button>
              }
            >
              As on AWS, a query run here fails unless it names its own output location.
            </Advisory>
          )}
          <DefinitionCard title="Configuration">
            <Definition
              label="Result location"
              value={location ? <S3UriLink uri={location} /> : undefined}
            />
            <Definition
              label="Override client-side settings"
              value={config?.EnforceWorkGroupConfiguration ? "Enforced" : "Off"}
            />
            <Definition
              label="Engine version"
              value={config?.EngineVersion?.EffectiveEngineVersion}
            />
            <Definition
              label="Data scanned limit"
              value={cutoff ? `${formatBytes(cutoff)} per query` : undefined}
            />
            <Definition label="State" value={workGroup?.State} />
            <Definition label="Created" value={formatDate(workGroup?.CreationTime)} />
            <Definition label="Description" variant="prose" value={workGroup?.Description} full />
          </DefinitionCard>
        </>
      )}
      <section className="flex flex-col gap-2">
        <SectionLabel>Prepared statements</SectionLabel>
        <ResourceTable
          query={{ data: prepared.data, isLoading: prepared.isLoading, error: prepared.error }}
          noun="prepared statements"
          emptyIcon={FileCode}
          emptyTitle="No prepared statements"
          emptyDescription="Create one with PREPARE in the editor, then run it with EXECUTE … USING."
          rowKey={(s) => s.StatementName ?? ""}
          defaultSort={{ id: "name", desc: false }}
          columns={[
            {
              id: "name",
              header: "Name",
              sortValue: (s) => s.StatementName,
              cell: (s) => s.StatementName,
            },
            {
              id: "modified",
              header: "Last modified",
              cellClassName: "text-fg-muted",
              sortValue: (s) => s.LastModifiedTime,
              cell: (s) => formatDate(s.LastModifiedTime),
            },
          ]}
        />
      </section>
      {editing && workGroup && (
        <WorkGroupFormDialog workGroup={workGroup} onClose={() => setEditing(false)} />
      )}
    </div>
  )
}
