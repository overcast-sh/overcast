//go:build dev

package elasticache

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		capabilities.Capability{Service: "elasticache", Operation: "AddTagsToResource", Category: "General", Status: capabilities.StatusSupported, Notes: "ARN-scoped tag storage"},
		capabilities.Capability{Service: "elasticache", Operation: "CreateCacheCluster", Category: "General", Status: capabilities.StatusSupported, Notes: "Docker-backed (redis/valkey/memcached); async creating→available once the engine answers PING; a cache that never answers ends in `incompatible-network`; port auto-alloc. CacheClusterId is validated to AWS’s 1–50 letter-led grammar and stored lowercase; NumCacheNodes is bounded per engine (1 for redis/valkey, 1–40 for memcached); AZMode and PreferredAvailabilityZones are memcached-only and the zone list must match the node count; ReplicationGroupId must name an existing group and adds the cluster to it. The response carries CacheNodes, CacheClusterCreateTime and the nested CacheParameterGroup, and ConfigurationEndpoint only for memcached, as AWS does. The CloudFormation resource’s Port, VpcSecurityGroupIds, TransitEncryptionEnabled, SnapshotRetentionLimit, NotificationTopicArn, PreferredMaintenanceWindow, AutoMinorVersionUpgrade and LogDeliveryConfigurations are not modeled"},
		capabilities.Capability{Service: "elasticache", Operation: "CreateCacheParameterGroup", Category: "General", Status: capabilities.StatusSupported, Notes: "Stores name, family, description, and ARN"},
		capabilities.Capability{Service: "elasticache", Operation: "CreateCacheSubnetGroup", Category: "General", Status: capabilities.StatusSupported, Notes: "Stores name, description, and subnet IDs"},
		capabilities.Capability{Service: "elasticache", Operation: "CreateReplicationGroup", Category: "General", Status: capabilities.StatusSupported, Notes: "Docker-backed (single primary node); async creating→available once the primary answers PING; a group that never answers ends in `create-failed`; `CacheSubnetGroupName` places the group in that subnet group’s VPC. ReplicationGroupId is validated to AWS’s 1–40 letter-led grammar and stored lowercase, and Engine must be redis or valkey — memcached does not replicate. Engine/EngineVersion pick the container image; SnapshotRetentionLimit and PrimaryClusterId round-trip from Create, and a group with no PrimaryClusterId names its primary `<group>-001` as AWS does. The response carries one NodeGroup with PrimaryEndpoint/ReaderEndpoint/NodeGroupMembers, ClusterEnabled false and ReplicationGroupCreateTime; ConfigurationEndpoint stays null, as AWS leaves it for a cluster-mode-disabled group. The CloudFormation resource’s NumCacheClusters, NumNodeGroups, ReplicasPerNodeGroup, CacheParameterGroupName, AtRestEncryptionEnabled, TransitEncryptionEnabled, AuthToken, KmsKeyId, UserGroupIds, CacheSecurityGroupNames, LogDeliveryConfigurations, Tags, NotificationTopicArn, PreferredMaintenanceWindow, SnapshotWindow and SecurityGroupIds are not modeled"},
		capabilities.Capability{Service: "elasticache", Operation: "CreateServerlessCache", Category: "General", Status: capabilities.StatusSupported, Notes: "Docker-backed (redis/valkey/memcached); async creating→available once the engine answers PING; a cache that never answers ends in `create-failed`; CloudFormation ServerlessCache supported"},
		capabilities.Capability{Service: "elasticache", Operation: "DeleteCacheCluster", Category: "General", Status: capabilities.StatusSupported, Notes: "Sets status to \"deleting\"; stops and removes Docker container asynchronously"},
		capabilities.Capability{Service: "elasticache", Operation: "DeleteCacheParameterGroup", Category: "General", Status: capabilities.StatusSupported, Notes: "Removes stored parameter group"},
		capabilities.Capability{Service: "elasticache", Operation: "DeleteCacheSubnetGroup", Category: "General", Status: capabilities.StatusSupported, Notes: "Removes stored subnet group"},
		capabilities.Capability{Service: "elasticache", Operation: "DeleteReplicationGroup", Category: "General", Status: capabilities.StatusSupported, Notes: "Sets status to \"deleting\"; stops and removes Docker container asynchronously"},
		capabilities.Capability{Service: "elasticache", Operation: "DeleteServerlessCache", Category: "General", Status: capabilities.StatusSupported, Notes: "Sets status to \"deleting\"; stops and removes Docker container asynchronously"},
		capabilities.Capability{Service: "elasticache", Operation: "DescribeCacheClusters", Category: "General", Status: capabilities.StatusSupported, Notes: "List all or filter by CacheClusterId"},
		// Routed, but handler_stubs.go answers NotImplemented (TODO priority:P3) —
		// which is precisely what StatusUnsupported means. Declared Supported, the
		// matrix advertised an operation that always 501s.
		capabilities.Capability{Service: "elasticache", Operation: "DescribeCacheEngineVersions", Category: "General", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "elasticache", Operation: "DescribeCacheParameterGroups", Category: "General", Status: capabilities.StatusSupported, Notes: "List all or filter by name"},
		capabilities.Capability{Service: "elasticache", Operation: "DescribeCacheParameters", Category: "General", Status: capabilities.StatusSupported, Notes: "Returns curated static parameters for the group's family; supports Source filter and MaxRecords/Marker pagination"},
		capabilities.Capability{Service: "elasticache", Operation: "DescribeCacheSubnetGroups", Category: "General", Status: capabilities.StatusSupported, Notes: "List all or filter by name"},
		capabilities.Capability{Service: "elasticache", Operation: "DescribeReplicationGroups", Category: "General", Status: capabilities.StatusSupported, Notes: "List all or filter by ReplicationGroupId"},
		capabilities.Capability{Service: "elasticache", Operation: "DescribeServerlessCaches", Category: "General", Status: capabilities.StatusSupported, Notes: "List all or filter by ServerlessCacheName"},
		capabilities.Capability{Service: "elasticache", Operation: "ListTagsForResource", Category: "General", Status: capabilities.StatusSupported, Notes: "Returns all tags for an ARN"},
		capabilities.Capability{Service: "elasticache", Operation: "ModifyCacheCluster", Category: "General", Status: capabilities.StatusSupported, Notes: "Metadata-only; updates nodeType, engineVersion, numNodes, parameterGroup; modifying→available"},
		capabilities.Capability{Service: "elasticache", Operation: "ModifyReplicationGroup", Category: "General", Status: capabilities.StatusSupported, Notes: "Metadata-only; updates description, nodeType, failover, multiAZ; modifying→available"},
		capabilities.Capability{Service: "elasticache", Operation: "ModifyServerlessCache", Category: "General", Status: capabilities.StatusSupported, Notes: "Metadata-only; updates description, engine/version, usage limits, security groups, snapshots, and user group; modifying→available"},
		// Same: routed to a NotImplemented stub, so Unsupported is the honest status.
		capabilities.Capability{Service: "elasticache", Operation: "RebootCacheCluster", Category: "General", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "elasticache", Operation: "RemoveTagsFromResource", Category: "General", Status: capabilities.StatusSupported, Notes: "Removes specific tag keys"},
	)
}
