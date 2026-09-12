# STATUS.md — Current implementation status

> Tracks what's built and what's next. Update as features land.
> For coding conventions see [CONTRIBUTING.md](./CONTRIBUTING.md).
> For agent workflow rules see [AGENTS.md](./AGENTS.md).

---

## Service coverage

50 AWS services are registered. Coverage varies from comprehensive to stub.

### Comprehensive — core + advanced features

| Service     | Ops | Highlights                                                                                                       |
| ----------- | --- | ---------------------------------------------------------------------------------------------------------------- |
| S3          | 53  | Bucket CRUD, object CRUD, list, copy, multipart, notifications                                                   |
| SQS         | 21  | Queue + message CRUD, batches, purge, attributes, visibility, DLQ, FIFO, long polling                            |
| DynamoDB    | 28  | Table/item CRUD, Scan, Query, Streams, TTL, batch ops, transactions                                              |
| Lambda      | 62  | Function CRUD, Invoke (Docker), versions, aliases, layers, event source mappings, function URLs (Host-routed invoke); resource policies stored always, **opt-in** enforcement for service-originated invokes (`OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY`, default off) |
| API Gateway | 106 | REST v1 + HTTP v2: full CRUD, stages, deployments, Lambda/MOCK/HTTP proxy execution, authorizers, API keys       |
| AppSync     | 82  | Full CRUD, GraphQL execution (NONE/HTTP/Lambda/DynamoDB), CloudFormation/CDK provisioning, merged APIs, Events API, channel namespaces |
| CloudFront  | 89  | Distribution CRUD, invalidations, OAC/OAI, cache policies, CloudFront Functions, key groups, field-level encrypt |
| Cognito     | 70  | User Pools + Clients, Users, Auth flows, TOTP MFA, Groups, RS256 JWT + JWKS endpoint                             |
| EC2 / VPC   | 79  | Instances, VPCs, subnets, security groups, key pairs, route tables, IGWs, VPC peering                            |
| SNS         | 30  | Topics, subscriptions (SQS/email), Publish/PublishBatch, FilterPolicy message filtering                          |

### Core operations — basic CRUD + common features

