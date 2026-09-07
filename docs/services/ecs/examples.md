---
title: "ECS examples"
description: "Running images from the emulated ECR, injecting secrets, shipping container logs, serving through a load balancer, hot-reloading local source inside a task, and attaching a debugger to a container."
section: "Service Reference"
tags:
  - docs
  - ecs
  - examples
  - services
---

# ECS examples

Worked task and service setups past the [ECS quick start](../ecs.md#quick-start).

## Images published to the emulated ECR

A container definition may name an image by its ECR address —
`{account}.dkr.ecr.{region}.amazonaws.com/{repo}:{tag}` — which is what CDK
synthesises for a container asset, from `AWS::AccountId` and `AWS::Region` rather
than from the repository it pushed to. That hostname belongs to real AWS and
resolves, so pulling it as written leaves the machine and is refused anonymously.

When the address is this account's registry, Overcast pulls it from the registry
it actually serves, with the credentials `ecr:GetAuthorizationToken` issues. Any
other image is pulled exactly as written, and another registry is never offered
these credentials. The task still reports the image its definition asked for.

Nothing has to be pushed through ECR for this to apply. If the registry is not
running, the reference is left alone. See [ECR](../ecr.md).

## Secrets

`containerDefinitions[].secrets` are resolved at task start and injected as
environment variables, from either source AWS supports, told apart by the ARN:

| Source | ARN form |
| --- | --- |
| Secrets Manager | `arn:aws:secretsmanager:…:secret:name-AbCdEf`, with the optional `:json-key:version-stage:version-id` suffix |
| SSM Parameter Store | `arn:aws:ssm:…:parameter/name`, or a bare parameter name |

Naming a key reads that field out of a JSON secret, which is what
`ecs.Secret.fromSecretsManager(secret, "password")` produces and therefore the
form most task definitions use.

A secret that cannot be resolved is named in a warning and left out rather than
injected as an empty value. Real ECS fails the task outright.

The value is read once per new container, immediately before it is created, so a
running task keeps the value it started with. After rotating a secret, launch a
new task or call `UpdateService` with `forceNewDeployment: true`; the replacement
deployment resolves the current value without a new task-definition revision.

## Container logs

A container definition using the `awslogs` driver has its output shipped to
CloudWatch Logs, into `awslogs-group` and a stream named
`<awslogs-stream-prefix>/<container>/<task-id>` — ECS's own naming. This applies
to every task however it was started and under either launch type, because the
driver is a property of the container definition rather than of Fargate or EC2.

Output is read back from the Docker daemon, and that read reconnects if the
daemon drops it, resuming from the last line delivered and recognising the ones
the daemon replays, so a hiccup costs neither the rest of a task's logs nor a
duplicate.

Separately, and whether or not `awslogs` is configured, Overcast retains the tail
of each container's output when it exits — up to 200 lines, capped at 16 KiB.
`GET /_overcast/ecs/tasks/{taskArn}/logs/{container}` returns that for a stopped
task, or a live tail for a running one. This is an emulator-only diagnostic, not
an ECS or CloudWatch Logs API, and stays addressable only while ECS retains the
stopped task record.

## Load balancers

A service with `loadBalancers` registers each task it places into the named
target group, at the task's ENI address and container port, and deregisters it
when the task stops or the service scales in — so
`ApplicationLoadBalancedFargateService` produces a URL that serves the
application.

The URL is reachable on Overcast's own port rather than on the listener's: the
DNS name resolves to Overcast, which serves every host-routed endpoint on one
listener. CDK's `ServiceURL` output carries that port already.

For a Docker-backed `awsvpc` task the registered ENI address is the address the
container really holds on its VPC's Docker network, and Overcast joins that
network itself — otherwise the address it registers is one it cannot dial, and
forwarding fails with a gateway error or a timeout. Both only apply when Overcast
is itself containerised.

## Hot reload

A task definition can point one of its scratch volumes at a directory on your
machine, so a save is live in the container on the next request with no image
rebuild and no redeploy. This is the ECS half of the mechanism Lambda uses (see
[Lambda examples](../lambda/examples.md)) — same tag, same flag family.

Nothing about it changes what the task definition means to AWS. The volume is an
ordinary name-only scratch volume, legal on Fargate and deployable as-is; only a
tag, which real AWS stores and ignores, says to back it with a bind locally.

Enable it on the server, for every compute service or just ECS:

```bash
OVERCAST_HOT_RELOAD=true overcast serve
```

Then tag the task definition with the volume to redirect and the host path:

```
overcast:hot-reload-path/<volume-name> = /absolute/host/path
```

The volume name lives in the tag **key**, so a Windows path in the value stays
unambiguous. With exactly one redirectable volume you can drop the suffix and use
the bare `overcast:hot-reload-path`, the same key Lambda takes.

Only a volume declared with a name and **no configuration** can be redirected: an
EFS, Docker or `host.sourcePath` volume already names its own storage. The
container path and `readOnly` come from the container's own `mountPoints`,
exactly as in production. Windows paths are normalised
(`F:\dev\app` → `/f/dev/app`).

Anything that cannot be honoured — the flag off, an ambiguous bare tag, an unknown
or unredirectable volume, a relative path — leaves the task running on the plain
scratch volumes it declared, and says so in a warning naming what to fix.

## Step debugging

The same flag family and tag as Lambda, on the task definition:

```bash
OVERCAST_DEBUGGER=true overcast serve
```

```typescript
cdk.Tags.of(taskDef).add("overcast:debug-port/app", "9229");
cdk.Tags.of(taskDef).add("overcast:debug-protocol/app", "inspector");
```

The container name goes in the key, as for hot reload; a single-container
task definition takes the bare `overcast:debug=true`. Overcast opens the port on
`127.0.0.1` at task start and forwards your editor to the container. Protocols,
path mappings and editor setup are in
[Step debugging inside emulated compute](../../debugger.md#ecs).

## Related

- [ECS](../ecs.md) — quick start and what works
- [ECS limitations](./limitations.md) — rollouts, volumes, networking
- [ECS troubleshooting](./troubleshooting.md) — tasks that will not start or stay up
- [The inner loop](../../local-dev.md) — hot reload across services
- [Step debugging inside emulated compute](../../debugger.md) — breakpoints inside a container
