import type { ReactNode } from "react"
import type { UseQueryResult } from "@tanstack/react-query"
import { FileJson } from "lucide-react"
import { EmptyState } from "@/components/ui/primitives"
import { SkeletonRows } from "@/components/ui/skeleton"
import type { MetadataFile } from "./data"
import type { IcebergMetadata } from "./metadata"

/** A metadata file that could not be read, or does not read as Iceberg table metadata. */
export function UnreadableMetadata({ reason }: { reason?: string }) {
  return (
    <EmptyState
      icon={<FileJson className="size-8" />}
      title="Could not read the table's metadata"
      description={
        reason ??
        "The metadata file is not Iceberg table metadata, or is larger than the console reads."
      }
    />
  )
}

interface MetadataGateProps {
  /** The table's metadata location; absent while the table has none. */
  location: string | undefined
  query: UseQueryResult<MetadataFile>
  /** Why a table has no metadata yet, in its catalog's terms. */
  missingDescription: string
}

/**
 * The metadata file a view reads, once it is there: a skeleton while it
 * loads, and a plain reason when the table has none or it could not be read.
 */
export function MetadataGate({
  location,
  query,
  missingDescription,
  children,
}: MetadataGateProps & { children: (file: MetadataFile) => ReactNode }) {
  if (!location) {
    return (
      <EmptyState
        icon={<FileJson className="size-8" />}
        title="No metadata yet"
        description={missingDescription}
      />
    )
  }
  if (query.isLoading) return <SkeletonRows rows={6} noun="metadata" />
  return query.data ? children(query.data) : <UnreadableMetadata reason={query.error?.message} />
}

/** The same gate, for a view that needs the file to read as Iceberg table metadata. */
export function ParsedMetadataGate({
  children,
  ...gate
}: MetadataGateProps & { children: (metadata: IcebergMetadata) => ReactNode }) {
  return (
    <MetadataGate {...gate}>
      {(file) => (file.metadata ? children(file.metadata) : <UnreadableMetadata />)}
    </MetadataGate>
  )
}
