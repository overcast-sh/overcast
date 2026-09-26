# Networking in Overcast

> Why Overcast's networking is shaped the way it is: what binds where, how the
> addresses in its responses are chosen, and what its DNS server does that
> Docker's cannot.
>
> This page is reasoning, not reference. The container side —
> the two planes, the Runtime API, aliasing one container's name into another's
> resolver — is [container-networking.md](./container-networking.md). The
> behaviour a user sees is [docs/networking/](../networking.md).
> [architecture.md](./architecture.md) §7 covers both at summary level.

---

## 1. The problem AWS does not have

On real AWS every hostname in every response is publicly routable. `sqs.us-east-1.amazonaws.com` resolves the same way from your laptop, from a Lambda function and from an ECS task, and reaches the same place. Whether the caller can reach an address is never a question, so nothing in the AWS API has to think about it.

Locally it is the whole problem, and it has two halves. **The same name must work from several places**: a queue URL handed to your test suite has to be dialable from your machine, and the same URL handed to a Lambda function has to be dialable from inside a container, where `localhost` means that container. **And the same resource is not at the same address for everyone**: a database Overcast started is reachable from your machine on a published port, and from a sibling container on the engine's own port over a different network path. Both are correct; neither works from the other place.

Everything below follows from those two facts — including the cast, which is four callers rather than two:

| Caller | Reaches Overcast how | Where `localhost` points |
|---|---|---|
| **Your machine** — SDK, CLI, IaC tool | the published port | your machine |
| **A container Overcast started** — Lambda, ECS | an injected endpoint name, over a Docker network | that container |
| **Another container** — a database, a cache | not Overcast at all; a Docker network alias | itself |
| **The Docker daemon** — pulling an ECR image | a published port it can dial itself | the daemon's host, which under Docker Desktop is a VM |

