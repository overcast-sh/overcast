---
title: "Route 53 limitations"
description: "Where Overcast's Route 53 diverges from AWS: unevaluated routing policies, unprobed health checks, private zones without VPC scoping, and the resolver's edge cases."
section: "Service Reference"
tags:
  - docs
  - limitations
  - route53
  - services
---

# Route 53 limitations

Every divergence from real Route 53. The working set is on
[Route 53](../route53.md).

## Not evaluated

| Feature | Stored and returned | Never acted on |
| --- | --- | --- |
| Routing policies | `SetIdentifier`, `Weight`, `Region`, `Failover`, `GeoLocation`, `MultiValueAnswer`, `HealthCheckId` | No choice is made. Several record sets sharing a name and type under different `SetIdentifier`s are all merged into one answer |
| Health checks | Configuration, with AWS's defaults — `RequestInterval` 30, `FailureThreshold` 3, port 80 or 443 by type — versioned by `UpdateHealthCheck` | No endpoint is ever probed. `GetHealthCheckStatus` and `GetHealthCheckLastFailureReason` return `501` |
| Traffic policies | — | `CreateTrafficPolicy` returns `501` |

Routing policies choose between multi-region infrastructure a single local node
does not have, so none of the selection logic is emulated.

## Private zones and VPCs

Real Route 53 serves a private hosted zone's records only to a resolver inside
one of the zone's associated VPCs. Overcast does not model "inside a VPC" as a
property of a DNS query, and the association operations are not implemented at
all — `AssociateVPCWithHostedZone`, `DisassociateVPCFromHostedZone` and
`ListHostedZonesByVPC` all return `501`.

So every container Overcast starts is answered from every private zone in the
store, with no filtering. Where a public and a private zone share a name, the
private zone wins.

This can only make **more** names resolve locally than would on AWS, never
fewer, for a container correctly associated with the zone's VPC. A private zone
may also be created without a VPC, which real AWS refuses; the VPC passed to
`CreateHostedZone` is stored and returned.

## Change propagation

| Behaviour | Overcast | AWS |
| --- | --- | --- |
| `ChangeResourceRecordSets` | Applies synchronously; status is `INSYNC` immediately | `PENDING`, then `INSYNC` |
| `GetChange` for an unknown ID | `INSYNC`, so CDK and CLI waiters converge even across a store reset | `NoSuchChange` |

## Resolver edge cases

- **Empty non-terminal names** — a label with no records of its own but with
  records below it, such as `mid.example.com` when only `leaf.mid.example.com`
  has data — are answered NXDOMAIN rather than the strictly correct NODATA. The
  resolver does not enumerate implicit non-terminals. This only affects a query
  for the exact intermediate name.
- **No answer compression** beyond the question-name pointer the split-horizon
  fast path already used. An answer with many large records could in principle
  exceed the 512-byte UDP-without-EDNS0 limit and need the TCP retry, which is
  otherwise unaffected.
- **Record types stored but not served**: `NAPTR`, `PTR`, `SRV`, `SPF`, `CAA`,
  `DS`, `TLSA`, `SSHFP`, `SVCB`, `HTTPS`. A query for one gets NODATA or
  NXDOMAIN like any absent type.

## Error messages

`InvalidChangeBatch` and `InvalidInput` message texts approximate AWS's
wording. Codes and HTTP statuses match, so error handling that switches on the
code behaves the same.

Record values are checked against their type for `A`, `AAAA`, `CNAME`, `NS`,
`PTR`, `MX`, `SRV`, `CAA`, `TXT` and `SPF`. `NAPTR`, `DS`, `TLSA`, `SSHFP`,
`SVCB` and `HTTPS` values are stored as given — only the record's shape is
checked, so AWS may reject a batch Overcast accepts.

## Related

- [Route 53](../route53.md) — quick start and DNS serving
- [Route 53 operations](./operations.md) — per-operation status
- [Networking and host-based addressing](../../networking.md)
