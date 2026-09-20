---
title: "Route 53 — Amazon Route 53"
description: "Quick start, the record types the built-in resolver answers, and the routing, health-check and VPC-scoping behaviour that is stored but never acted on."
section: "Service Reference"
tags:
  - amazon
  - dns
  - docs
  - route
  - route53
  - services
---

# Route 53 — Amazon Route 53

Hosted zone records really answer queries: Overcast's own resolver is
authoritative for every zone in the store. Routing policies are stored but not
evaluated, and health checks never probe anything.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566
ZONE=$(aws route53 create-hosted-zone --name example.internal --caller-reference "$RANDOM" \
  --query 'HostedZone.Id' --output text)

aws route53 change-resource-record-sets --hosted-zone-id "$ZONE" --change-batch '{
  "Changes":[{"Action":"CREATE","ResourceRecordSet":{
    "Name":"api.example.internal","Type":"A","TTL":60,
    "ResourceRecords":[{"Value":"10.0.0.9"}]}}]}'

dig +short @localhost api.example.internal
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

The resolver runs on port 53 by default (`OVERCAST_DNS`, `OVERCAST_DNS_PORT`).
Failing to bind it is not fatal, so on a machine where something already owns
port 53 the API still works and only DNS answers are missing.

## DNS serving

A query whose name falls under a hosted zone — the apex or any subdomain — is
answered from that zone's record sets.

| Aspect | Behaviour |
| --- | --- |
| Types served | `A`, `AAAA`, `CNAME`, `MX`, `TXT`, `NS`, `SOA`, including the apex `NS`/`SOA` every zone gets on creation |
| `CNAME` | Chased across zones, up to 8 hops; a cycle terminates the chain rather than looping |
| Wildcards | One label, as AWS scopes them: `*.example.com` answers `foo.example.com` but not `foo.bar.example.com` |
| TTL | The record's own stored TTL, not the split-horizon default |
| `ALIAS` | Resolves to Overcast's own address — the one the calling container or host can reach — since that is the only correct answer for an alias pointed at an emulated ELB, CloudFront, S3 website or API Gateway hostname |
| Negative answers | A name in the zone with no record of that type is NODATA; a name absent from the zone is NXDOMAIN. Both carry the zone's SOA for negative caching |

Types that can be stored but are not served — a query gets NODATA or NXDOMAIN
like any absent type: `NAPTR`, `PTR`, `SRV`, `SPF`, `CAA`, `DS`, `TLSA`,
`SSHFP`, `SVCB`, `HTTPS`. An `AAAA`-typed alias answers NODATA, because
Overcast has no IPv6 address to give.

A hosted zone always wins over Overcast's split-horizon names: a name some zone
claims is answered from that zone, authoritatively, and never forwarded. Only a
name no zone claims falls through to the split-horizon and forwarding
behaviour. A container endpoint — an RDS endpoint, an ElastiCache node — is
resolved by Docker's embedded resolver from the container's network aliases
before either authority is reached, so it is unaffected either way.

## What works

| Area | Behaviour |
| --- | --- |
| Hosted zones | `CreateHostedZone` requires a `CallerReference` and rejects a reused one with `HostedZoneAlreadyExists`. Each zone gets default apex `NS` and `SOA` records and a four-server delegation set derived from the zone ID, and the new zone's URL comes back in `Location` |
| Record changes | `ChangeResourceRecordSets` validates the whole batch atomically before applying anything — `InvalidChangeBatch` for creating an existing record, deleting a missing one or one whose values do not match, records outside the zone, a CNAME at the apex, deleting the default apex records, and a value that does not fit its record type |
| Listing | `ListResourceRecordSets` returns records in DNS order (names compared with labels reversed) and paginates on `name`/`type`/`identifier`/`maxitems` |
| Deletion | `DeleteHostedZone` returns `HostedZoneNotEmpty` while non-default records exist; a successful delete cascades the default records and the zone's tags |
| Identifiers | Zone and change IDs are stored and returned in AWS's path format — `/hostedzone/Z123…`, `/change/C123…` |
| Errors | Route 53's `ErrorResponse` envelope (`Type`/`Code`/`Message`), not S3's bare `<Error>` shape |
| Health checks | Configuration is stored with AWS's defaults and versioned by `UpdateHealthCheck` |

Route 53 is a global service here, and domain and record names are
canonicalised to lowercase with a trailing dot.

## Differences from AWS

| Area               | On AWS                                                                  | Overcast                                                                                                                                                                |
| ------------------ | ----------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Routing policies   | Weighted, latency, failover, geolocation and multivalue pick one answer | The metadata is stored and returned but never evaluated — several record sets sharing a name and type are merged into one answer                                        |
| Health checks      | Endpoints are probed                                                    | None is ever probed; `GetHealthCheckStatus` and `GetHealthCheckLastFailureReason` return `501`                                                                          |
| Private zones      | Resolvable only from the associated VPCs                                | Served to every container Overcast starts, with no VPC filtering; `AssociateVPCWithHostedZone`, `DisassociateVPCFromHostedZone` and `ListHostedZonesByVPC` return `501` |
| Change propagation | `PENDING`, then `INSYNC`                                                | `ChangeResourceRecordSets` applies synchronously, so a change is `INSYNC` immediately                                                                                   |
| Traffic policies   | Full API                                                                | `CreateTrafficPolicy` returns `501`                                                                                                                                     |

The full list is in [Route 53 limitations](./route53/limitations.md).

## Gotchas

> [!NOTE]
> Overcast never becomes an internet-reachable authority for a public zone's
> real domain. There is no NS delegation and nothing published to the internet —
> the resolver answers the containers Overcast starts, and a host that has
> explicitly pointed its own resolution here.

<!-- BEGIN overcast:capabilities -->

## Operations

19 of 25 listed operations are implemented.
Per-operation status, notes and AWS API links: [Route 53 operations](route53/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Route 53 limitations](./route53/limitations.md) — the full divergence list
- [All service pages](./README.md)
- [Networking and host-based addressing](../networking.md)
- [AWS API reference](https://docs.aws.amazon.com/Route53/latest/APIReference/Welcome.html)
