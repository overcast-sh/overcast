import { useQuery } from "@tanstack/react-query"
import { Advisory } from "@/components/ui/advisory"
import { ArnLink } from "@/components/ui/arn-link"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import { SkeletonRows } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/ui/primitives"
import { encryptionQueryOptions, type ConfigTarget } from "../data"

/** The server-side encryption new tables inherit. */
export function EncryptionTab({ target }: { target: ConfigTarget }) {
  const { data, isLoading, error } = useQuery(encryptionQueryOptions(target))
  if (isLoading) return <SkeletonRows rows={2} noun="encryption" />
  if (error) {
    return (
      <EmptyState title="Could not read the encryption configuration" description={error.message} />
    )
  }
  return (
    <div className="flex flex-col gap-3">
      <Advisory
        tone="info"
        title="Stored, not applied"
        docsPath="services/s3tables/limitations.md#stored-never-run"
      >
        New tables inherit this setting and report it, but Overcast writes their files unencrypted.
      </Advisory>
      <DefinitionCard columns={2}>
        <Definition label="Algorithm" value={data?.sseAlgorithm ?? "AES256"} />
        <Definition
          label="KMS key"
          value={data?.kmsKeyArn ? <ArnLink arn={data.kmsKeyArn} /> : undefined}
        />
      </DefinitionCard>
    </div>
  )
}
