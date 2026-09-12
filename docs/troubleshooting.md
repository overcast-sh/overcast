---
title: "Troubleshooting"
description: "Start here when something is wrong: a symptom-to-guide index, then every startup warning and runtime advisory Overcast raises, with what each one means and the fix."
section: "Troubleshooting"
tags:
  - docs
  - troubleshooting
  - preflight
  - warnings
  - debugging
---

# Troubleshooting

Find the symptom, follow the link. If nothing matches, start the daemon with
`OVERCAST_DEBUG=true` and read the request's full trace in the web console or at
[`/_overcast/debug/trace/{requestId}`](./debug-endpoints.md).

| Symptom | Where the answer is |
| --- | --- |
| Overcast logged a `WARN` at startup | [Startup warnings](#startup-warnings), below |
| A resource I created is gone after a restart | [Storage and persistence](./storage.md) — and [Builds without SQLite](./storage.md#builds-without-sqlite) if you run the slim image or `overcastd` |
| A hostname will not resolve, or works from the shell but not a container | [Networking](./networking/hostnames.md#in-docker-compose) |
| A Lambda or task cannot reach a database | [Lambda, ECS and VPCs](./networking/vpcs.md) |
| A function cannot reach the internet or real AWS (`ENETUNREACH`) | [A function in a VPC fails with `ENETUNREACH`](./networking/troubleshooting.md#a-function-in-a-vpc-fails-with-enetunreach) |
| `cdk deploy` fails, or a stack sits in `CREATE_IN_PROGRESS` | [CDK troubleshooting](./cdk/troubleshooting.md) |
| CDK reports no private subnet groups, or stale VPC IDs | [Importing a VPC into CDK § Troubleshooting](./cdk/vpc-lookups.md#troubleshooting) |
| A browser will not trust the certificate | [HTTPS and HTTP/2](./https.md) |
| The console freezes while Lambdas run | [HTTPS and HTTP/2](./https.md) — it is the browser's 6-connection limit |
| My file edits are not reaching a container | [The inner loop § When it does not work](./local-dev.md#when-it-does-not-work) |
| My breakpoints never hit inside a function or task | [Step debugging § When it does not work](./debugger.md#when-it-does-not-work) |
| The console's **Debug in console** button is missing or disabled, or the session keeps waiting for a container | [Debugging in the console § When it does not work](./debugger-console.md#when-it-does-not-work) |
| It all feels slow | [Performance](./performance.md) — usually the client, not the emulator |
| Something that worked on LocalStack does not here | [Migrating from LocalStack § Troubleshooting](./migration-from-localstack.md#troubleshooting) |
| An operation returns `501 Not Implemented` | The service's page under [Reference index § Services](./README.md#services) — it is not emulated yet |

## Startup warnings

A handful of environment mistakes cost real time because they do not look like
environment mistakes — Overcast answers normally, and the symptom (an empty
console list, a container that never starts, data that is not where you left it)
reads exactly like a bug in the emulator. Where Overcast can tell, it says so:
one actionable `WARN`, the moment the symptom appears, never on a healthy setup.

| Message names… | Means |
| --- | --- |
| `Docker is not reachable for: ...` | Container-backed services (ECS, RDS, Lambda, MSK, …) found no Docker daemon and will run metadata-only: creates succeed, nothing starts. Start Docker, or check the socket is readable by this user. |
| `the API is published on a different host port (N) than it listens on (N)` | The container remapped its port (`-p 4580:4566`). Overcast rewrites the common cases already; publish 1:1 if something still compares the port literally, such as a Cognito token's `iss`. |
| `a request arrived addressed to "..." — a real AWS hostname` | A hosts-file entry, DNS override, or proxy is sending `*.amazonaws.com` traffic here. Point `AWS_ENDPOINT_URL` at Overcast explicitly, or remove the redirect. |
| `OVERCAST_HOSTNAME=... does not resolve` | This host's resolver cannot resolve `OVERCAST_HOSTNAME` or its subdomains, which breaks virtual-hosted S3 and `cdk deploy` asset publishing. The message names the fix for this host; see [Hostnames that resolve for every caller](./networking/hostnames.md). |
| `this run is memory-only, but an existing Overcast database was found` | `OVERCAST_STATE=memory` — explicitly, or a build without SQLite resolving `auto` to memory — is ignoring a database that already holds data. Set `auto`, `hybrid` or `wal` to use it. |
| `control plane network: internal=true while OVERCAST_VPC_EGRESS is not none` | The deprecated `OVERCAST_CONTROL_PLANE_INTERNAL=true` isolated one network while the mode says egress is open, so containers lose their route out anyway. Set `OVERCAST_VPC_EGRESS=none` if that is what you meant, or unset the deprecated variable. |
| `OVERCAST_VPC_EGRESS=none asked for an isolated control plane, but ...` | This host cannot isolate the control plane, so it was left routable and the stack is not hermetic. Run Overcast in a container, or against a native Linux daemon — see [Egress modes](./networking/egress.md). |
| `OVERCAST_VPC_EGRESS=routed asked for an isolated control plane, but ...`, or `... decides egress from each subnet's route table, but ...` | The same host limit, so a subnet with no default route still reaches the internet. Run Overcast in a container, or against a native Linux daemon — see [`routed`](./networking/routed-egress.md). |
| `OVERCAST_VPC_EGRESS_POOL ... has no free /24 left for another VPC's egress network` | Under `routed`, every VPC with egress takes one `/24` from the pool, and 256 of them fit in the `198.18.0.0/16` default. Delete VPCs that no longer need one, or set a wider `OVERCAST_VPC_EGRESS_POOL`. See [The address-pool ceiling](./networking/routed-egress.md#the-address-pool-ceiling). |
| `Docker network is not in the state this configuration asks for` | A network Overcast reuses differs from what this configuration would create, and it has containers on it, could not be read, or belongs to somebody else — so Overcast left it alone. `overcast network reset --dry-run`, then `overcast network reset` — see [Network state verification](./networking/network-state.md). |

A port Overcast wants that is already taken is not on this list because it needs
no diagnosis: startup fails immediately with the OS's own
`bind: address already in use` rather than falling back silently.

## Runtime advisories

Some conditions only surface once Overcast is running. Those are raised as
advisories, on the web console's **Metrics & Health** page and in the
`advisories` array of `GET /_overcast/debug/metrics`:

| Advisory | Means |
| --- | --- |
| `Running in memory-only mode` | Nothing is mounted and no `OVERCAST_DATA_DIR` is set, so state will not survive a restart. Expected outside a persistent setup — see [Storage and persistence](./storage.md). |
| `Data directory filesystem is slow` | An fsync probe of the data dir took over 75 ms, the signature of a Docker Desktop bind mount. See [Storage tuning § Data dir placement](./performance/storage-tuning.md#data-dir-placement--avoid-host-bind-mounts-on-docker-desktop). |
| `Storage degraded to memory-only` | The SQLite file became unreadable; `hybrid` keeps serving from memory for the rest of the run rather than crashing. |
| `Memory mode is ignoring an existing database` | The runtime form of the startup warning above. |
| `Docker network is not in its configured state` | A network Overcast reuses differs from what this configuration would create — usually one made by an older version, or with a different `OVERCAST_VPC_EGRESS`. It has containers on it, could not be read, or belongs to somebody else, so Overcast left it alone. The advisory names the command that fixes it — see [Network state verification](./networking/network-state.md). |
| `A VPC's network does not match its internet gateway or route tables` | Overcast could not bring a VPC's Docker network to the isolation its gateway state calls for, or — under `routed` — could not move a container onto or off its VPC's egress network. Containers in that VPC have the reachability of the state *before* the one `DescribeInternetGateways` and `DescribeRouteTables` report. It retries at the next restart — see [VPC backing § Internet gateways and isolation](./networking/vpc-backing.md#internet-gateways-and-isolation). With more than one VPC affected the title counts them: `2 VPC networks do not match their internet gateways or route tables`. |
| `OVERCAST_VPC_EGRESS=none cannot withhold egress on this host` | The control plane had to stay routable, so the stack is not hermetic — see [Egress modes](./networking/egress.md). Expected on Docker Desktop with Overcast on the host. The title names whichever mode you set; under `routed` the detail also covers VPC placement (below). |
| `OVERCAST_VPC_EGRESS=routed cannot withhold egress on this host` | The same control-plane limit, plus one only `routed` has: where Overcast's DNS resolver cannot start, a VPC-placed container also joins the routable shared plane, so its subnet's route table decides nothing — see [`routed`](./networking/routed-egress.md). |
| `No Lambda can run: containers cannot reach the Runtime API` | No candidate address a Lambda container could dial answered, so every invocation will fail during INIT. Usually a host firewall blocking a freshly built binary — see [Containers cannot reach the Runtime API](./services/lambda/troubleshooting.md#containers-cannot-reach-the-runtime-api). |
| `A Lambda init volume belongs to another instance` | Informational. Another Overcast on this daemon created the volume holding this build's init; it is safe to keep mounting, but only that instance will ever prune it — see [Lambda execution environments](./services/lambda/execution-environments.md#init-delivery-is-shared-across-instances). |

One more advisory is served on demand rather than raised: `GET /_overcast/preflight/region`
answers whether resources of a given `?kind=` exist in some region other than the
caller's. An empty console list with `There are N in <region>` behind it is an
`AWS_REGION` mismatch, not missing data.

## Related

- [Networking troubleshooting](./networking/troubleshooting.md) — names, ports, egress and VPCs
- [Debug endpoints](./debug-endpoints.md) — request traces, metrics and the state dump
- [Storage and persistence](./storage.md) — what survives a restart
