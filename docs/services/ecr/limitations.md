---
title: "ECR limitations"
description: "How a repository URI is derived and why it says localhost, what survives a restart, and how the image inventory tracks the registry rather than accumulating."
section: "Service Reference"
tags:
  - docs
  - ecr
  - limitations
  - services
---

# ECR limitations

How a repository URI is derived, what survives a restart, and what the image
inventory tracks, behind [ECR](../ecr.md).

## The repository URI

```
localhost:<registryPort>/<accountId>/<repositoryName>
```

| Question                                       | Answer                                                                                                                                                    |
| ---------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Which port?                                    | The **registry container's**, not the API port — the URI is what a `docker push` targets                                                                  |
| Where else does the same address appear?       | `proxyEndpoint` from `GetAuthorizationToken`, and `Fn::GetAtt Repo.RepositoryUri` — never an `amazonaws.com` one                                          |
| Stored or derived?                             | Re-minted on every read. Repositories persist and the registry does not                                                                                   |
| Read while the registry is down?               | The fallback address, and the registry again once there is one                                                                                            |
| Why `localhost`?                               | `docker push`, `pull` and `login` are performed by the Docker daemon, and a daemon trusts plain HTTP to `localhost` without configuration                 |
| Why not `OVERCAST_HOSTNAME`?                   | A hostname such as `localhost.overcast.sh` goes through any proxy the daemon has configured (`proxyconnect tcp: dial tcp 192.168.65.1:3128: i/o timeout`) |
| Remote daemon, which cannot reach `localhost`? | `OVERCAST_HOSTNAME` stands instead, and that address is the one to add to the daemon's `insecure-registries`                                              |

### The startup reachability probe

Startup has the daemon itself contact the registry, through the Engine's
distribution-inspect endpoint, and warns once when it cannot rather than letting
every later push and pull fail on its own. The probe carries this instance's
credentials, which is what tells this registry from a sibling's — see
[A port answering as another instance's registry](./troubleshooting.md#a-port-answering-as-another-instances-registry).

## When the fixed port is taken

If something else holds `OVERCAST_ECR_REGISTRY_PORT` (default `4510`), the
registry falls back to an ephemeral port and says so in the log. The daemon may
then not reach it at all. Measured on Docker Desktop 29.6.2 for Windows, against
the same registry image:

| Publish                       | `docker login` |
| ----------------------------- | -------------- |
| Fixed — `localhost:5099`      | Succeeds       |
| Ephemeral — `localhost:62154` | Times out      |

`docker port` reports both bindings as dual-stack. Overcast says so at startup
("the Docker daemon cannot reach the ECR registry it just published"). Free the
fixed port, or point the variable at another fixed one, rather than working
around the pull failures downstream.

## Persistence

| Thing                                   | Backed by                                                                                                       | Survives a restart            |
| --------------------------------------- | --------------------------------------------------------------------------------------------------------------- | ----------------------------- |
| Images pushed to the fixed port         | The named volume `overcast-ecr-registry-data-<port>` on `/var/lib/registry`, labelled `overcast.service=ecr`    | Yes                           |
| Images pushed to the ephemeral fallback | The container's own volume — its name is random and its port is whatever the daemon had spare                   | No                            |
| The registry container                  | Nothing. It runs with `AutoRemove`, which reclaims only the anonymous volume it would otherwise have been given | No                            |
| Repository metadata                     | The state backend, not the volume                                                                               | Only if `OVERCAST_STATE` does |

`OVERCAST_ECR_REGISTRY_PERSIST=false` gives the fixed port the ephemeral
behaviour too.

A run on `OVERCAST_STATE=memory` comes back with no repositories even though the
images are still there. Re-creating the repository is enough for them to
reappear: the first read reconciles it against the registry.

Keeping the volume is what stops `cdk deploy` re-uploading — cdk-assets asks
`DescribeImages` for a container asset's content-hash tag and skips both the
build and the push when it resolves.

## Asking whether an image is published

| `DescribeImages` call                 | Answer                                                                            |
| ------------------------------------- | --------------------------------------------------------------------------------- |
| An `imageIds` entry that resolves     | The image                                                                         |
| An `imageIds` entry it cannot resolve | `ImageNotFoundException`, as real ECR answers — not a `200` carrying a short list |
| No `imageIds` at all                  | An empty list                                                                     |

That decides whether anything is ever pushed: cdk-assets treats *any*
non-throwing `DescribeImages` as "already published" and skips building and
pushing the asset, so a `200` for an absent tag leaves an empty repository behind
a clean deploy and a 404 at pull time.

The image inventory follows the registry rather than accumulating. On every
repository read:

| Record                         | What happens |
| ------------------------------ | ------------ |
| A manifest the registry serves | Recorded     |
| A record the registry 404s     | Dropped      |
| A record written by `PutImage` | Left alone   |

So a restart that keeps the registry's storage but loses the in-memory records
rediscovers them, and one that keeps the records but loses the storage drops
them.

`ListImages` reconciles the same way but, unlike `DescribeImages` and
`BatchGetImage`, never reports the sweep failing: a registry that is
momentarily unreachable still answers with whatever the store currently holds
rather than an error, because an empty or stale list names no specific image
to be wrong about.

`BatchDeleteImage` only ever removes the store's record. It never asks the
registry to delete the manifest, so when Docker backs the repository, deleting
an image the registry still serves does not stick — the next reconciling read
writes the record straight back.

## Related

- [ECR](../ecr.md) — quick start and what works
- [ECR troubleshooting](./troubleshooting.md) — push failures, leaked containers, reclaiming storage
- [ECR operations](./operations.md) — per-operation status
- [Environment variable reference](../../configuration/reference.md) — `OVERCAST_ECR_REGISTRY_PORT` and the rest
