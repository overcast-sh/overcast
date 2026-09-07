---
title: "The inner loop — live code inside emulated compute"
description: "Edit a file and see it take effect without rebuilding an image or redeploying: cdk watch, Lambda hot reload, and ECS hot reload, with a full Laravel-on-Fargate walkthrough."
section: "Getting Started"
tags:
  - docs
  - guide
  - hot-reload
  - lambda
  - ecs
  - development
---

# The inner loop — live code inside emulated compute

Running your application against Overcast gets you a realistic AWS. It does not,
on its own, get you a fast edit-run cycle: a Lambda still wants a zip and an ECS
task still wants an image, so a one-character fix costs a rebuild and a redeploy.

## Which one to use

| | How it works | Best for |
| --- | --- | --- |
| **`cdk watch`** | Redeploys changed assets on every save | Any runtime, any bundler. **Start here** — nothing to configure |
| **Lambda hot reload** | Binds a host directory at `/var/task` | Interpreted runtimes, when redeploy latency still bites |
| **ECS hot reload** | Binds a host directory over a task volume | Long-running services — Laravel, Rails, Django, Express |

`cdk watch` is genuinely the right answer more often than people expect. It
needs no tags, no flags, and no Docker file-sharing configuration, and it works
with compiled runtimes and bundlers that hot reload cannot help:

```bash
AWS_ENDPOINT_URL=http://localhost:4566 cdk watch
```

Reach for a bind mount when the redeploy cycle itself is the cost you want gone.

Breakpoints are a separate switch: [Step debugging inside emulated compute](./debugger.md)
attaches your editor to the same containers, and reads `overcast:hot-reload-path`
as its source root so a hot-reloaded function needs no second tag.

## Turning hot reload on

Both services are off by default — a server that binds arbitrary host paths on
request is not something to enable silently. One flag covers everything:

```bash
OVERCAST_HOT_RELOAD=true overcast serve
```

| Variable | Default | Effect |
| --- | --- | --- |
| `OVERCAST_HOT_RELOAD` | `false` | Every compute service |
| `OVERCAST_LAMBDA_HOT_RELOAD` | inherits the umbrella | Lambda only |
| `OVERCAST_ECS_HOT_RELOAD` | inherits the umbrella | ECS only |

The per-service variables override the umbrella **in both directions**, so
`OVERCAST_HOT_RELOAD=true OVERCAST_LAMBDA_HOT_RELOAD=false` hot reloads ECS
while leaving functions on their deployed zips.

## Lambda

Tag the function with an absolute host path and the directory is bound at
`/var/task`, read-only:

```bash
aws --endpoint-url http://localhost:4566 lambda create-function \
  --function-name demo \
  --runtime nodejs20.x \
  --handler index.handler \
  --role arn:aws:iam::000000000000:role/lambda-role \
  --zip-file fileb://minimal.zip \
  --tags overcast:hot-reload-path=/absolute/path/to/src
```

