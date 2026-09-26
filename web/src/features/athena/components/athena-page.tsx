import { useQueryClient } from "@tanstack/react-query"
import { PageHeader } from "@/components/ui/primitives"
import { RefreshAction } from "@/components/ui/resource-list-page"
import type { ResourceTableSort } from "@/components/ui/resource-table"
import { Tab, TabList, TabPanel, Tabs } from "@/components/ui/tabs"
import { RawStateLink } from "@/features/debug/raw-state-link"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { SERVICES } from "@/lib/service-registry"
import { cn } from "@/lib/utils"
import { athenaKeys } from "../data"
import type { AthenaTab } from "../search"
import { DataCatalogsTab } from "./data-catalogs-tab"
import { QueryWorkspace, type EditorLink } from "./editor/query-workspace"
import { HistoryTab } from "./history-tab"
import { SavedQueriesTab } from "./saved-queries-tab"
import { WorkGroupsTab } from "./workgroups-tab"

const TAB_LABELS: Record<AthenaTab, string> = {
  editor: "Editor",
  history: "History",
  "saved-queries": "Saved queries",
  workgroups: "Workgroups",
  "data-catalogs": "Data catalogs",
}

export interface AthenaPageProps {
  tab: AthenaTab
  onTabChange: (tab: AthenaTab) => void
  filter: string
  onFilterChange: (value: string) => void
  sort?: ResourceTableSort
  onSortChange: (sort: ResourceTableSort | undefined) => void
  /** SQL another page asked the editor to open. */
  link?: EditorLink
  onLinkOpened: () => void
  /** `?execution=`: expanded in History. */
  execution?: string
  /** `?workgroup=`: open in Workgroups. */
  workGroup?: string
  onWorkGroupChange: (name: string | undefined) => void
}

/**
 * Athena: the query editor first — that is the job — and the lists behind
 * it. Every tab is `?tab=`, and every list's filter and sort are `q` and
 * `sort`.
 */
export function AthenaPage({
  tab,
  onTabChange,
  filter,
  onFilterChange,
  sort,
  onSortChange,
  link,
  onLinkOpened,
  execution,
  workGroup,
  onWorkGroupChange,
}: AthenaPageProps) {
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()
  const queryClient = useQueryClient()
  const list = { filter, onFilterChange, sort, onSortChange }
  const entry = SERVICES.athena

  return (
    // The editor fills the window, so its result grid scrolls rather than the page.
    <div
      className={cn("flex flex-col gap-4", tab === "editor" && "h-[calc(100dvh-7rem)] min-h-144")}
    >
      <PageHeader
        title={entry.label}
        description={entry.description}
        actions={
          <>
            <ServiceDocsButton
              service={entry.docKey}
              label={entry.label}
              open={docsOpen}
              onOpen={openDocs}
              onClose={closeDocs}
            />
            <RawStateLink service="athena" />
            <RefreshAction
              onClick={() => void queryClient.invalidateQueries({ queryKey: athenaKeys.all() })}
            />
          </>
        }
      />
      <Tabs
        selectedKey={tab}
        onSelectionChange={(key) => onTabChange(key as AthenaTab)}
        className="flex min-h-0 flex-1 flex-col"
      >
        <TabList aria-label="Athena">
          {(Object.keys(TAB_LABELS) as AthenaTab[]).map((id) => (
            <Tab key={id} id={id}>
              {TAB_LABELS[id]}
            </Tab>
          ))}
        </TabList>
        <TabPanel id="editor" className="flex min-h-0 flex-1 flex-col pt-4">
          <QueryWorkspace link={link} onLinkOpened={onLinkOpened} />
        </TabPanel>
        <TabPanel id="history">
          <HistoryTab {...list} execution={execution} />
        </TabPanel>
        <TabPanel id="saved-queries">
          <SavedQueriesTab {...list} />
        </TabPanel>
        <TabPanel id="workgroups">
          <WorkGroupsTab {...list} selected={workGroup} onSelect={onWorkGroupChange} />
        </TabPanel>
        <TabPanel id="data-catalogs">
          <DataCatalogsTab {...list} />
        </TabPanel>
      </Tabs>
    </div>
  )
}
