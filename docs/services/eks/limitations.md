---
title: "EKS limitations"
description: "The two EKS modes, what live mode does and does not provision, and why a mock-created cluster is refused once live mode is on."
section: "Service Reference"
tags:
  - docs
  - eks
  - limitations
  - services
---

# EKS limitations

What [EKS](../eks.md) provisions in each of its two modes, and where the emulation
stops.

## The two modes

`OVERCAST_EKS_MODE` selects one. `mock` is the default.

| | `mock` | `live` |
| --- | --- | --- |
| `CreateCluster` | Records the cluster, `ACTIVE` immediately | Pulls and starts a k3s container |
| Status path | `ACTIVE` | `CREATING` → `ACTIVE` once k3s `/readyz` answers, or `FAILED` |
| `cluster.endpoint` | `https://<name>.mock.eks.local` | Minted for the caller — see below |
| `certificateAuthority` | Synthetic placeholder | The k3s cluster's real CA |
| `UpdateKubeconfig` | Placeholder values | A kubeconfig that reaches the running control plane |
| Resource footprint | None | One container per cluster |

A live cluster's endpoint points at the k3s container, so `DescribeCluster`
mints it for whoever asks. From the host it is
`https://<OVERCAST_HOSTNAME or localhost>:<mapped port>`. From a Lambda
function, an ECS task or any other sibling container it is
`https://<name>.<region>.eks.<hostname>:6443`, a name the container answers to
on the same networks the other [data-plane endpoints](../../networking/data-plane-endpoints.md)
use. The generated kubeconfig's `server:` follows the same rule.

A live cluster that cannot start reaches `FAILED` rather than sitting in
`CREATING`, and `DescribeCluster` reports why under `cluster.health.issues` —
the Docker error verbatim for an image that could not be pulled or a container
that could not be created or started. The one substituted message is a shutdown
mid-start, which reports "Overcast shut down while the cluster was still
starting".

## A mock-created cluster is refused in live mode

Records minted in `mock` mode are recognised by their `*.mock.eks.local`
endpoint, and in live mode every cluster-scoped operation refuses them with
`501`:

`DescribeCluster`, list/describe updates, `DescribeClusterVersions`, insights,
`UpdateClusterVersion`, `UpdateClusterConfig`, every nodegroup, Fargate profile
and add-on operation, access entries and access-policy associations, identity
provider configs, pod identity associations, and the tagging operations for any
EKS ARN that resolves to such a cluster.

`DeleteCluster` is the exception, so mixed-mode leftovers can be cleaned up.
`ListClusters` filters them out.

## `UpdateKubeconfig` and 503

`POST /_overcast/eks/clusters/{name}/kubeconfig` answers `503` twice over:

- the cluster is not `ACTIVE`, or has no endpoint yet;
- the cluster is ready but the CA is still missing after Overcast has tried to
  reconcile the runtime and read it back out of the k3s container.

Both are temporary: retry once the cluster settles.

## Non-goals

- Live mode launches a **control plane** only. It provisions no worker capacity,
  so nodegroups and Fargate profiles remain metadata in both modes.
- Nodegroup, Fargate profile, add-on, access entry and policy, identity provider
  config and pod identity association APIs are control-plane surfaces. Nothing
  they store enforces IAM policy or schedules a Kubernetes workload.
- Overcast's published startup and idle-memory figures are measured in `mock`
  mode.

## Related

- [EKS](../eks.md) — quick start and what works
- [EKS operations](./operations.md) — per-operation status
- [ECS](../ecs.md) — the container service that does run real tasks