The fourth catches people out, and it is why ECR gets [its own section](#10-ecr-and-the-fourth-caller).

---

## 2. What binds where

| Listener | Default port | Config | Honours `OVERCAST_LISTEN`? |
|---|---|---|---|
| AWS API | 4566 | `OVERCAST_PORT` | **Yes** — every address in the list |
| Web console / BFF | 4567 | `OVERCAST_UI_PORT`, `--ui-port`; `0` disables | **Partly** — first address only |
| Lambda Runtime API | 9001 | `LAMBDA_RUNTIME_API_PORT`; `0` = ephemeral; a taken default falls back to ephemeral | **No** — resolved independently |
| SMTP capture | 1025 | `OVERCAST_SMTP_PORT`; `0` = ephemeral; a taken default falls back to ephemeral | **No** — hardcoded loopback |
| Container DNS (UDP + TCP) | 53 | `OVERCAST_DNS_PORT`, `OVERCAST_DNS` | **No** — binds the wildcard |
| Bridge proxy (opt-in) | 80 | `--http-port`, `--bridge` | **No** — binds the wildcard |

`OVERCAST_LISTEN` takes a comma-separated list, with blanks dropped and duplicates collapsed. Four rules are worth the words:

- **Set-to-nothing is an error, not a default.** Unset means "use the default"; set to nothing means the value is wrong, and defaulting it would bind every interface — the opposite of what anyone setting it wants.
- **A wildcard mixed with a specific address is refused.** `127.0.0.1,0.0.0.0` reads as a widening when it is the opposite of what the specific address asked for; on Linux the second bind fails anyway, as an opaque listen error rather than as the configuration mistake it is. `::` counts as a wildcard here too.
- **A partial bind holds no ports.** If a later address fails, everything already opened is closed before the error returns.
- **The default depends on where Overcast is running** ([#761](https://github.com/overcast-sh/overcast/issues/761)): `0.0.0.0` containerised, which Docker's `-p` publishing requires, and `127.0.0.1` natively. Containerised is detected from the `OVERCAST_DATA_DIR_SOURCE=image` marker the Dockerfile bakes in, the same way storage-mode auto-detection tells the image apart. An explicit value always wins, in both directions, and the startup log names which default fired.

The list exists for one shape in particular: an instance that wants loopback and nowhere routable, whose test suite runs in a sibling container reaching the host over the Docker bridge. `OVERCAST_LISTEN=127.0.0.1,172.17.0.1` serves both, and the compat harness does exactly this.

Three listeners diverge from the list, each for a reason.

- **The web console binds the first address only.** `OVERCAST_LISTEN=127.0.0.1` is how an unauthenticated emulator is kept off the network the machine is attached to, and a console that ignored it would leave the state browser — and its write actions — reachable from that network anyway. The extra addresses exist so container-side clients can reach the API; the console is a browser surface on this machine.
- **SMTP is loopback-only regardless**, so an SMTP client in a sibling container cannot reach it. Undeclared rather than deliberate — see [§13](#13-things-that-will-bite-you).
- **The Lambda Runtime API binds loopback plus exactly one address a container can reach**, and *reach* is measured rather than inferred. The candidate order, the cached verdict, `LAMBDA_RUNTIME_API_HOST`, and why the listener a request arrives on is the container's identity are [container-networking.md §1a and §1b](./container-networking.md).

---

## 3. Bind address versus advertised name

Two separate concerns, as in any server reachable by a name it does not bind — nginx has `listen` and `server_name`; Kafka has `listeners` and `advertised.listeners`.

| | Binds | Advertises |
|---|---|---|
| Overcast | `OVERCAST_LISTEN` | `OVERCAST_HOSTNAME` |
| LocalStack | `GATEWAY_LISTEN` | `LOCALSTACK_HOST` |
| Floci | — (`FLOCI_PORT` only) | `FLOCI_HOSTNAME` |

`OVERCAST_LISTEN` follows LocalStack's `GATEWAY_LISTEN` idiom directly, which is what closed the naming collision that used to live here ([#870](https://github.com/overcast-sh/overcast/issues/870)). The pre-rename `OVERCAST_HOST` was removed rather than kept as an alias: the policy for a removed setting is a startup error naming the replacement, not a silently-honoured leftover.

`OVERCAST_HOSTNAME` is the name that goes inside URLs Overcast hands back, and it is additive to the built-in wildcard bases rather than replacing them. It also becomes a virtual-hosted base for [host classification](#4-which-service-owns-a-request), an `/etc/hosts` entry inside every container Overcast starts, and a TLS SAN.

Setting it to `localhost` silently breaks container callers, as [urls.md](../networking/urls.md) warns. The half-defence is worth knowing when reading the code: loopback names are dropped from the hostname set, so the setting produces neither an `/etc/hosts` entry nor a DNS claim — but URL minting still uses it, because a hostname *was* configured, and nothing warns.

---

## 4. Which service owns a request

Overcast serves every AWS service on one port, so something must decide which service a request belongs to. Most of that is the wire protocol's business, in [architecture.md §4](./architecture.md#4-the-request-path); the part decided from the `Host` header alone, with its tiers and the recognised subdomain list, is [docs/networking/host-routing.md](../networking/host-routing.md#how-overcast-decides-who-owns-a-host).

What that page does not say is why precedence runs A, then B, then C. Tier A (`{bucket}.s3.{base}`) must beat tier C (a registered host-route label) so a dotted bucket addressed explicitly wins: `my.execute-api.s3.localhost` is a bucket, not an API Gateway invoke. Tier B (`{bucket}.{base}`) must beat C so a configured `OVERCAST_HOSTNAME` wins over a generic label match inside it.

Two more consequences of the grammar rather than of any table. Bases are sorted longest-first, so a configured parent domain never shadows a longer default — with `OVERCAST_HOSTNAME=overcast.sh`, `x.localhost.overcast.sh` is bucket `x`, not `x.localhost`. And hostnames are case-folded through the same helper used when minting, because casing must never decide which service answers, and one implementation is the only way the two directions cannot drift.

---

## 5. The addresses Overcast hands back

The rule, the three deliberate divergences from it and the remapped-port edge are [docs/networking/urls.md](../networking/urls.md). Three things behind it are not on that page.

**The rule has a third part.** Host and port are as documented; **scheme** is https if Overcast serves TLS *or* the caller arrived over https, never a downgrade. The configured port also fills in when a request carries no port at all, as with internal dispatch.

**It funnels through one helper on purpose.** Before that existed the repository had four base-URL precedences and two of them disagreed about the port, which was exactly the bug. New code mints through `serviceutil.ClientBaseURL`; the divergences are commented where they live.

**Not every name can be host-routed.** An IP literal cannot carry a subdomain, and a bare single-label name only resolves under wildcard DNS that Windows and macOS do not provide for `*.localhost`. Fields with a path-style alternative degrade to it; a Lambda function URL and an API Gateway v2 `apiEndpoint` have none, so they are host-routed unconditionally. This is also why several AWS SDKs matter here at all: they send a request to the **queue URL's own origin** rather than to the endpoint you configured ([architecture.md §2](./architecture.md#2-what-an-aws-sdk-call-actually-is)), so a URL naming a host the caller cannot reach fails there while the AWS CLI succeeds.

---

## 6. Names: the split-horizon domains

Which names resolve, from where, and how to add one is [docs/networking/hostnames.md](../networking/hostnames.md). Two mechanisms deliver it, and why there are two is contributor material.

### Why a DNS server rather than `/etc/hosts`

`/etc/hosts` is an **exact-match table**, and DNS names are hierarchical. The apex can be shadowed that way; a subdomain cannot.

The URLs AWS SDKs build are overwhelmingly subdomains — virtual-hosted S3, API Gateway, AppSync, Lambda function URLs. Those names miss the table, fall through to public DNS, and resolve to `127.0.0.1` **by design**, which inside a container is the container itself. The lookup *succeeds*, so the failure surfaces as a refused connection rather than an unknown host. Enumeration cannot close the gap either: bucket names and API IDs are minted *after* a container starts, and a container's `/etc/hosts` is fixed at creation.

Both are kept. `/etc/hosts` covers the apex names with no dependency on the resolver being reachable; the resolver covers everything below them.

### How the resolver answers

It claims each split-horizon domain **and every subdomain**, with the boundary on a label separator — so `notlocalhost.overcast.sh` is not claimed.

For an address it asks the **kernel's routing table** which source address would reach the querying peer, by opening a UDP socket that transmits nothing. That is exactly the address the peer can reach back on, and it stays correct as Docker networks come and go, with no interface enumeration to refresh. Answers are cached per peer; loopback and unspecified results are discarded, since handing `127.0.0.1` to a container would name the container itself. All of this exists because Overcast sits on several networks at once and therefore has more than one address.

Three behaviours are load-bearing elsewhere:

- **Only A records are served.** AAAA for an owned name is deliberate NODATA — see [§11](#11-ipv6).
- **Out-of-zone queries are forwarded only for loopback, private and link-local peers**; anyone else gets REFUSED. Publishing the resolver by accident would otherwise expose an open forwarding resolver — the reflector half of a DNS amplification attack.
- **Failing to bind port 53 is not fatal.** It is logged, containers are not pointed at a resolver that is not there, and `/etc/hosts` still covers the apex names.

Overcast also never forges a negative answer, and distinguishes "I own this name but cannot resolve it" from "there is nothing here", because clients cache the second.

---

## 7. The container networks

There is a control plane, a default data plane and one network per VPC. The model, the `internal/dataplane` API that owns it, the egress modes and the per-start spec verification are [container-networking.md](./container-networking.md); the user-facing view is [docker-networks.md](../networking/docker-networks.md), [egress.md](../networking/egress.md) and [vpc-backing.md](../networking/vpc-backing.md). Three things belong here because they are EC2's VPC model rather than the plane model.

**`OVERCAST_EC2_VPC_STRATEGY` decides how VPCs map onto bridges.** `shared` (the default), `strict` and `remapped` are implemented; `netns` is declared but not implemented, and `config.Load` rejects it at startup rather than falling back. It matters because enforcement is per Docker network, so isolation is exactly as strong as the strategy in use — `shared` puts two same-CIDR VPCs on one bridge, and they are not isolated from each other.

**Resolving a resource's VPC is not optional for a new service.** One that never calls `PlaceInVPC` used to lose a little fidelity; now it strands its resource on the default plane, where the consumers in its own VPC cannot see it.

### The default VPC

Every region seeds one on first use, as every real AWS account has: `IsDefault`, `172.31.0.0/16`, a default subnet per availability zone, an attached internet gateway, a main route table carrying the default route, and a `default` security group.

Its backing network **is** the default data plane. So "this resource named no VPC" and "this resource is in the default VPC" are the same place by construction rather than by coincidence, which is what lets the reachability question stay a single comparison instead of a special case for the empty VPC ID. Two consequences: `DeleteVpc` on the default VPC removes the record and leaves the network, because every container Overcast started is attached to it, and a deleted default VPC stays deleted rather than being reseeded on the next describe — as on AWS, where `CreateDefaultVpc` is the way back.

### Two allocators, one subnet

An ECS `awsvpc` task has two things allocating addresses from the same subnet, and they do not know about each other. EC2's own counter decides what the task's ENI record says; Docker's IPAM decides what the container gets. They agree until one allocates once more than the other, and then they diverge silently — leaving a load balancer to read the ENI address and dial somewhere nothing is listening.

So Overcast asks rather than assumes. It requests the EC2-allocated address when attaching the container, and if Docker refuses it — outside the subnet, or already taken — accepts what Docker gives and rewrites the ENI record to match. The recorded address is always the real one. Only the first container in a task carries it: AWS gives an `awsvpc` task one shared network namespace, so a single address covers every container in it, and Docker gives each container its own.

---

## 8. A container reaching Overcast

How the endpoint is resolved and injected, and what `internal/containerendpoint` does with URLs baked into env vars, is [container-networking.md §1](./container-networking.md). Two decisions belong here.

**A name is injected rather than an address.** It survives Overcast's own container being recreated on a different address, which an address baked into a warm execution environment does not. And it can carry a subdomain, so an SDK deriving a virtual-hosted URL from the endpoint — `{bucket}.s3.{host}` — produces a name that resolves. From an IP endpoint the SDK is forced to path-style, and any URL it builds by prefixing labels is unusable.

**A container cannot see its own port mapping from the inside.** Started with `-p 4580:4566`, the process listens on 4566 and nothing in its environment records that host callers arrive on 4580. Overcast finds out by identifying its own container and asking Docker, bounded to three seconds because it sits on the startup path.

Sometimes it cannot: Overcast is not containerised, there is no Docker socket, or the port is not published at all. The information genuinely does not exist then, so Overcast falls back to the port it listens on — correct for every caller except one arriving through a remap. Two things lose accuracy as a result. **The web console** tells the browser to use the internal port, which is closed on the host, so its requests fail; this is the reason the detection exists at all. And **published-port URL rewriting is off**, so a queue URL baked in on the published port is not rewritten and a container dials its own loopback on a dead port. Both are avoided by mounting the Docker socket, or by publishing 1:1.

---

## 9. A container reaching another container

Docker's embedded resolver answers this from **network aliases**, and registering them is `internal/dataplane`'s job — [container-networking.md §2](./container-networking.md) is the whole of how, including the failure mode where a missing alias reaches Overcast's own resolver and the caller hangs on port 3306.

What belongs here is what Overcast's resolver does about that, because it is a property of `internal/dns` rather than of the attachment. Two obvious fixes do not work:

- **A blanket stricter resolver.** Overcast cannot tell "this subdomain should have been a container" from "this subdomain is one of mine" by pattern, because resource hostnames are minted at runtime inside a namespace it legitimately owns. Refusing every name it owns would break virtual-hosted S3 and every host-routed service, and forging NXDOMAIN would be worse, since clients cache negative answers and the failure would outlive the fix.
- **Matching the *shape* of a data-plane name.** `rds`, `cache` and `kafka` are ordinary words in a bucket name. A bucket legitimately called `my.rds` is addressed virtual-hosted as `my.rds.localhost`, which matches any grammar you would write for an RDS endpoint. This is the same reasoning that keeps those labels out of the host-routing table.

**What does work is refusing on positive identification.** When a name the zone owns reaches Overcast's resolver, it asks Docker a factual question: is some container advertising this exact alias, on a network this caller is not attached to? If so, the only answer Overcast could give is its own address, which produces the hang — so it refuses instead, and logs both sides:

```
refusing a data-plane name the caller cannot reach
  name:            cache-1.us-east-1.cfg.localhost.overcast.sh
  target:          elasticache cache-1
  caller:          ecs app/9f2
  target_networks: [overcast-vpc-vpc-0abc]
  caller_networks: [overcast_control overcast]
```

That cannot misidentify a bucket, because nothing advertises `my.rds.localhost` as an alias. Two limits fall out of the same rule: a caller Overcast did not start has unknown attachments rather than known-disjoint ones, so it is answered rather than refused; and a name with no container behind it anywhere — an RDS instance whose engine never started — is logged but still answered, since it cannot be told from an ordinary bucket with enough confidence to refuse.

The check is affordable because of where it sits. A container's resolver is Docker's, which answers from aliases and forwards only what it cannot answer, so every data-plane name reaching Overcast is *already* a miss and the Docker lookup is paid by connections that are already broken.

---

## 10. ECR and the fourth caller

Every other address here is dialled by your code, or by a container running your code. An ECR registry address is different: **the Docker daemon dials it**. And the daemon is not necessarily on your machine — under Docker Desktop it runs inside a VM, so an address that works from your shell may be meaningless to it, and an address that works from a Lambda container is on a network the daemon is not using either. That is why the registry URI is one of the [deliberate divergences](#5-the-addresses-overcast-hands-back) from the per-caller rule: the caller asking for the URI is not the party that will dial it.

**Overcast cannot verify the port itself.** Publishing must be dual-stack, because an explicit `0.0.0.0` restricts the binding to IPv4 and Docker Desktop does not wire an ephemeral v4-only binding into the daemon's VM. But a dual-stack ephemeral publish can land the IPv4 and IPv6 bindings **on different ports**, and which family `localhost` resolves to is the daemon's business — a port confirmed reachable from Overcast's own vantage still produced `dial tcp [::1]:<v4port>: connection refused` on CI. So Overcast asks the daemon to dial, using the same operation `docker login` performs, and takes the first port that answers.

The probe carries this instance's credentials, which makes it an *identity* check rather than a liveness one, and that detail is load-bearing. Two Overcast instances sharing a daemon can have their dual-stack publishes interleave, so that A's IPv4 port is B's IPv6 port. An anonymous probe would reach B, be refused exactly as A's own registry would refuse it, and A would then advertise a port serving B — every token A issued afterwards failing against B's credentials, as an authentication error carrying a valid-looking token. Offering the password separates them: our registry accepts it and reports the probe repository missing; a sibling's rejects it.

Failure is not fatal: Overcast advertises its first candidate port anyway, and warns. The symptom to recognise and the measured fixed-versus-ephemeral difference are in [docs/services/ecr/limitations.md](../services/ecr/limitations.md#when-the-fixed-port-is-taken).

---

## 11. IPv6

**Overcast is IPv4 in practice.** Do not plan on reaching it over IPv6. That is a choice never to take IPv6 on rather than an oversight: the API's default is IPv4 either way, container address discovery cannot produce an IPv6 address at all, and CloudFormation states outright that there is no IPv6 networking for a template to describe. An explicit IPv6 bind address is accepted and formatted correctly, but nothing downstream is built for one.

Where IPv6 has forced itself on Overcast it has been dealt with deliberately. The DNS server answers AAAA for its own names with a deliberate empty response rather than forwarding, because the public record for the split-horizon domains is `::1` and forwarding would hand a dual-stack client its own loopback — exactly the failure the resolver exists to prevent. The other case is [ECR](#10-ecr-and-the-fourth-caller), where the publish shape decides whether the daemon can reach the registry at all.

---

## 12. The bridge: friendly names on port 80

`overcast bridge`, or `overcast serve --bridge`, is opt-in; what it gives a user is [docs/cli/bridge.md](../cli/bridge.md). Three facts belong here. It **tails Overcast's internal domain feed** and advertises every registered API Gateway custom domain as it appears, rather than publishing a fixed set once. Failing to bind port 80 is **not fatal** — it logs a platform-specific privilege hint, and mDNS still works. And it is **refused while TLS is enabled**, because the proxy speaks plain HTTP to its backends and would fail every request.

---

## 13. Things that will bite you

Symptom first, and only the ones a contributor needs on top of [docs/networking/troubleshooting.md](../networking/troubleshooting.md) and [vpcs.md](../networking/vpcs.md).

- **A resource whose service does not resolve its VPC lands on the default plane, where its own VPC cannot see it.** The resource looks placed to the API and is not. Check the service wires a VPC resolver before assuming the caller is at fault.
- **Security groups, NACLs and subnets are not enforced.** Docker network membership expresses "in this VPC or not" and nothing finer — no port- or source-level filtering, and no public/private subnet distinction within a VPC.
- **`OVERCAST_CONTROL_PLANE_INTERNAL` is deprecated.** Still honoured for that one network, and setting it logs a notice naming the mode that means the same thing. It pins one network, and a container takes its default route from whichever of its networks is routable, so it only ever settled a third of the question. A pin of `false` always wins; a pin of `true` is overridden back to `false` where an internal control plane would sever the Runtime API.
- **A `{network}_control` created by an older Overcast keeps the isolation it was born with.** Docker never applies `--internal` retroactively, so a long-lived machine can lag a version pin until the network is rebuilt. What is checked, recreated and warned about is [container-networking.md §1d](./container-networking.md); `overcast network reset` is the fix a user is told.
- **A container cannot reach the SMTP capture server.** It binds loopback whatever `OVERCAST_LISTEN` says.
- **Host-routed HTTPS works in `OVERCAST_TLS=auto` only, not with your own certificate.** A one-level wildcard matches exactly one label, so `{id}.execute-api.{region}.{base}` falls outside any enumerable SAN list; auto mode covers it by minting per SNI from the local CA at handshake time ([how-it-works.md](../https/how-it-works.md#which-names-the-certificate-covers)). In explicit mode (`OVERCAST_TLS_CERT`) there is nothing to mint with, so those names need SANs in the certificate you supplied.
- **On a native Windows or macOS host the data-plane guard does not run, so VPC placement is not enforced and `OVERCAST_VPC_EGRESS=none` is short.** The resolver needs `/etc/resolv.conf` to find upstreams and silently does not start without one, logging at debug level; the apex names still work, from `/etc/hosts`. Since a forbidden connection could only fail by hanging there, the restriction is withheld rather than delivered blind — so the same stack behaves differently on a host run and a containerised one.

---

## 14. Where to go next

| Question | Document |
|---|---|
| How does a request get routed to a service at all? | [architecture.md §4](./architecture.md#4-the-request-path) |
| Why can my Lambda not reach my database? | [container-networking.md](./container-networking.md) |
| Which host and port will a returned URL carry? | [docs/networking/urls.md](../networking/urls.md) |
| What subdomains does Overcast recognise? | [docs/networking/host-routing.md](../networking/host-routing.md#known-aws-resource-subdomains) |
| What does each egress mode do? | [docs/networking/egress.md](../networking/egress.md) |
| How do I test an endpoint change properly? | [manual-testing.md](./manual-testing.md) |
| How do I set up HTTPS? | [docs/https.md](../https.md) |
