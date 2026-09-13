---
title: "Lambda, ECS and VPCs"
description: "A VpcConfig puts the container in that VPC and takes away everything outside it, the way AWS does. What is refused, the three ways out, and what a VPC still does not restrict."
section: "Networking"
tags:
  - docs
  - ecs
  - lambda
  - networking
  - vpc
---

# Lambda, ECS and VPCs

Giving a function a `VpcConfig` (or a task an `awsvpc` configuration) puts the
container in that VPC: it joins that VPC's Docker network, takes an address from
its CIDR, and reaches the other resources in it by name. **It also takes away
everything outside that VPC**, which is what naming a VPC means on AWS.

| | On AWS | In Overcast |
| --- | --- | --- |
| A function **with** a `VpcConfig` reaching a resource outside that VPC | ✗ no route | ✗ refused |
| A function **without** a `VpcConfig` reaching a resource inside one | ✗ no route | ✗ refused |
| Two resources in the same VPC reaching each other | ✓ | ✓ |
| A function in a VPC with no NAT gateway reaching the internet | ✗ | ✓ by default; ✗ with `OVERCAST_VPC_EGRESS=none` — see [Egress modes](./egress.md) |
| Security groups restricting any of the above | ✓ enforced | ✗ stored, never applied |
| A function in a VPC calling the AWS APIs without a NAT or VPC endpoint | ✗ | ✓ **deliberately** — see below |

## If a call just started failing

"Refused" means what it says: Overcast will not answer a name the caller cannot
reach, and the log names both sides.

```
refusing a data-plane name the caller cannot reach
  name:            mydb.us-east-1.rds.localhost.overcast.sh
  target:          rds mydb
  caller:          lambda api-handler
  target_networks: [overcast-vpc-vpc-0abc]
  caller_networks: [overcast_control overcast]
```

The same refusal is reported as the `data-plane-name-refused` advisory on
`/_overcast/health` and the console's health page, so you do not need to be
watching the log at the moment the lookup fails. From inside the application
it looks like an ordinary DNS failure — `Temporary failure in name resolution`,
`getaddrinfo` returning nothing — because the resolver answers `REFUSED`.

Your stack is describing something that would not work deployed either, so the
three ways out are AWS's own fields rather than Overcast settings — the fix that
works here is the fix that works on AWS.

| Situation | Fix |
| --- | --- |
| A function or task should be in the VPC | Give it a `VpcConfig` / `awsvpcConfiguration` naming a subnet in that VPC |
| A database should be reachable from outside its VPC | `PubliclyAccessible: true` on the instance |
| A task should be reachable from outside its VPC | `assignPublicIp: ENABLED` in its `awsvpcConfiguration` |

If none of those is what you want, the honest answer is that the two things
genuinely cannot talk on AWS, and the local failure has told you so early —
rather than as a connection that times out several minutes later with nothing to
point at.

## What a VPC does not restrict

Docker network membership expresses "in this VPC or not", and nothing finer. So
what a VPC lets *through* is not modelled:

| Not restricted | Detail |
| --- | --- |
| Overcast's own API, from inside any VPC | A container calls it for S3, SQS, DynamoDB and everything else, on the same channel as the Lambda Runtime API. Read it as "every VPC has an interface endpoint for every service" |
| Security groups and NACLs | Stored and returned, never applied. No port- or source-level filtering between two containers that share a network |
| Subnets within a VPC | One flat network per VPC, no public/private distinction. Everything in a VPC reaches everything else in it, whichever subnets they are in |
| Subnet-level internet access | Under the default `open`, a private subnet behind a NAT gateway and a fully isolated one get the same answer. Set [`OVERCAST_VPC_EGRESS=routed`](./routed-egress.md) to have each subnet's route table decide |
| Two VPCs with the same CIDR | Under the default `shared` strategy they are one Docker network, and not isolated from each other. `strict` and `remapped` give real separation — see [`OVERCAST_EC2_VPC_STRATEGY`](../services/ec2.md) |

**On a native Windows or macOS host, nothing is restricted at all.** The
restriction is only safe where a forbidden connection fails by name, and that
needs Overcast's DNS resolver, which needs `/etc/resolv.conf` to find upstream
servers. There is no such file on those hosts, so the resolver does not start,
and rather than let a forbidden connection hang with no explanation Overcast
keeps the old permissive behaviour. Run Overcast in a container — the recommended
setup — to get the restriction and the diagnostics together.

## Related

- [Egress modes](./egress.md) — whether a container in a VPC reaches the internet
- [The Docker networks Overcast uses](./docker-networks.md) — the bridge a VPC becomes
- [Networking troubleshooting](./troubleshooting.md) — the VPC migrations and the default VPC
- [Networking and host-based addressing](../networking.md) — the rest of the addressing story
