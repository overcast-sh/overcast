import { msk } from "@/services/api"
import { createSearchContributor } from "./create-contributor"
import type { ClusterInfo } from "@/services/api/msk"
import { formatQuantity } from "@/lib/format"

createSearchContributor<ClusterInfo>({
  id: "msk",
  cacheKey: (ep) => [ep.baseUrl, ep.region, "msk", "clusters"] as const,
  fetchAll: () => msk.listClusters(),
  matchFields: (c) => [c.ClusterName, c.CurrentBrokerSoftwareInfo?.KafkaVersion, c.State],
  toResult: (c) => ({
    id: `msk:${c.ClusterArn}`,
    label: c.ClusterName ?? "",
    sublabel: `Kafka ${c.CurrentBrokerSoftwareInfo?.KafkaVersion ?? ""} · ${formatQuantity(c.NumberOfBrokerNodes ?? 0, "broker")}`,
    service: "MSK",
    serviceKey: "/msk",
    type: "Kafka Cluster",
    href: `/msk`,
  }),
})
