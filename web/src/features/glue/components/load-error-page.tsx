import { EntityNotFoundException } from "@aws-sdk/client-glue"
import { AlertTriangle, SearchX } from "lucide-react"
import { EmptyState } from "@/components/ui/primitives"
import { ResourceListPage } from "@/components/ui/resource-list-page"

interface LoadErrorPageProps {
  /** The page's title: the name the URL asked for. */
  title: string
  meta: string
  /** What was asked for: "database", "table". */
  noun: string
  error: Error
}

/**
 * A database or table page whose resource did not load. "Not found" only when
 * Glue said so; any other failure is said as one, with the service's words.
 */
export function LoadErrorPage({ title, meta, noun, error }: LoadErrorPageProps) {
  const missing = error instanceof EntityNotFoundException
  return (
    <ResourceListPage title={title} meta={meta}>
      <EmptyState
        icon={missing ? <SearchX className="h-10 w-10" /> : <AlertTriangle className="h-10 w-10" />}
        title={missing ? `No such ${noun}` : `Could not load the ${noun}`}
        description={error.message}
      />
    </ResourceListPage>
  )
}
