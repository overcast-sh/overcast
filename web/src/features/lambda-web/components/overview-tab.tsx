import { useQuery } from "@tanstack/react-query"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import { ResourceListCard } from "@/components/ui/resource-list-page"
import { formatDate } from "@/lib/format"
import type { WebFunction } from "@/services/api/lambda-web"
import {
  webAccountSettingsQueryOptions,
  webEndpointsQueryOptions,
  webRevisionsQueryOptions,
} from "../data"
import { StateBadge } from "./state-badge"
import { TagsSection } from "./tags-section"

/** A function's identity, how much it holds, the account's quotas, and its tags. */
export function OverviewTab({ fn }: { fn: WebFunction }) {
  const name = fn.functionName ?? ""
  const { data: revisions } = useQuery(webRevisionsQueryOptions(name))
  const { data: endpoints } = useQuery(webEndpointsQueryOptions(name))
  const { data: settings } = useQuery(webAccountSettingsQueryOptions())
  const quotas = settings?.accountQuotas
  return (
    <div className="flex flex-col gap-4">
      <DefinitionCard title="Web function">
        <Definition label="ARN" value={fn.functionArn} copyable full />
        <Definition label="State" value={<StateBadge state={fn.state} />} />
        <Definition label="State reason" value={fn.stateReason} variant="prose" />
        <Definition label="Created" value={formatDate(fn.createdAt)} />
        <Definition label="Updated" value={formatDate(fn.updatedAt)} />
        <Definition
          label="Revisions"
          value={
            revisions && quotas
              ? `${revisions.length} of ${quotas.maxRevisionsPerFunction}`
              : revisions?.length
          }
        />
        <Definition
          label="Endpoints"
          value={
            endpoints && quotas
              ? `${endpoints.length} of ${quotas.maxEndpointsPerFunction}`
              : endpoints?.length
          }
        />
      </DefinitionCard>
      {settings && (
        <DefinitionCard title="Account in this Region">
          <Definition label="Web functions" value={settings.accountUsage?.functionCount} />
          <Definition label="Max vCPUs" value={quotas?.maxTotalArmVCpus} />
          <Definition label="Max rate limit (req/s)" value={quotas?.maxTotalRateLimit} />
        </DefinitionCard>
      )}
      <section className="flex flex-col gap-2">
        <h2 className="text-sm font-medium">Tags</h2>
        <ResourceListCard>
          <div className="p-3">
            <TagsSection functionArn={fn.functionArn ?? ""} />
          </div>
        </ResourceListCard>
      </section>
    </div>
  )
}
