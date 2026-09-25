import { BookOpen } from "lucide-react"
import { Button } from "@/components/ui/button"
import { CopyButton } from "@/components/ui/copy-button"
import { EmptyState } from "@/components/ui/primitives"
import { ResourceListCard, ResourceListPage } from "@/components/ui/resource-list-page"
import { ServiceIconTile } from "@/components/service/service-icon-tile"
import { RawStateLink } from "@/features/debug/raw-state-link"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { SERVICES } from "@/lib/service-registry"

/** The emulated services whose console pages are still to be built. */
export type PendingConsoleService = "athena" | "glue" | "s3tables"

export interface ConsolePendingPageProps {
  /** The service's SERVICES key: the page takes its name, glyph and docs from there. */
  service: PendingConsoleService
  /**
   * The resource a detail route names. Its name becomes the page title, so an
   * `ArnLink` or a search result that lands here still says what it pointed at.
   */
  resource?: {
    name: string
    /** What the resource is, and where — e.g. "Table in database sales". */
    kind: string
  }
}

/**
 * The page for a service Overcast emulates but whose console page is not
 * built yet. It has the shape of the service home it stands in for — Docs
 * and Raw state first in the header, as on every home page — so the routes,
 * the sidebar entry and every link into them work now, and the real page
 * replaces the body without moving anything.
 */
export function ConsolePendingPage({ service, resource }: ConsolePendingPageProps) {
  const entry = SERVICES[service]
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()

  return (
    <ResourceListPage
      title={resource?.name ?? entry.label}
      meta={resource?.kind}
      description={resource ? undefined : entry.description}
      actions={
        <>
          <ServiceDocsButton
            service={entry.docKey}
            label={entry.label}
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
          <RawStateLink service={service} />
          {resource && <CopyButton value={resource.name} noun="name" />}
        </>
      }
    >
      <ResourceListCard>
        <EmptyState
          icon={<ServiceIconTile service={entry} size={40} iconSize={20} />}
          title="No console page yet"
          description={`${entry.label} is emulated and answers the AWS API now. Its console page is still being built; until it lands, use the AWS CLI or an SDK.`}
          action={
            <Button size="sm" variant="outline" onClick={openDocs}>
              <BookOpen className="h-3.5 w-3.5" />
              Read the {entry.label} docs
            </Button>
          }
        />
      </ResourceListCard>
    </ResourceListPage>
  )
}
