import type { NamedQuery, WorkGroupSummary } from "@aws-sdk/client-athena"
import { athena } from "@/services/api"
import { athenaKeys } from "@/features/athena/data"
import { ATHENA_TAB } from "@/components/ui/arn-routes"
import { createSearchContributor } from "./create-contributor"

/** The Athena page on one tab, filtered to `name`. */
function athenaHref(tab: string, name: string): string {
  return `/athena?${new URLSearchParams({ tab, q: name }).toString()}`
}

createSearchContributor<WorkGroupSummary>({
  id: "athena:workgroups",
  cacheKey: () => athenaKeys.workGroups(),
  fetchAll: () => athena.listWorkGroups(),
  matchFields: (wg) => [wg.Name, wg.Description],
  toResult: (wg) => ({
    id: `athena:workgroup:${wg.Name}`,
    label: wg.Name ?? "",
    sublabel: wg.Description || undefined,
    service: "Athena",
    serviceKey: "/athena",
    type: "Workgroup",
    href: athenaHref(ATHENA_TAB.workgroups, wg.Name ?? ""),
  }),
})

createSearchContributor<NamedQuery>({
  id: "athena:named-queries",
  cacheKey: () => athenaKeys.namedQueries(),
  fetchAll: () => athena.listAllNamedQueries(),
  matchFields: (q) => [q.Name, q.Description],
  toResult: (q) => ({
    id: `athena:named-query:${q.NamedQueryId}`,
    label: q.Name ?? "",
    sublabel: q.WorkGroup,
    service: "Athena",
    serviceKey: "/athena",
    type: "Saved query",
    href: athenaHref(ATHENA_TAB.savedQueries, q.Name ?? ""),
  }),
})
