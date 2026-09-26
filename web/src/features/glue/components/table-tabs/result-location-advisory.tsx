import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Advisory } from "@/components/ui/advisory"
import { Button } from "@/components/ui/button"
import { workGroupQueryOptions } from "@/features/athena/data"
import { athenaWorkGroupLink } from "../../athena-link"
import { QUERY_WORKGROUP } from "../../athena-query"

/**
 * Athena, like AWS, refuses a query with nowhere to write its result, and a
 * new account's `primary` workgroup names no location. Said before the
 * reader clicks, with the way to set one, rather than after as an error.
 */
export function ResultLocationAdvisory() {
  const { data: workGroup } = useQuery(workGroupQueryOptions(QUERY_WORKGROUP))
  const config = workGroup?.Configuration
  const hasLocation =
    !!config?.ResultConfiguration?.OutputLocation ||
    !!config?.ManagedQueryResultsConfiguration?.Enabled
  if (!workGroup || hasLocation) return null
  return (
    <Advisory
      tone="info"
      title={`The ${QUERY_WORKGROUP} workgroup has no query result location`}
      docsPath="services/athena.md"
      action={
        <Button size="sm" variant="outline" asChild>
          <Link {...athenaWorkGroupLink(QUERY_WORKGROUP)}>Set one in Athena</Link>
        </Button>
      }
    >
      Athena writes every result, DDL included, to S3, so these actions fail until the workgroup
      names an output location — as they would on AWS.
    </Advisory>
  )
}