| Service         | Ops | Highlights                                                                                                                                                                                                 |
| --------------- | --- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| IAM             | 74  | Users, roles, groups, policies, instance profiles; real policy simulation, **opt-in** request-time enforcement (`OVERCAST_ENFORCE_IAM`, default off)                                                                                                                                    |
| ECS             | 48  | Clusters, task definitions, tasks (Docker), services with reconciler                                                                                                                                       |
| ECR             | 22  | Repository CRUD + registry metadata (DescribeRegistry), image metadata (PutImage/DescribeImages/BatchGetImage/BatchDeleteImage/DescribeImageScanFindings), auth token, repository+lifecycle policies, tags |
| KMS             | 34  | Keys, aliases, symmetric AES-256-GCM + RSA-2048 signing                                                                                                                                                    |
| Kinesis         | 23  | Streams, records, shards, tags, retention                                                                                                                                                                  |
| EventBridge     | 29  | Event buses, rules, targets, PutEvents, tags                                                                                                                                                               |
| Scheduler       | 12  | Schedule groups, schedules, tags, clock-driven Lambda/SQS target firing                                                                                                                                    |
| CloudFormation  | 53  | Stacks, change sets, async provisioner (136 resource types — see `docs/cdk.md#supported-resource-types`), intrinsic functions, GetAtt                                                                       |
| RDS             | 34  | DB instances (Docker), start/stop, modify, subnet/parameter groups                                                                                                                                         |
| ElastiCache     | 24  | Clusters (Docker Redis), replication groups, subnet groups, tagging                                                                                                                                        |
| EFS             | 31  | File systems (Docker-volume-backed, `live` mode default), mount targets, access points, file-system policies, lifecycle/backup config, tagging                                                            |
| AppConfig       | 20  | Apps, environments, profiles, hosted config versions (CRUD + version counter)                                                                                                                              |
| AppConfigData   | 2   | StartConfigurationSession, GetLatestConfiguration; poll-based delivery with "unchanged" detection                                                                                                          |
| Secrets Manager | 22  | Secret CRUD, versioning, tags, real rotation (invokes the configured Lambda, all four steps), resource policies (stored, not evaluated — #496)                                                            |
| SSM             | 18  | Parameter Store: put, get, get-by-path, history, tags                                                                                                                                                      |
| CloudWatch Logs | 23  | Log groups, streams, events, FilterLogEvents, DeleteLogStream                                                                                                                                              |
| SES             | 45  | v1 + v2: SendEmail, SendRawEmail, identities, mail capture                                                                                                                                                 |
| STS             | 11  | GetCallerIdentity, AssumeRole, GetSessionToken, temp credentials                                                                                                                                           |
| Route 53        | 25  | Hosted zones (default NS/SOA, delegation sets), validated change batches, DNS-order pagination, tags, health checks (never probed); Overcast's own resolver now answers real DNS queries from a zone's records (A/AAAA/CNAME/MX/TXT/NS/SOA, wildcards, ALIAS — #1189)              |
| Step Functions  | 15  | State machine CRUD plus a real ASL interpreter: all eight state types, Retry/Catch, Lambda/SQS/SNS/DynamoDB/nested-execution Task integrations, real GetExecutionHistory. Executions run synchronously; unsupported ASL fails loudly |

### Minimal / Stub

| Service        | Ops | Highlights                                                      |
| -------------- | --- | --------------------------------------------------------------- |
| Pipes          | 8   | CreatePipe, DescribePipe, DeletePipe, ListPipes; DDB→SQS only   |
| WAF v2         | 7   | Metadata-only Web ACL CRUD; rules are stored but not evaluated or enforced |
| Shield         | 8   | Minimal subscription and protection metadata for CDK/CF workflows |

### Op counts from capability registry

<!--The tables below are Auto-generated by `go run -tags dev ./cmd/capgen --write-docs`. Do NOT edit manually, your changes will be lost! See ./CONTRIBUTING.md for details -->

<!-- BEGIN overcast:status -->

| Service         | Ops |
| --------------- | --- |
| S3              | 53  |
| SQS             | 21  |
| DynamoDB        | 28  |
| Lambda          | 62  |
| API Gateway     | 106 |
| AppSync         | 82  |
| CloudFront      | 89  |
| Cognito         | 70  |
| EC2 / VPC       | 79  |
| SNS             | 30  |
| IAM             | 74  |
| ECS             | 48  |
| ECR             | 22  |
| KMS             | 34  |
| Kinesis         | 23  |
| EventBridge     | 29  |
| Scheduler       | 12  |
| CloudFormation  | 53  |
| RDS             | 34  |
| ElastiCache     | 24  |
| EFS             | 31  |
| AppConfig       | 20  |
| AppConfigData   | 2   |
| Secrets Manager | 22  |
| SSM             | 18  |
| CloudWatch Logs | 23  |
| SES             | 45  |
| STS             | 11  |
| Route 53        | 25  |
| Auto Scaling    | 25  |
| Step Functions  | 15  |
| Pipes           | 8   |
| WAF v2          | 7   |
| Shield          | 8   |
| ACM             | 11  |
| Athena          | 11  |
| Bedrock         | 2   |
| CloudWatch      | 17  |
| DynamoDB Streams | 4   |
| Firehose        | 9   |
| Glue            | 11  |
| OpenSearch      | 8   |
| AppRegistry     | 22  |
| Backup          | 18  |
| CloudTrail      | 12  |
| EKS             | 50  |
| ELBv2           | 22  |
| MSK             | 30  |
| Organizations   | 15  |
| Transfer Family | 13  |

<!-- END overcast:status -->

---

## Current focus

This section previously named four items — full UpdateTable GSI/LSI support,
IAM integration tests, DynamoDB Streams tests, and real ECR registry
push/pull — that had all already shipped by the time this drift was reported
in [#744](https://github.com/overcast-sh/overcast/issues/744): none of the PRs that
finished them came back to remove the STATUS.md line. Rather than hand-curate
a list that has already gone stale twice, "current focus" now points at the
live source instead of restating it: filter
[GitHub Issues](https://github.com/overcast-sh/overcast/issues) by
[`priority/p0`](https://github.com/overcast-sh/overcast/issues?q=is%3Aissue+is%3Aopen+label%3Apriority%2Fp0)
for what's blocking or
[`priority/p1`](https://github.com/overcast-sh/overcast/issues?q=is%3Aissue+is%3Aopen+label%3Apriority%2Fp1)
for what's next. [#484](https://github.com/overcast-sh/overcast/issues/484) is the
prioritized Tier 2 full-emulation backlog itself.

## Future roadmap

Tracked in [GitHub Issues](https://github.com/overcast-sh/overcast/issues).
`// TODO(priority:Pn):` comments in code are auto-converted to issues.

- Step Functions `.waitForTaskToken`, activity tasks and distributed Map (the ASL interpreter landed; these are what it still refuses)
- API Gateway cache settings (`CacheClusterEnabled`/`Size`, `ClientCertificateId`, `DocumentationVersion`); usage-plan throttle/quota enforcement itself shipped and is opt-in via `OVERCAST_ENFORCE_APIGATEWAY_THROTTLE`
- Topology graph enhancements (`internal/router/topology.go`) — e.g. S3 → SNS notification edges via `TopicConfigurations`
