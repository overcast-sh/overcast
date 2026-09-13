* [elasticache] cache endpoints are minted for whoever asks, on every read, instead of overwritten with the address Overcast itself dials
  A Lambda function or ECS task gets the endpoint hostname and the engine port, the host the published port.
  The record used to be rewritten after the container started with the address Overcast itself dials, so a
  function reading the endpoint at runtime was given `127.0.0.1` and connected to itself.
* [elasticache] a replication-group container adopted after a restart rejoins its subnet group's VPC network
  It used to be re-attached with no VPC at all and landed on the default plane, where the tasks in its VPC
  could not resolve it, while a freshly created group was placed correctly.
* [elasticache] `CreateCacheCluster` and `CreateReplicationGroup` refuse an unknown `CacheSubnetGroupName` with `CacheSubnetGroupNotFoundFault`
  Accepting it silently placed the cache outside its VPC, and the first symptom was a consumer that could not
  resolve the endpoint.
* [rds] `PubliclyAccessible` decides placement as well as the response
  A public instance in a subnet group joins the shared data plane as well as its VPC network, and
  `ModifyDBInstance` re-attaches a running container when the flag changes. It was stored and echoed but never
  reached the wiring.
* [efs] a mount target's NFS export joins its subnet's VPC network, on create and on adoption after a restart
  It carried the file system's endpoint names on the default plane only, so a task or function in the
  mount target's VPC could not resolve them.
* [eks] a live cluster joins its VPC network before it starts, rejoins it after a restart, and honours `endpointPublicAccess`
  `DescribeCluster` and `UpdateKubeconfig` also hand a sibling container the cluster's endpoint name on the
  API port, rather than a host port nothing inside the network listens on.
+ [router] the `data-plane-name-refused` health advisory names each endpoint the resolver refused, and both containers involved
  The refusal is the placement the template asked for — the two resources are in different VPCs — but from
  inside the application it is only `Temporary failure in name resolution`. The WARN line carries the fix too.
