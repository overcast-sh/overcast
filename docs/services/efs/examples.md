---
title: "EFS examples"
description: "Sharing an EFS file system between a Lambda function and an ECS task, and mounting one over NFS with OVERCAST_EFS_NFS."
section: "Service Reference"
tags:
  - docs
  - efs
  - examples
  - services
  - storage
---

# EFS examples

Worked setups past the [EFS quick start](../efs.md#quick-start). Both assume `live` mode
(the default) and a reachable Docker daemon.

## Sharing a file system with Lambda and ECS

No NFS is involved: both sides mount the same named Docker volume, so they see
each other's writes immediately.

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

FS=$(aws efs create-file-system --creation-token shared \
  --query FileSystemId --output text)
AP=$(aws efs create-access-point --file-system-id "$FS" \
  --root-directory 'Path=/app/data,CreationInfo={OwnerUid=1000,OwnerGid=1000,Permissions=0755}' \
  --query AccessPointId --output text)

AP_ARN="arn:aws:elasticfilesystem:us-east-1:000000000000:access-point/$AP"
aws lambda update-function-configuration --function-name writer \
  --file-system-configs "Arn=$AP_ARN,LocalMountPath=/mnt/data"
```

An ECS task reaches the same bytes through `efsVolumeConfiguration` with the
same file system id, optionally scoped by `rootDirectory`. Access points with
`CreationInfo` have their root directory created with the declared ownership
and permissions before the first mount; without `CreationInfo`, a missing
directory makes the mount fail, exactly as on AWS.

## Mounting over NFS

`OVERCAST_EFS_NFS=true` gives every mount target a real, mountable NFSv4
export. `CreateMountTarget` starts one NFS-Ganesha container named
`overcast-efs-nfs-<MountTargetId>` exporting the file system's volume and
publishing container port 2049 on a free host port at or above
`EFS_NFS_PORT_BASE`; `DeleteMountTarget` removes it.

It is opt-in: Lambda and ECS already share bytes through the volume, and an
export costs a container and a port per mount target.

| Variable | Default | Purpose |
| --- | --- | --- |
| `OVERCAST_EFS_NFS` | `false` | Opt into exports (live mode, which is the default) |
| `EFS_NFS_PORT_BASE` | `22049` | First host port considered for publishing 2049 |
| `EFS_NFS_IMAGE` | digest-pinned NFS-Ganesha | Override the export image |
| `OVERCAST_NETWORK` | `overcast` | Docker network an export joins when its subnet is not in a VPC Overcast knows |

Ganesha runs entirely in userspace — no `--privileged`, no kernel modules — so
the export works on Linux, macOS and Windows Docker hosts alike. The container
is granted exactly one Linux capability, `CAP_DAC_READ_SEARCH`: Ganesha's VFS
backend resolves NFS file handles with `open_by_handle_at()`, which the kernel
gates on it. Mounting the export needs `CAP_SYS_ADMIN` on the *client*.

### Where to mount from

| Client | Address |
| --- | --- |
| The Docker host | `localhost:<published port>` — read it from `docker ps`, since `DescribeMountTargets` does not report it |
| A sibling container | The mount target's DNS name, on the network the export joined — see below |

An export joins the Docker network of the VPC its subnet is in, the same
network a Lambda function or ECS task placed in that VPC is on, so
`<FileSystemId>.efs.<region>.<hostname>` resolves there. A mount target whose
subnet is not one the EC2 emulation knows joins the shared data plane
(`OVERCAST_NETWORK`) instead. A subnet in a VPC that has no Docker network
behind it is refused at `CreateMountTarget` with a `BadRequest` naming the VPC.

### Pseudo-paths

Exports follow the file system's access points: `/` is the volume root, and
`/<AccessPointId>` is that access point's root directory, squashed onto its
`PosixUser` when it declares one. Two consequences:

- **An empty directory named after each access point appears in the file system
  root.** Ganesha grafts a pseudo-path onto a name that already exists in the
  exported volume, so the export container creates one anchor directory per
  access point before it starts. Mounting `/<AccessPointId>` shows the access
  point's root directory, never the anchor.
- **Exports are fixed when the mount target starts.** Ganesha reloads exports
  only on restart. An access point created afterwards has no pseudo-path until
  the mount target is deleted and recreated; its data is reachable in the
  meantime at the equivalent path under the root export
  (an access point rooted at `/app/data` is `/app/data` below `/`), just
  without the `PosixUser` squash.

### Readiness

With exports on, a mount target stays `creating` until its export answers an
NFSv4 call, then becomes `available` — so a successful `DescribeMountTargets`
means the export is genuinely serving. An export that never answers settles the
mount target in `error`, with a warning naming the container whose logs say
why. With `OVERCAST_EFS_NFS` off there is no export to fail and mount targets
go straight to `available`.

> [!NOTE]
> `DescribeMountTargets.IpAddress` is always synthetic, exports on or off. The
> export is reached through its published host port or the export network,
> never through that address.

## Related

- [EFS](../efs.md) — quick start and what works
- [EFS operations](./operations.md) — per-operation status
- [Lambda, ECS and VPCs](../../networking/vpcs.md) — what a mount target needs to be reachable
- [Storage and persistence](../../storage.md) — where file system data lives
