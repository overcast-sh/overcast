* [elasticache] The cluster, replication-group and parameter-group not-found faults answer HTTP 404 rather than 400.
  `CacheClusterNotFound`, `ReplicationGroupNotFoundFault` and `CacheParameterGroupNotFound` each bind `httpResponseCode: 404` in the model; the wire codes and messages are unchanged.
  `CacheSubnetGroupNotFoundFault` keeps the 400 its own trait declares, because the status is per fault rather than per service.
