import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Terminal } from "lucide-react"
import { Advisory } from "@/components/ui/advisory"
import { Button } from "@/components/ui/button"
import { workGroupQueryOptions } from "@/features/athena/data"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import { copyAwsCommand, type AwsService } from "@/lib/aws-command"
import { athenaWorkGroupLink } from "../../athena-link"
import { QUERY_WORKGROUP } from "../../athena-query"

const ATHENA: AwsService = {
  cli: "athena",
  sdkPackage: "@aws-sdk/client-athena",
  sdkClient: "AthenaClient",
}

interface ResultLocationAdvisoryProps {
  /** Where results could go: the table's own bucket, under `athena-results/`. */
  suggestedBucket?: string
}

/**
 * Athena, like AWS, refuses a query with nowhere to write its result, and a
 * new account's `primary` workgroup names no location. Said before the
 * reader clicks, with two ways to set one: the Athena workgroup page, or the
 * CLI command that does it, aimed at this emulator.
 */
export function ResultLocationAdvisory({ suggestedBucket }: ResultLocationAdvisoryProps) {
  const { copy } = useCopyToClipboard()
  const { data: workGroup } = useQuery(workGroupQueryOptions(QUERY_WORKGROUP))
  const config = workGroup?.Configuration
  const hasLocation =
    !!config?.ResultConfiguration?.OutputLocation ||
    !!config?.ManagedQueryResultsConfiguration?.Enabled
  if (!workGroup || hasLocation) return null

  const outputLocation = `s3://${suggestedBucket ?? "YOUR-BUCKET"}/athena-results/`
  const copyCli = () =>
    copyAwsCommand(
      copy,
      {
        service: ATHENA,
        operation: "UpdateWorkGroup",
        input: {
          WorkGroup: QUERY_WORKGROUP,
          ConfigurationUpdates: { ResultConfigurationUpdates: { OutputLocation: outputLocation } },
        },
      },
      "cli",
    )
  return (
    <Advisory
      tone="info"
      title={`The ${QUERY_WORKGROUP} workgroup has no query result location`}
      docsPath="services/athena.md"
      action={
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="ghost" onClick={copyCli}>
            <Terminal className="h-3.5 w-3.5" />
            Copy CLI command
          </Button>
          <Button size="sm" variant="outline" asChild>
            <Link {...athenaWorkGroupLink(QUERY_WORKGROUP)}>Set one in Athena</Link>
          </Button>
        </div>
      }
    >
      Athena writes every result, DDL included, to S3, so these actions fail until the workgroup
      names an output location, as they would on AWS. The CLI command sets it to{" "}
      <code className="font-mono">{outputLocation}</code>.
    </Advisory>
  )
}
