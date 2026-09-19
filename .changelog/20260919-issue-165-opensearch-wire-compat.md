* [opensearch] `DomainName` and `EngineVersion` are checked against the constraints AWS declares, where a malformed value used to create a domain (#165)
  a name outside 3-28 characters of `[a-z][a-z0-9-]` produced a malformed ARN and endpoint hostname, and an engine version outside `OpenSearch_X.Y`/`Elasticsearch_X.Y` produced a wrong `EngineType`
  `DescribeDomain` and `DeleteDomain` answer `ValidationException` for such a name rather than `ResourceNotFoundException`, because AWS validates input before it looks anything up
* [opensearch] `DomainStatus` carries `ClusterConfig`, one of the four members AWS marks required and always sends
  it echoes the configuration the create asked for, or an empty object when it asked for none — Overcast runs no cluster to describe
