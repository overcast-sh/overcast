* [elasticache] CreateCacheCluster and CreateReplicationGroup validate their inputs and return the CacheNodes and NodeGroups shapes AWS models
  Identifiers follow AWS's grammar (1-50 for a cluster, 1-40 for a group, letter-led, no trailing or doubled hyphen) and are stored lowercase.
  NumCacheNodes is bounded per engine, AZMode and PreferredAvailabilityZones are Memcached-only, and a ReplicationGroupId must name a group that exists.
  ConfigurationEndpoint is now Memcached-only on a cluster and absent on a cluster-mode-disabled group, matching AWS; the address moved to the node and node group.
