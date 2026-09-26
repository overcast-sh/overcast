import type { NamespaceSummary, TableBucketSummary, TableSummary } from "@aws-sdk/client-s3tables"
import { s3tables } from "@/services/api"
import type { InTableBucket } from "@/services/api/s3tables"
import { s3tablesKeys, tableIdOf } from "@/features/s3tables/data"
import { createSearchContributor } from "./create-contributor"

const bucketHref = (bucket: string) => `/s3tables/${encodeURIComponent(bucket)}`

createSearchContributor<TableBucketSummary>({
  id: "s3tables:buckets",
  cacheKey: () => s3tablesKeys.buckets(),
  fetchAll: () => s3tables.listTableBuckets(),
  matchFields: (b) => [b.name, b.arn],
  toResult: (b) => ({
    id: `s3tables:bucket:${b.name}`,
    label: b.name ?? "",
    sublabel: b.arn,
    service: "S3 Tables",
    serviceKey: "/s3tables",
    type: "Table bucket",
    href: bucketHref(b.name ?? ""),
  }),
})

createSearchContributor<InTableBucket<NamespaceSummary>>({
  id: "s3tables:namespaces",
  cacheKey: () => s3tablesKeys.allNamespaces(),
  fetchAll: () => s3tables.listAllNamespaces(),
  matchFields: (n) => n.namespace ?? [],
  toResult: (n) => {
    const namespace = n.namespace?.join(".") ?? ""
    return {
      id: `s3tables:namespace:${n.tableBucketName}/${namespace}`,
      label: namespace,
      sublabel: n.tableBucketName,
      service: "S3 Tables",
      serviceKey: "/s3tables",
      type: "Namespace",
      // The bucket page's filter keeps a namespace whose name matches whole.
      href: `${bucketHref(n.tableBucketName)}?${new URLSearchParams({ q: namespace }).toString()}`,
    }
  },
})

createSearchContributor<InTableBucket<TableSummary>>({
  id: "s3tables:tables",
  cacheKey: () => s3tablesKeys.allTables(),
  fetchAll: () => s3tables.listAllTables(),
  matchFields: (t) => [t.name, t.tableARN],
  toResult: (t) => {
    const tableId = tableIdOf(t.tableARN)
    return {
      id: `s3tables:table:${t.tableARN}`,
      label: t.name ?? "",
      sublabel: `${t.tableBucketName} · ${t.namespace?.join(".") ?? ""}`,
      service: "S3 Tables",
      serviceKey: "/s3tables",
      type: "Table",
      href: `${bucketHref(t.tableBucketName)}/${encodeURIComponent(tableId)}`,
    }
  },
})
