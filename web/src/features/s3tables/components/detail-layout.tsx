import type { ReactNode } from "react"
import { Link } from "@tanstack/react-router"
import { Database } from "lucide-react"
import { Button } from "@/components/ui/button"
import { EmptyState, PageHeader } from "@/components/ui/primitives"
import { ResourceListCard } from "@/components/ui/resource-list-page"
import { SkeletonRows } from "@/components/ui/skeleton"
import { ConnectPanel } from "./connect-panel"
import type { ConnectTarget } from "../connect-snippets"

/** The page column every S3 Tables detail state sits in. */
const PAGE = "flex w-full flex-col gap-4"

/**
 * A bucket's or a table's page: the header, the tabs, and the *Connect a
 * client* panel always in view beside them — beneath them on a narrow
 * screen, where there is no room beside.
 */
export function DetailLayout({
  header,
  connect,
  children,
}: {
  header: ReactNode
  connect: Pick<ConnectTarget, "warehouseArn" | "table">
  children: ReactNode
}) {
  return (
    <div className={PAGE}>
      {header}
      <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="min-w-0">{children}</div>
        <ConnectPanel {...connect} className="xl:sticky xl:top-4" />
      </div>
    </div>
  )
}

/** The page while the resource is loading: its name, and a static skeleton. */
export function DetailLoading({ title }: { title: string }) {
  return (
    <div className={PAGE}>
      <PageHeader title={title} />
      <ResourceListCard>
        <SkeletonRows rows={6} />
      </ResourceListCard>
    </div>
  )
}

/** The page for a resource that does not exist, or could not be read. */
export function DetailMissing({
  title,
  heading,
  description,
}: {
  title: string
  heading: string
  description: string
}) {
  return (
    <div className={PAGE}>
      <PageHeader title={title} />
      <ResourceListCard>
        <EmptyState
          icon={<Database className="size-8" />}
          title={heading}
          description={description}
          action={
            <Button size="sm" variant="outline" asChild>
              <Link to="/s3tables">All table buckets</Link>
            </Button>
          }
        />
      </ResourceListCard>
    </div>
  )
}
