---
title: "EKS — Amazon Elastic Kubernetes Service"
description: "Quick start, the control-plane coverage in both modes, what OVERCAST_EKS_MODE=live provisions with k3s, and why nodegroups never start worker compute."
section: "Service Reference"
tags:
  - docs
  - eks
  - kubernetes
  - services
---

# EKS — Amazon Elastic Kubernetes Service

Control-plane metadata by default, or a real k3s control plane per cluster in
live mode — and no worker compute in either.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws eks create-cluster --name dev \
  --role-arn arn:aws:iam::000000000000:role/eks \
  --resources-vpc-config subnetIds=subnet-11111111

aws eks describe-cluster --name dev --query 'cluster.status'
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

In the default `mock` mode the cluster is `ACTIVE` immediately, with synthetic
endpoint and CA fields. Set `OVERCAST_EKS_MODE=live` to have `CreateCluster`
launch a k3s control-plane container instead — see
[Live mode](./eks/limitations.md).

## What works

| Area | Behaviour |
| --- | --- |
| Clusters | Full CRUD, updates, insights, version and config updates, tagging. |
| Nodegroups, Fargate profiles, add-ons | Full metadata CRUD, including scaling and update config, taints, labels and launch templates. |
| Access entries and access policies | Full CRUD and association APIs. |
| Identity provider configs, pod identity associations | Full CRUD. |
| Live mode (`OVERCAST_EKS_MODE=live`) | `CreateCluster` starts a k3s container; the cluster reports `CREATING` until k3s `/readyz` answers, then `ACTIVE` with a real endpoint and CA. |
| `UpdateKubeconfig` | `POST /_overcast/eks/clusters/{name}/kubeconfig` returns a generated kubeconfig. This is an Overcast extension — `aws eks update-kubeconfig` is a CLI-side command that no AWS API backs. |

## Differences from AWS

| Area                               | On AWS                        | Overcast                                                                           |
| ---------------------------------- | ----------------------------- | ---------------------------------------------------------------------------------- |
| Worker compute                     | Real EC2 and Fargate capacity | None. Nodegroups and Fargate profiles are metadata in both modes.                  |
| Control plane                      | Managed Kubernetes            | Absent in `mock`; k3s in `live`                                                    |
| IAM, access policies, pod identity | Enforced                      | Stored, never enforced                                                             |
| Cluster records across modes       | Not applicable                | A `mock`-created cluster is refused with `501` by nearly every live-mode operation |

The full mode-boundary rules, the failure vocabulary and the live-mode non-goals
are in [Limitations](./eks/limitations.md).

## Gotchas

> [!IMPORTANT]
> Live mode is opt-in because it costs a container per cluster. Overcast's
> startup and idle-memory figures are measured with `OVERCAST_EKS_MODE=mock`,
> and the first cluster on a machine that has never run one waits on the k3s
> image download.

Switching the mode does not carry existing clusters across.

> [!WARNING]
> Clusters created in `mock` mode are not usable after switching to `live`.
> Every read, update and mutation answers `501`; only `DeleteCluster` still
> works, so you can clear them out.

<!-- BEGIN overcast:capabilities -->

## Operations

All 49 listed operations are implemented.
Per-operation status, notes and AWS API links: [EKS operations](eks/operations.md).

<!-- END overcast:capabilities -->

## Related

- [EKS limitations](./eks/limitations.md) — live mode, mode boundaries, non-goals
- [ECS](./ecs.md) — the container service that does run real tasks
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/eks/latest/APIReference/Welcome.html)
