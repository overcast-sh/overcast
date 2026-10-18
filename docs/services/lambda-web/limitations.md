---
title: "Lambda Web Functions limitations"
description: "What Lambda Web Functions stores without acting on, which behaviours are inferred rather than observed, and what the data plane will add."
section: "Service Reference"
tags:
  - docs
  - lambda
  - lambda-web
  - limitations
  - services
---

# Lambda Web Functions limitations

The full divergence list behind [Lambda Web Functions](../lambda-web.md). The
control plane is complete; what is missing is everything that runs.

## Stored, never run

| Configuration | On AWS | Overcast |
| --- | --- | --- |
| Revision code | Lambda downloads the zip from a versioned S3 bucket and prepares it | Resolved and checked, but never run. Creating a revision pins the S3 version, refuses a missing or unversioned bucket, a missing key or version, and a package over 512 MB (`ValidationException`), then a background worker reads the zip: a corrupt archive, an entry that escapes the package root, or more than 512 MB unzipped makes the revision `Failed`. A missing entry file is not checked, as on AWS |
| `runtimeConfig`, `serviceConfig` | Shape the execution environment | Validated and echoed; nothing runs, so timeout, concurrency, environment variables and logging have no effect |
| `kmsKeyArn`, `egressNetworkConnectorArn` | Encrypt the revision; give it VPC access | Format-checked and echoed. A revision with a network connector is refused on `MultiRegion` and `PerRegion` endpoints, as on AWS |
| `scalingConfig`, `throttleConfig` | Cap environments and requests per second | Validated (`rateLimit` takes AWS's supported values) and echoed; no traffic is counted |
| Resource policy | Authorises callers of the endpoint, across accounts | Stored and validated for public access; never evaluated |
| `IamAuth` | Requires a SigV4 or SigV4a signature on requests | Recorded; there is no request path |
| `MultiRegion` and `PerRegion` | Replicate to the other Regions | Recorded in `regions` and `regionalEndpoints`; the other Regions hold nothing |

## State transitions

Revisions and endpoints move through their documented states.

- **Revisions** are created `Pending`. A worker fetches and checks the package
  and the revision becomes `Active`, or `Failed` with `stateReason` and an
  `errors` entry for `buildConfig.codeConfig.s3Object`. `Failed` is permanent.
  Once `Active`, `LatestRevision` endpoints are pointed at the newest `Active`
  revision. Weights can name only `Active` revisions, and switching an endpoint
  from `Disabled` to `LatestRevision` is a `ConflictException` while the latest
  revision is not `Active`. Automatic cleanup past 50 revisions counts and
  deletes only revisions that are not `Pending`. A service with no S3 code
  source wired (a control-plane-only run) makes a revision `Active` at once.
  `Pending` revisions found at start are resumed.
- **Endpoints** are created `Pending` and become `Active` once the data plane
  has registered them. Changes to weights, auth, mode or scaling report
  `updateStatus` `InProgress` and then `Successful` (or `Failed` with
  `updateStatusReason` when the data plane refuses the change); `state` stays
  `Active`. Deleting an endpoint sets `Deleting`, lets the data plane drain,
  then removes it. For `MultiRegion` and `PerRegion` endpoints every Region is
  applied together, so each `regionalEndpoints` entry carries the endpoint's own
  state and update status.
- Reads report the stored state. Create responses report `Pending`, as AWS's do.
- A record stored before these states existed has none and reads as `Active`.
- Everything above happens quickly and in one process, so a waiter usually sees
  the final state on its first poll.

## Inferred, not observed

These came from AWS's documentation and the Smithy model rather than from a call
to AWS, and may differ in message text or edge cases.

| Behaviour | Basis |
| --- | --- |
| A revision's ARN is `…:web-function/{name}/revision/{id}` | Symmetry with the documented endpoint ARN |
| Revision IDs look like `rev-a1b2c` | The documentation's examples |
| `stateReason` text | Required by the model; no example shows a value |
| `LatestRevision` and no revision yet leaves `revisionWeights` empty | Nothing to route to |
| `CreateWebFunction` refuses a `MultiRegion` or `PerRegion` endpoint | Those require weights and the call has no member for them |
| The error code of a refused S3 package (`ValidationException`) and every message | AWS says only that Lambda "rejects the request"; no code is documented |
| The `errors[].errorCode` values of a `Failed` revision: `InvalidCodePackage`, `CodePackageTooLarge`, `CodeUnavailable`, `CodeAccessLost` | The `errors` list is documented, its codes are not |
| A bucket whose versioning is `Suspended` is refused like an unversioned one | The documentation requires versioning to be "enabled" |
| A key whose current version is a delete marker is treated as missing | It reads as absent over S3 |
| Package contents are checked by unzipping every entry, rejecting absolute paths and `..` | The documentation states the 512 MB limit and the entry file, not the checks Lambda runs |
| A reserved environment variable is refused with `ValidationException` | Documented only as "Lambda rejects the request" |
| The environment variable size and count are not limited | The Web Functions pages give no limit |
| `stateReason` of a `Failed` revision reads "Lambda could not prepare the deployment package: …" | No example shows a value |
| Filter names | The model's documentation lists them: `state` for functions and revisions; `authType`, `autoDeploymentMode`, `endpointType`, `state` and `updateStatus` for endpoints |

## Related

- [Lambda Web Functions](../lambda-web.md)
- [Lambda Web Functions operations](./operations.md)
- [Lambda](../lambda.md)
- [All service pages](../README.md)
- [AWS Lambda Web Functions](https://docs.aws.amazon.com/lambda/latest/dg/web-functions.html)
