import { useIsFetching, useQuery, useQueryClient } from "@tanstack/react-query"
import { CopyButton } from "@/components/ui/copy-button"
import { PageHeader } from "@/components/ui/primitives"
import { RefreshAction } from "@/components/ui/resource-list-page"
import { Tab, TabList, TabPanel, Tabs } from "@/components/ui/tabs"
import { RawStateLink } from "@/features/debug/raw-state-link"
import { formatDate } from "@/lib/format"
import { s3tablesKeys, tableBucketsQueryOptions, type ConfigTarget } from "../data"
import type { BucketTab } from "../search"
import { BucketTablesTab } from "./bucket-tables-tab"
import { DetailLayout, DetailLoading, DetailMissing } from "./detail-layout"
import { EncryptionTab } from "./encryption-tab"
import { MaintenanceTab } from "./maintenance-tab"
import { PolicyTab } from "./policy-tab"
import { TagsTab } from "./tags-tab"

interface TableBucketDetailProps {
  bucketName: string
  tab: BucketTab
  onTabChange: (tab: BucketTab) => void
  filter: string
  onFilterChange: (next: string) => void
}

/** `/s3tables/$bucket`: a table bucket's tables, and the configuration it holds. */
export function TableBucketDetail({
  bucketName,
  tab,
  onTabChange,
  filter,
  onFilterChange,
}: TableBucketDetailProps) {
  const queryClient = useQueryClient()
  // Refresh covers the whole page — the bucket, its namespaces and tables, and
  // the configuration tabs — so it shows busy while any of them refetches.
  const refreshing = useIsFetching({ queryKey: s3tablesKeys.all() }) > 0
  // The bucket is found by name in the list: GetTableBucket wants the ARN,
  // and the list answers from the same cache the index page filled.
  const {
    data: bucket,
    isLoading,
    error,
  } = useQuery({
    ...tableBucketsQueryOptions(),
    select: (buckets) => buckets.find((b) => b.name === bucketName),
  })
  if (isLoading) return <DetailLoading title={bucketName} />
  if (!bucket?.arn) {
    return (
      <DetailMissing
        title={bucketName}
        heading={error ? "Could not load the table bucket" : "No such table bucket"}
        description={
          error?.message ?? `There is no table bucket called ${bucketName} in this region.`
        }
      />
    )
  }
  const arn = bucket.arn
  const target: ConfigTarget = { resource: { kind: "bucket", tableBucketARN: arn }, arn }
  return (
    <DetailLayout
      connect={{ warehouseArn: arn }}
      header={
        <PageHeader
          title={bucketName}
          meta={
            <span className="inline-flex flex-wrap items-center gap-x-1.5">
              table bucket · {arn}
              <CopyButton value={arn} noun="table bucket ARN" tone="inline" />· created{" "}
              {formatDate(bucket.createdAt)}
            </span>
          }
          actions={
            <>
              <RawStateLink service="s3tables" />
              <RefreshAction
                isFetching={refreshing}
                onClick={() => void queryClient.invalidateQueries({ queryKey: s3tablesKeys.all() })}
              />
            </>
          }
        />
      }
    >
      <Tabs selectedKey={tab} onSelectionChange={(key) => onTabChange(key as BucketTab)}>
        <TabList aria-label="Table bucket views">
          <Tab id="tables">Tables</Tab>
          <Tab id="maintenance">Maintenance</Tab>
          <Tab id="policy">Policy</Tab>
          <Tab id="encryption">Encryption</Tab>
          <Tab id="tags">Tags</Tab>
        </TabList>
        <TabPanel id="tables" className="pt-4">
          <BucketTablesTab
            bucketName={bucketName}
            tableBucketARN={arn}
            filter={filter}
            onFilterChange={onFilterChange}
          />
        </TabPanel>
        <TabPanel id="maintenance" className="pt-4">
          <MaintenanceTab target={target} />
        </TabPanel>
        <TabPanel id="policy" className="pt-4">
          <PolicyTab target={target} />
        </TabPanel>
        <TabPanel id="encryption" className="pt-4">
          <EncryptionTab target={target} />
        </TabPanel>
        <TabPanel id="tags" className="pt-4">
          <TagsTab target={target} />
        </TabPanel>
      </Tabs>
    </DetailLayout>
  )
}
