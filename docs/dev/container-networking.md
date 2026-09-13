# Container networking — how emulated compute reaches emulated services

Overcast starts real containers for several services. Two questions come up
every time a new one is added, and both already have an answer in the tree.
Reach for these before writing anything new: the last agent to need
container-to-container name resolution built a second DNS mechanism next to the
one that already worked.

**The one-line version.** Two resolvers answer inside a container Overcast
started, and they answer different questions:

| Question | Answered by | Mechanism |
| --- | --- | --- |
| Where is **Overcast**? | `internal/dns`, via `--dns` | Owns the split-horizon domains and every subdomain; answers with Overcast's own address, chosen per caller |
| Where is **that other container**? | Docker's embedded resolver, `127.0.0.11` | Network **aliases** on containers attached to the caller's network — consulted *before* anything is forwarded to `internal/dns` |
| Where does **a customer's own domain** resolve? | `internal/dns` again, but from Route 53's store, not the split-horizon zone | A hosted zone's records answer authoritatively (A/AAAA/CNAME/MX/TXT/NS/SOA, wildcards, ALIAS-to-Overcast's-address); see [route53.md](../services/route53.md#dns-serving) |

## 0. Two planes, and one package that owns them

Every container Overcast starts sits on a control plane and a data plane, and
`internal/dataplane` decides both. Nothing else should be reaching for a network
name.

"Two" is the model, not always the count. Where the DNS resolver cannot run —
a native Windows or macOS host, which has no `/etc/resolv.conf` to read
upstreams from — VPC placement is not enforced, and a VPC-attached container
joins its VPC network *and* the default plane as well as the control plane. See
§13 and `dataplane.enforceable`.

| Plane | Name | Members | Carries |
| --- | --- | --- | --- |
| **Control** | `overcast_control` (`cfg.ControlNetwork()`) | Overcast, and every container it starts | The Lambda Runtime API, and the `AWS_ENDPOINT_URL` calls function and task code makes back into the emulator |
| **Data** | `overcast` (`OVERCAST_NETWORK`), and the resource's VPC network when it named one | one per container in the target model; see below | Traffic *between* resources — a task reaching a cache node, a function reaching a database |

The split exists because those two are not the same kind of thing. On AWS the
Runtime API lives inside the execution sandbox and is reachable whatever VPC a
function joins — it is the mechanism by which the function runs at all — and
Overcast's own API stands in for a VPC endpoint per service. Both must survive
VPC placement. Reaching a database must not. One network cannot express that,
and while they shared one, "keep the Runtime API" was inseparable from "keep
reaching every database".

The API is small on purpose:

| Call | Use |
| --- | --- |
| `dataplane.Primary(cfg)` | The network to create a container on (`HostConfig.NetworkMode`). Always the control plane — Docker accepts one network at create, and this is the one that must be up from the first packet |
| `dataplane.PrimaryEndpoints(cfg)` | The same as a `NetworkingConfig` |
| `dataplane.PlaceInVPC(ctx, resolver, vpcID)` | Resolve a VPC to a `Placement`. Refuses an unlaunchable VPC rather than silently falling back |
| `dataplane.Attach(ctx, dc, cfg, id, placement)` | Join the data plane(s), advertising the placement's aliases. Call it **after create, before start** |
| `dataplane.AttachAdopted(...)` | The same for a container reused after a restart — joins the control plane too, since one adopted from an earlier version was never created there |
| `dataplane.Hostnames(cfg, name, advertised...)` | The alias set: `name` applied to every base an endpoint could be minted under, plus what the record already advertises |
| `dataplane.ContainerAddr(ctx, dc, cfg, id)` | The address *Overcast itself* dials a managed container on, or `""` meaning "use the published port on loopback" |
| `dataplane.PlaneSpecs(cfg)` | The complete desired state of both planes — isolation, driver, IPAM, options and labels. Handed to `docker.Probe`, which brings each network into exactly that state; the isolation decisions run as `InternalMode` once there is a live client. See § 1c and § 1d |
| `dataplane.VPCNetworkSpec(cfg, vpcID, subnet, owner, hasIGW)` | The same for one VPC's network, including the ownership label that stops one instance removing another's |

**Historical note, because the shape of the old bug is instructive.** There used
to be one network per emulator service — `overcast_lambda`, `overcast_rds`,
`overcast_elasticache`, and four more — partitioned by *which package called
Docker*, which is not a boundary AWS has. Everything downstream was
compensation: RDS attached its containers to two compute networks, ElastiCache
to one, MSK to none. Whether a given pair could talk came down to how completely
each service remembered to undo the partition, and #872 is what that costs.

Getting these the wrong way round is the standing trap. Overcast's resolver is
authoritative for the domains an endpoint name ends in, so it will happily
answer for `my-db.us-east-1.rds.localhost.overcast.sh` — with **Overcast's**
address. A missing alias therefore does not surface as an unknown host; it
surfaces as a connection to Overcast on port 3306 that hangs.

## 1. A container calling Overcast

`internal/containerendpoint` — resolves an address the container can dial,
rewrites loopback URLs baked into env vars, supplies `/etc/hosts` entries for
the split-horizon hostnames, and hands over the CA bundle under TLS.

Container-facing DNS for those hostnames is `internal/dns`, started by
`internal/router/container_dns.go`. It is authoritative for
`localhost.overcast.sh` (and the LocalStack/floci aliases) and forwards
everything else upstream. It answers with **Overcast's own** address, chosen per
caller by `dns.Locator` because Overcast sits on several Docker networks.

**Do not add per-name overrides here.** This resolver answers "where is
Overcast". A name that belongs to some other container is question 2.

### 1a. …and the server it is calling has to be listening there

`Resolve` is the whole answer only for a server bound to every interface, which
Overcast's own API is by default. A server that chooses its own bind addresses
needs the pair, and `containerendpoint.ResolveListen` returns it: the host
containers dial, and the local addresses that has to be listening on.

The Lambda Runtime API is the case that needs it. Nothing off this machine is a
legitimate caller — it is an unauthenticated control channel for every Lambda
container — but loopback alone strands every invocation, because the RIC dials
back over the control plane. So it binds loopback plus exactly one more address.

**Which address is measured, not inferred.** Since #1579 Overcast binds each
candidate in turn and has a throwaway busybox container connect back, keeping
the first one a container actually reaches: its own address on the control
plane, then the network's gateway, then `host.docker.internal`, then the host's
own address, then the wildcard. The verdict is remembered per daemon and control
plane in `<data dir>/runtime-api-host-<network>.json`, so only the first startup
pays for it. `LAMBDA_RUNTIME_API_HOST` pins it and skips the walk. The candidate
list, the failure modes and the `lambda-runtime-api-unreachable` advisory are in
[docs/services/lambda/troubleshooting.md](../services/lambda/troubleshooting.md);
the code is `containerendpoint.ResolveListen` and `reachability.go`.

The table below is what that replaced — and it still earns its place, because it
answers a *different* question that is asked nowhere else:

| Overcast is | Can reach a server on this host at | |
| --- | --- | --- |
| in a container | our address on the control plane | on-link |
| on a native Linux host | that network's **gateway** — host-local, and on-link from every container attached, so it survives a function joining a VPC network that takes over the default route | on-link |
| on a Docker Desktop host | the host's routable address (Desktop's networks have a gateway too, but it belongs to the daemon's VM) | beyond the bridge |

Which of the last two applies is decided by **binding the gateway**, not by
`runtime.GOOS`: "native daemon or Desktop VM" is the same question asked less
directly, and `uname -s` says `Linux` under WSL2 either way.

This table decides one thing and one thing only: whether an `--internal`
control plane would still carry the Runtime API. `dataplane.runtimeAPIReachableOnInternalPlane`
answers **yes** for the first two rows — Overcast's own address, and a native
daemon's gateway, both stay on-link on an `--internal` bridge, since only
routing *beyond* it is cut — and **no** for Desktop, where the host's routable
address sits beyond the bridge. It cannot inspect the control plane itself to
decide this, since a first run has not created it yet, so it asks the same
bindability question of Docker's always-present default `bridge` network
instead (`containerendpoint.NativeLinuxDaemon`).

**It does not decide egress, and that separation is the whole of #1564.** Until
`OVERCAST_VPC_EGRESS` existed, this probe was also what decided whether any
container could reach the internet: a fact about the host answering a question
about the deployment. Two engineers on one pinned image got different network
behaviour with nothing anywhere saying which they had. Now the mode decides
egress and this decides only whether the isolation the mode asked for can be
delivered on this host — and when it cannot, the shortfall is warned about
rather than silently applied.

### 1c. Egress is one decision for the whole topology

`OVERCAST_VPC_EGRESS` (`config.VPCEgressMode`) decides egress for the whole
topology at once: `open` leaves both planes routable, `none` makes every network
`--internal`, `routed` makes every VPC plane and the control plane `--internal`
and carries the route out on a second network per VPC. One setting rather than a
flag per network, because a container sits on two networks and takes its default
route from whichever is routable — so isolating one settles nothing.

Under `open` a VPC network is still `--internal` when its VPC has no internet
gateway. That flag stays honest about the template; it no longer decides
*egress*, because the container is also on the routable control plane and takes
its default route from there.

Under `routed` the *decision* moves to the subnet but the *carrier* stays one
network per class: `dataplane.VPCEgressNetworkSpec` describes a routable,
masquerading bridge named `config.VPCEgressNetwork(vpcID)`, pinned to a `/24`
carved from `OVERCAST_VPC_EGRESS_POOL` so it never draws on Docker's own
~31-network default address pools. `ec2.Handler.egressNetworkForSubnets` reads
the route tables at placement time; `ec2.Handler.reconcileVPCEgress` revisits
every recorded placement in a VPC when a route, an association, a NAT gateway
or a gateway attachment changes, and connects or disconnects in place. A
container on two routable networks takes its default route by
`EndpointSettings.GwPriority` (Docker 28.0 / API 1.48), which
`docker.Client.ConnectNetworkWithConfig` negotiates and drops, with a log line,
on an older daemon.

That is measured, not assumed. An end-to-end matrix over `shared`/`strict`/
`remapped` and every VPC shape (no VPC, public+IGW, private+NAT, isolated)
found identical full egress in all of them with the control plane routable:
HTTP 200 from `checkip.amazonaws.com` and 403 from real `sts.us-east-1` in
every cell, including the isolated VPC whose own network was correctly
`Internal=true`.

```
                 ┌──────────────────────────────────────┐
   Overcast ─────┤  overcast_control                    │  Runtime API and
                 │  internal only under EGRESS=none,    │  AWS_ENDPOINT_URL
                 │  and only where the probe above      │  calls back in
                 │  says the Runtime API survives it    │
                 └──────────────────────────────────────┘
                                  │
        ┌─────────────────────────┼─────────────────────────┐
        │                         │                         │
┌───────────────┐   ┌──────────────────────┐   ┌──────────────────────┐
│   overcast    │   │ overcast-vpc-A       │   │ overcast-vpc-B       │
│ default plane │   │ (IGW attached)       │   │ (no IGW)             │
└───────────────┘   └──────────────────────┘   └──────────────────────┘
        │                         │                         │
        └─────────────────────────┴─────────────────────────┘
                                  │
    EGRESS=open  → both planes routable; a VPC network is --internal
                   without an IGW, and its containers still egress via
                   the control plane
    EGRESS=none  → every one --internal
    EGRESS=routed → every plane --internal, plus one
                    overcast-vpc-<id>-egress per VPC, joined per
                    subnet route table (#1571)
```

The internet-gateway bit no longer decides a VPC network's **egress**. It still
decides its `--internal` flag under `open` (`dataplane.VPCNetworkInternal`
returns `!hasInternetGateway`), and `routed` still records it on the plane for
the readers that compute the same decision from labels alone — but reading it
*alone* delivered only the withholding half of AWS's model, in which a
private-with-NAT subnet and an isolated one are the same network. Under `none`
and `routed` the flag is `true` whatever the template says.

### 1d. Every network is verified against a full spec on every start

Docker's create-network call returns an existing network unchanged: no
isolation, no subnet, no driver options. So `docker.NetworkSpec` describes the
complete desired state — driver, `Internal`, IPv6, IPAM subnet and gateway,
driver options, labels — and `docker.EnsureNetwork` compares a live network
against it field by field, on every start, for the two planes and every per-VPC
network (`dataplane.PlaneSpecs`, `dataplane.VPCNetworkSpec`).

| Outcome | What happens |
| --- | --- |
| Absent | Created to spec |
| Matches | Left alone |
| Differs, nothing attached | Removed and recreated, logged at **WARN** — it destroyed a network, and on the first start after an upgrade it destroys one nobody labelled |
| Differs, containers attached — **a plane** | Left alone; WARN naming every differing field and every attached container, `/_overcast/health` degraded, console advisory, `overcast network reset` as the fix |
| Differs, containers attached — **a VPC network** | Recreated *under* them: each container is disconnected, the network is rebuilt, and each is reconnected with the address and aliases it had. Only connections across that VPC bridge drop; the control-plane attachment is untouched, so an in-flight invocation keeps its Runtime API (`ec2.flipDockerVPCNetworkInternal`) |
| Could not be read | The read is retried once. Still unreadable, the create runs anyway — a daemon with no network at all fails every container create with an error naming nothing about networks — and is then **verified rather than trusted**, because Docker resolves a name conflict by returning the existing network unchanged. What that verification cannot establish is reported as **unverified**: a `Drift` naming the reason, `/_overcast/health` degraded. "I did not look" is not "I looked and it was right" (#1582) |
| Owned by another instance, or by another tool | Left alone, always |

Two labels carry the rules. `overcast.network.spec-hash` is the first 12 hex
characters of the SHA-256 over the behavioural fields; **a network without one
is treated as mismatched**, because
every network created before this code has none and those are the ones that have
actually been wrong. `overcast.instance` names the instance that created the
network, and nothing removes a network carrying somebody else's — the sweep in
`ec2.reconcileNetworks` used to remove every `overcast.service=ec2` network its
own store did not claim, which on a shared daemon deleted a neighbour's live VPC
network (#1569).

The full decision record, including the measured evidence and the alternatives
rejected, is in [the container-networking egress plan](../plans/container-networking-egress.md).

### 1b. …and a container's source address is not its identity

The Runtime API is an unauthenticated control channel shared by every Lambda
container, so it has to know *which* container is calling. It used to answer
that from `r.RemoteAddr`, matched against the bridge IP `InspectContainer`
reported at registration. **That only holds while the container's packets
arrive on-link**, which is the Docker Desktop row above and nowhere else:

| Overcast is | Listener sees | Matches the registered IP |
| --- | --- | --- |
| in a container, container dials our address on the control plane | the container's bridge IP | yes |
| on a native Linux host, container dials the bridge gateway | the container's bridge IP | yes |
| on a Docker Desktop host, container dials the host's address | the host's own address | **no** |

Desktop's userspace proxy re-originates the connection, so a natively-built
Overcast on Windows or macOS matched no container and failed every invocation at
INIT — reported to the user as their function's `Runtime.InitError`.

So identity comes from **the listener the request arrived on**, not from where
it came from. Each execution environment gets a listener of its own
(`RuntimeAPIServer.AddContainerListener`, bound at port 0 across the same
`BindHosts`), is told about only that port through `OVERCAST_RUNTIME_API`, and
gives it up in `containerInstance.Close`; the count is bounded by
`LAMBDA_MAX_INSTANCES`. The source-address lookup remains as the fallback, which
is what the containerised row above still uses.

Nothing else was available when this was decided: the RIC builds its own
requests, so no header or token could be injected, and `AWS_LAMBDA_RUNTIME_API`
is parsed as a bare `host:port`, so there was no path prefix either. "Attribute
the only initialising container" is racy the moment two cold starts overlap,
which the cold-start semaphore explicitly permits.

Two variables now, and the split matters. `AWS_LAMBDA_RUNTIME_API` carries the
AWS value, `127.0.0.1:9001`, because what the runtime and the extensions talk to
is the Runtime API served by Overcast's init *inside* the container
(`internal/lambdainit`). `OVERCAST_RUNTIME_API` carries this environment's own
host endpoint, and only the init reads it. Identity is unchanged — the init
dials the per-environment listener, so the port it arrives on still names the
environment — but the init *is* a component of Overcast's, so it can add
headers where the RIC could not, which is how `X-Overcast-Log-Seq` travels back
on a forwarded response. The same listener serves the init's log stream
(`POST /overcast/v1/logs`), identified in exactly the same way.

### 1e. …and one name that points at the container, not at Overcast

`sandbox.localdomain` is the exception to everything above: it is a Lambda
execution environment's name for **itself**, and it resolves to `127.0.0.1`
inside a real one. AWS documents every telemetry destination under it — the
Telemetry API and the Logs API both describe the delivery endpoint as "a local
HTTP endpoint (`http://sandbox.localdomain:${PORT}/${PATH}`)" — so an extension
ported from AWS subscribes that name verbatim, and the POST is made from inside
the sandbox by the in-container init.

The entry is added by `ContainerRuntime.extraHosts`
(`internal/services/lambda/container_runtime.go`), on top of what
`containerendpoint.Mapper.ExtraHosts` returns, and deliberately not inside that
method: the mapper is shared with ECS's namespace container and answers "where
is Overcast", where this name is Lambda-only and points the other way. Neither
resolver in the table at the top of this page is involved — a `--add-host`
entry is read out of `/etc/hosts` before either is asked (#1837).

## 2. A container calling another container

This is Docker's embedded resolver (`127.0.0.11`), not Overcast's. Every
container on a user-defined network gets it, and it resolves **network aliases**
from the containers attached to that network *before* forwarding anything
upstream to Overcast. So the way to make `my-db.us-east-1.rds.localhost.overcast.sh`
reach the database is to attach the database container to the caller's network
carrying that name as an alias.

The utilities:

| Helper | Use |
| --- | --- |
| `docker.EndpointAliases(addrs...)` | Filters a set of endpoint addresses down to unique, non-IP hostnames usable as aliases |
| `containerendpoint.ResourceHostnames(cfg)` | Every base a resource name can be minted under — the split-horizon set plus `localhost`. Build one alias per entry |
| `docker.Client.ConnectNetworkWithAliases(ctx, network, container, aliases)` | Attaches an existing container to a network, advertising those aliases |
| `docker.Client.ConnectNetwork(ctx, network, container)` | The same with no aliases — reachable by IP, **not resolvable by name** |
| `NetworkingConfig.EndpointsConfig[net] = {Aliases: …}` | The same at container-create time |

The established shape, as every container-backed service now uses it:

```go
// aliases: every hostname the API could hand a caller for this resource.
func (h *Handler) instanceEndpointAliases(region string, inst *Instance) []string {
    return dataplane.Hostnames(h.cfg, func(base string) string {
        return instanceEndpointHostname(inst.ID, region, base)
    }, advertised...)
}

// Create on the control plane, then join the one data plane this resource
// belongs on — its VPC's network, or the default one.
placement, err := dataplane.PlaceInVPC(ctx, h.vpcResolver, inst.VpcID)
placement.Aliases = aliases
err = dataplane.Attach(ctx, h.docker, h.cfg, containerID, placement)
```

Note the **set**, not the one name. Endpoint names are minted on the hostname
the caller reached Overcast on (`docs/networking/data-plane-endpoints.md`), so
the same instance is `db.us-east-1.rds.localhost.overcast.sh` to one caller and
`db.us-east-1.rds.localhost` to another. RDS and ElastiCache each have an
`endpoint.go` doing exactly that, and a `DialAddress`/`DialPort` pair on the
record for the address *Overcast* uses to health-check the container. Keep
them apart: the record's endpoint is never overwritten with the dial target,
because an address is dialable by one party only, and `127.0.0.1` inside a
sibling container is the sibling. Docker aliases are exact-match: a name
that was not registered does not resolve — and under a split-horizon domain it
is worse than that, because the query then reaches Overcast's own resolver,
which answers *any* subdomain of those domains with Overcast's address. The
caller connects to Overcast on 3306 and hangs. Register every name you can mint.

Three more things worth knowing:

- **Attach after create, before start.** A container that starts before it has
  joined its data plane can race its own first outbound connection — function
  code that opens a database connection during INIT runs before the name it
  dials resolves. The one exception is ECS's `awsvpc` attachment, which reads
  back the address Docker assigned and therefore cannot run until the container
  does; the same task joins the default plane first, if it is entitled to one.
- **An ECS task is attached once, not once per container.** Every container in
  an `awsvpc` task shares one network namespace, held open by a container of its
  own (`internal/services/ecs/task_netns.go`) the way the ECS agent's
  `~internal~ecs~pause` does. That container is the one attached to the planes
  and the one carrying the task's ENI; the application containers are created
  with `NetworkMode: container:<id>` and inherit all of it, which is what makes
  `127.0.0.1` reach across a task as it does on AWS. Docker rejects a container
  in that mode that declares ports, resolvers, hosts entries or network
  endpoints of its own, so all four belong to the namespace container.
- **A VPC-placed resource gets its VPC network and nothing else.** That is the
  restriction, and `dataplane.DataNetworks` is where it lives. Two things widen
  it back: `Placement.Public`, set from AWS's own `PubliclyAccessible` and
  `assignPublicIp`; and a deployment where Overcast's resolver is not running,
  since the restriction is only safe where a forbidden connection fails by name
  rather than hanging.
- **So a service must pass a VPC when the resource has one.** Before enforcement
  a service that never called `PlaceInVPC` merely lost some fidelity; now it
  strands the resource on the default plane where its own consumers cannot see
  it. If you are adding a container-backed service, resolving its VPC is not
  optional.
- **A VPC attachment needs aliases too**, which is why they live on `Placement`
  rather than being passed only on the default path. Attaching without them
  leaves the container reachable by IP but unresolvable by name — so the caller
  falls through to Overcast's resolver and connects to a port nothing is
  listening on.
- **Overcast attaches itself** to the control plane, and for VPC work to the VPC
  networks, which is why the fallback answer is reachable at all.
- **A refused name is reported twice.** `dataplane.Guard` logs it at WARN and
  keeps the last few (`Guard.Recent`) for the `data-plane-name-refused`
  advisory on `/_overcast/health`, which names both containers and their
  networks. From inside the application the same event is only ever
  `Temporary failure in name resolution`, so the advisory is what makes a
  misplacement diagnosable without reading Overcast's log at the right moment.
- **A subnet group that does not exist is refused at create**, for RDS and
  ElastiCache alike (`DBSubnetGroupNotFoundFault`,
  `CacheSubnetGroupNotFoundFault`). Accepting it silently put the resource on
  the default plane and turned a typo into the failure above.

## 3. VPC networks

`internal/services/ec2` owns one Docker network per VPC and exposes
`VpcIDForSubnet`, `VPCNetworkStatus`, `DockerNetworkForVpc` and
`AvailabilityZoneForSubnet` to other services (wired in
`internal/router/router.go`). A VPC whose network could not be created reports
status `unbacked`, and services refuse to place into it rather than starting
something unreachable.

The common cause of `unbacked` is a CIDR collision with a leftover
`overcast-vpc-*` network from an earlier run — Docker refuses the pool overlap.
`docker network ls | grep overcast-vpc-` and remove the stale ones.