Full detail, including CDK tagging and the TypeScript caveats, is in
[Lambda examples § Hot reload](./services/lambda/examples.md#hot-reload).

### What counts as a change

Overcast fingerprints the mounted tree — every entry's path, and every file's
size and modification time — before each invocation. When the fingerprint moves
the warm container is retired and the next invocation starts a fresh one against
the edited source, exactly as `UpdateFunctionCode` does, so editing an
already-loaded file works on runtimes that cache modules.

The read is bounded so it stays cheap:

| Bound | Effect |
| --- | --- |
| Dependency and VCS directories are not looked inside | `node_modules`, `.git`, `__pycache__`, `.venv`, `.mypy_cache`, `.pytest_cache` are fingerprinted by name only |
| 20,000 entries, 24 directory levels | A larger or deeper tree is covered up to the limit; edits past it are not noticed |
| A read costing more than 25 ms is rate-limited | Re-read at most once per 20× the previous read's cost, and at least once every 2 seconds |
| Symbolic links are not followed | A symlink is fingerprinted by name |

| Situation | What you get |
| --- | --- |
| Below the 25 ms budget | The tree is read before every invocation and an edit is live on the next one. A local disk takes 4–11 µs per entry, so a few thousand files stays under it |
| A Docker Desktop file share | About 2 ms per entry, so an edit can take up to 2 seconds rather than one invocation to be seen |
| Overcast itself in a container | The bind mount is the daemon's, but the fingerprint is read by Overcast from its own filesystem. Mount the source into Overcast at the same path too, or the first invocation runs the mounted source and every later one keeps that container |
| Filesystem timestamps at 1–2 second granularity | Two saves within one tick that leave the file the same size are one change; the second waits for something else to change |
| A very large tree, a slow file share, or dependencies you edit directly | Use `cdk watch` instead |

## ECS

A task definition declares an ordinary **name-only scratch volume**, mounts it
where the source belongs, and a tag redirects that volume at your working tree:

```
overcast:hot-reload-path/<volume-name> = /absolute/host/path
```

Nothing about this changes what the task definition means to AWS. The volume is
a plain scratch volume — legal on Fargate, deployable as-is — and the container
path and `readOnly` come from the container's own `mountPoints`, exactly as in
production. Only the tag, which real AWS stores and ignores, redirects it
locally. A tag that reaches a production deploy does nothing there.

Only a volume that declares **nothing** can be redirected. An EFS, Docker, or
`host.sourcePath` volume already names its own storage; pointing it somewhere
else would override what the definition asked for rather than fill in what it
deliberately left open.

With exactly one redirectable volume you can drop the suffix and use the bare
`overcast:hot-reload-path` — the same key Lambda takes. See
[ECS examples § Hot reload](./services/ecs/examples.md#hot-reload).

## Worked example: Laravel on Fargate

The shape most teams have: PHP-FPM behind nginx, deployed to Fargate, running
locally against Overcast from the same CDK app.

Everything local sits behind one guard, so the production synth is untouched:

```typescript
const taskDef = new ecs.FargateTaskDefinition(this, "AppTask", {
  cpu: 512,
  memoryLimitMiB: 1024,
});

const app = taskDef.addContainer("app", {
  image: ecs.ContainerImage.fromAsset("./docker/php-fpm"),
  logging: ecs.LogDrivers.awsLogs({ streamPrefix: "app" }),
  portMappings: [{ containerPort: 8000 }],
});

if (process.env.OVERCAST_LOCAL === "true") {
  // Plain scratch volumes — all Fargate-legal. Only app-src is redirected;
  // vendor and storage stay scratch so they shadow the host tree.
  taskDef.addVolume({ name: "app-src" });
  taskDef.addVolume({ name: "app-vendor" });
  taskDef.addVolume({ name: "app-storage" });

  app.addMountPoints(
    { sourceVolume: "app-src", containerPath: "/var/www/html", readOnly: false },
    { sourceVolume: "app-vendor", containerPath: "/var/www/html/vendor", readOnly: false },
    { sourceVolume: "app-storage", containerPath: "/var/www/html/storage", readOnly: false },
  );

  // Suffixed key: three redirectable volumes means the bare tag is ambiguous.
  cdk.Tags.of(taskDef).add(
    "overcast:hot-reload-path/app-src",
    path.resolve(__dirname, ".."),
  );
}
```

```bash
OVERCAST_HOT_RELOAD=true overcast serve
```

```bash
OVERCAST_LOCAL=true AWS_ENDPOINT_URL=http://localhost:4566 npx cdk deploy
```

### The traps, in the order they bite

**OPcache will ignore your edits.** Production PHP images ship
`opcache.validate_timestamps=0`, so PHP compiles each file once and never checks
it again. Your saves land in the container and do nothing. This is by far the
most common "the mount isn't working" report when the mount is fine. Ship a
dev-only ini:

```ini
opcache.validate_timestamps=1
opcache.revalidate_freq=0
```

**Keep `vendor/` in the image.** A bind mount from a Windows or macOS host
crosses a VM boundary, and Composer's autoloader stats thousands of files per
request. The `app-vendor` scratch volume above shadows the host's `vendor/`, and
Docker's copy-up populates it from the image on first mount. Re-run
`composer install` in the image, not on the host, when dependencies change. Same
reasoning as
[Storage tuning § Data dir placement](./performance/storage-tuning.md#data-dir-placement--avoid-host-bind-mounts-on-docker-desktop).

**Overlay the writable paths.** `storage/` and `bootstrap/cache` get the same
treatment: writes stay out of your working tree, and copy-up hands the
directories to the image's `www-data` ownership — which also dissolves the uid
mismatch that breaks first write on native-Linux hosts. (Docker Desktop maps
bind ownership permissively; Linux does not.) The trade-off: `storage/logs` then
lives in the volume rather than your tree, so read logs through the task's
`awslogs` stream instead.

**File watchers do not see host edits.** inotify does not propagate across
Docker Desktop's host boundary, so anything watching needs polling. Worse,
`php artisan queue:work` caches the whole application in memory and will not
pick up code changes at all — use `queue:listen` locally.

**Reaching the app.** Overcast publishes a host port only when a port mapping
carries an explicit `hostPort`, and Fargate mappings usually carry only
`containerPort`. Front the service with `ApplicationLoadBalancedFargateService`
and use the stack's `ServiceURL` output, which resolves to Overcast and forwards
to the task — see [ECS examples § Load balancers](./services/ecs/examples.md#load-balancers) —
or add a `hostPort` under the same local guard as the volume.

## When it does not work

Nothing here degrades silently: a task never starts with a mount you asked for
and did not get.

| Symptom | Cause | Fix |
| --- | --- | --- |
| A startup warning, and the task running on the plain scratch volumes it declared | The tag cannot be honoured — the flag is off, the volume is unknown or not redirectable, the path is relative, or a bare key is ambiguous | The warning names exactly what to correct |
| `CannotStartContainerError`, with a `stoppedReason` naming the host paths | The daemon refused the bind, usually a path outside Docker Desktop's **File Sharing** set | Add the directory there and run the task again |
| Edits reach the container and the application ignores them | The mount is working and the problem is upstream — OPcache, a compiled bundle, or a worker holding the old code in memory | Check the mount with a file the framework does not cache |

```bash
docker exec $(docker ps -q -f name=overcast-ecs) ls -la /var/www/html
```

## Related

- [Lambda examples](./services/lambda/examples.md) — hot reload for functions
- [ECS examples](./services/ecs/examples.md) — hot reload inside a task
- [Step debugging inside emulated compute](./debugger.md) — breakpoints inside the same containers
- [Using AWS CDK](./cdk.md) — `cdk watch` against Overcast
- [Troubleshooting](./troubleshooting.md) — when an edit does not reach a container
