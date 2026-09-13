---
title: "Networking troubleshooting"
description: "Symptom, cause and fix for names that will not resolve, ports that differ by caller, refused data-plane names, exhausted address pools, and the two VPC-era migrations."
section: "Networking"
tags:
  - docs
  - networking
  - troubleshooting
  - vpc
---

# Networking troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| A host-routed URL will not resolve from your shell | DNS rebinding protection, or `*.localhost` on Windows | [Hostnames that resolve for every caller](./hostnames.md) |
| A URL works from your shell but not inside a container | `OVERCAST_HOSTNAME=localhost` — inside a container that is the container | Set a wildcard-DNS name or the Compose service name — [Hostnames](./hostnames.md) |
| A bucket with a dot in its name reaches API Gateway or AppSync | The name carries a reserved service label | Use path-style, or the explicit `.s3.` form — [Host-routed addressing](./host-routing.md#how-overcast-decides-who-owns-a-host) |
| The same stack output shows `:4652` on the host and `:4566` in a function | Each caller gets the port it can dial | Nothing. Both are correct — [What host and port a URL carries](./urls.md) |
| A token minted on the host fails validation inside a container | A remapped port splits the OIDC issuer | Publish the API 1:1 (`-p 4566:4566`) — [URLs](./urls.md) |
| An RDS or ElastiCache endpoint refuses connections | The port belongs to the other caller, or the engine container is gone | [Data-plane endpoints](./data-plane-endpoints.md), then [below](#an-endpoint-name-resolves-nowhere) |
| `refusing a data-plane name the caller cannot reach`, or the `data-plane-name-refused` advisory | The two resources are not in the same VPC, and would not be on AWS either | [Lambda, ECS and VPCs](./vpcs.md) |
| `Temporary failure in name resolution` for a cache or database endpoint, from inside a task or function only | The same refusal, seen from the application: the resource named no subnet group and is in the default VPC, or a different one | Give the cache or instance a subnet group in the caller's VPC — [Lambda, ECS and VPCs](./vpcs.md) |
| Outbound connections fail with `ENETUNREACH` | `OVERCAST_VPC_EGRESS=none`, or `routed` with no `0.0.0.0/0` route on the subnet | [A function in a VPC fails with `ENETUNREACH`](#a-function-in-a-vpc-fails-with-enetunreach), below |
| The `vpc-egress-not-withheld` advisory, or two egress warnings at startup | This host cannot withhold egress with Overcast running outside a container | Run Overcast in a container, or against a native Linux daemon — [Egress modes](./egress.md) |
| `OVERCAST_VPC_EGRESS_POOL … has no free /24 left` | Every VPC with egress takes one `/24`, and 256 fit in the default | Delete VPCs, or widen the pool — [The address-pool ceiling](./routed-egress.md#the-address-pool-ceiling) |
| `all predefined address pools have been fully subnetted` | Docker's own default pools, which every tool on the machine shares | Remove unused Docker networks, or widen Docker's `default-address-pools` |
| `Docker network is not in the state this configuration asks for` | A reused network differs from what this configuration would create | `overcast network reset --dry-run`, then reset — [Network state verification](./network-state.md) |

## A function in a VPC fails with `ENETUNREACH`

**Symptom.** A Lambda or ECS task with a `VpcConfig` cannot reach an external
API, a real AWS endpoint, or anything else outside Docker. Code that works
without a VPC — and works on LocalStack — fails here, usually as
`ENETUNREACH`, sometimes as a DNS failure because the resolver is unreachable too.

**Cause.** Almost always one of two things.

| | |
| --- | --- |
| `OVERCAST_VPC_EGRESS=none` | That is the mode working: no container Overcast starts reaches anything outside this machine. On Docker Desktop the control plane stays routable, so containers keep a route out and a startup warning says so — see [Egress modes](./egress.md) |
| `OVERCAST_VPC_EGRESS=routed` and the container is in a subnet with no `0.0.0.0/0` route | That is the mode working too — the missing NAT gateway, caught locally. `overcast logs` names the subnet and route table that decided it. Add a NAT gateway and a route to grant egress; containers placed afterwards get it, and running ones are moved onto it. On the hosts where `none` cannot isolate the control plane, `routed` cannot withhold either, and reports `vpc-egress-not-withheld`. See [`routed`](./routed-egress.md) |
| A network drifted | A network Overcast reuses kept a setting from an older version or a different mode, because Docker never applies `--internal` to an existing network. Overcast repairs one with nothing attached and warns about one with containers on it |

A container with a `VpcConfig` joins two networks — its VPC's network and the
control plane — so if both are `--internal`, Docker installs no default route
and it has no way out. `none` makes both internal; drift can leave one that
way. (Under `routed` there is a third, the VPC's egress network, and only the
containers whose subnet grants a route out join it.)

**Check which.** The startup log and `GET /_overcast/health` both say what each
network ended up as, and why:

```
network isolation  network=overcast_control internal=true
                   reason="OVERCAST_VPC_EGRESS=none"
```

```sh
overcast network status
```

A network reported as `NOT in the configured state` is drift; one reported `ok`
with `internal=true` under `OVERCAST_VPC_EGRESS=none` is the mode.

**Fixes:**

| | |
| --- | --- |
| Restore egress | Unset `OVERCAST_VPC_EGRESS`, or set it to `open`, and restart. `open` is the default |
| The network kept an old setting | `overcast network reset --dry-run` to see what it would do, then `overcast network reset`. It stops Overcast's own containers, disconnects yours, and rebuilds the network to spec |
| You want a hermetic stack | Then `ENETUNREACH` is the correct answer. Keep `none`, and check the startup log: on Docker Desktop the control plane stays routable — see [Egress modes](./egress.md) |
| The function does not need the VPC locally | Drop the `VpcConfig` for the local stage. Under `none` even a non-VPC function has no egress either |

**Reaching real AWS from a local function** — a hybrid stack whose code calls a
real regional endpoint or a third-party API — works under the default `open`
mode with no extra configuration. Overcast injects `AWS_ENDPOINT_URL` into every
container it starts, so an SDK client picks Overcast up by default; construct
the one client that should talk to real AWS with an explicit endpoint (or none)
and real credentials, and leave the rest pointing at the emulator. The variables, the
rules and a worked client are in
[Egress modes § Reaching real AWS from a container](./egress.md#reaching-real-aws-from-a-container).

## An endpoint name resolves nowhere

The Docker network alias exists only while the engine container does.

```sh
docker ps --filter name=overcast-rds-
```

If it is missing, the instance is `available` as metadata with nothing behind it.
An `EngineVersion` Overcast does not advertise is no longer a cause — the nearest
image family is used and the substitution is logged — so a missing container
means Docker was unavailable or the image could not be pulled.

## This used to work with `LAMBDA_NETWORK` set

Overcast used to create one Docker network per emulator service:
`overcast_lambda`, `overcast_ecs`, `overcast_rds`, `overcast_elasticache`,
`overcast_msk`, `overcast_eks` and `overcast_efs`. That partition is gone, and so
are the seven environment variables that named those networks. It was the reason
a cache node could be reachable from a Lambda function and not from an ECS task
([#872](https://github.com/overcast-sh/overcast/issues/872)) — whether any two
things could talk depended on which service happened to bridge the gap.

| If | Then |
| --- | --- |
| You set one of the old variables | They are no longer read. Set `OVERCAST_NETWORK` instead — one value, for the one network |
| Your compose file joins `overcast_lambda` (or another of the seven) | Join `overcast` instead |
| You have leftover `overcast_*` networks | Overcast removes them at startup once nothing is attached. One that survives still has a container on it — `docker network inspect overcast_lambda` names it |

## `DescribeVpcs` returns a VPC I did not create

Each region seeds a default VPC on first use, as every real AWS account has. It
is marked `isDefault`, uses AWS's own `172.31.0.0/16`, and is what
`Vpc.fromLookup(isDefault: true)` adopts.

`DescribeVpcs` honours `VpcId.N` and the `vpc-id` and `isDefault` filters, so a
lookup that names what it wants gets one VPC back. If you were relying on an
unfiltered list containing exactly your own VPCs, filter it.

You can delete it (`DeleteVpc`), as on AWS, and Overcast will not seed another —
also as on AWS, where `CreateDefaultVpc` is the way back. Its backing network is
the shared data plane, so the delete removes the record and leaves the network
every running container is attached to.

## Related

- [Troubleshooting](../troubleshooting.md) — the whole-emulator symptom index
- [The Docker networks Overcast uses](./docker-networks.md) — what each network carries
- [Networking and host-based addressing](../networking.md) — the rest of the addressing story
