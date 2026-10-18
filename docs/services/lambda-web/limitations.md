---
title: "Lambda Web Functions limitations"
description: "What Lambda Web Functions stores without acting on, which behaviours are inferred rather than observed, and what the Docker data plane does differently from AWS."
section: "Service Reference"
tags:
  - docs
  - lambda
  - lambda-web
  - limitations
  - services
---

# Lambda Web Functions limitations

The full divergence list behind [Lambda Web Functions](../lambda-web.md).

## Not as on AWS

| Configuration | On AWS | Overcast |
| --- | --- | --- |
| Revision code | Lambda downloads the zip, unpacks it into a microVM and starts it | A background worker checks the zip when the revision is created (a corrupt archive, an entry that escapes the root or more than 512 MB unzipped makes it `Failed`). The first request starts a Docker container with the zip injected; a missing entry file shows up as `Runtime.ImportCodeError` |
| Execution environment | Firecracker microVM | A container of `public.ecr.aws/lambda/nodejs:24` with 2 GiB and 2 vCPU (`LAMBDA_WEB_ENVIRONMENT_MEMORY_MB`) |
| Capacity | Per-account service limits | `LAMBDA_WEB_MAX_ENVIRONMENTS` (default 8) containers per host; at the limit, idle environments of other endpoints are reclaimed, otherwise the request is refused with `Platform.EndpointMaxEnvThrottle` |
| `runtimeConfig`, `serviceConfig` | Shape the execution environment | `timeoutSeconds`, `maxConcurrencyPerEnvironment`, `environmentVariables` and `telemetryConfig.loggingConfig` are honoured. `kmsKeyArn` is format-checked and echoed |
| `egressNetworkConnectorArn` | Gives the revision VPC access | Format-checked and echoed; the container has Docker's default networking. A revision with a connector is refused on `MultiRegion` and `PerRegion` endpoints, as on AWS |
| `scalingConfig`, `throttleConfig` | Cap environments and requests per second | Enforced. The default rate limit is 10,000 per second per endpoint and 100,000 per account, with a burst of a tenth of a second's allowance |
| Resource policy and `IamAuth` | Authorise callers of the endpoint | Enforced for SigV4 and SigV4a signatures. The signature check follows the service-wide `SigV4Validate` setting and policy evaluation follows `EnforceIAM`. SigV4a is verified against Overcast's own signer only |
| Protocol | HTTP/1.1 and HTTP/2 over TLS | Plain HTTP; only HTTP/1.0 is refused (505) |
| Client address | The client's | `RealIP` rewrites the remote address from `X-Forwarded-For`, so the application sees that address twice in the header |
| `MultiRegion` and `PerRegion` | Replicate to the other Regions | Recorded in `regions` and `regionalEndpoints`; each Region's domain is served by this process |
| Invoke path | None | Requests can also be sent to `/_overcast/lambdaweb/invoke/{id}/{region}/…` when the Host header cannot be set |

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
  has registered them in the routing table. Changes to weights, auth, mode or scaling report
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
| Platform error messages and a few of their status codes | The error type names are documented; messages are not |
| 403 for every failed authentication or authorisation on an `IamAuth` endpoint | The documentation shows only `Forbidden` |
| Environments are reused first-fit, 80% headroom triggers a pre-start, a failed start backs off for 1 s | The documentation describes the behaviour, not the policy |
| `lambda-web:RequestCompleted` events are coalesced to one per endpoint per second | Not documented |
| Filter names | The model's documentation lists them: `state` for functions and revisions; `authType`, `autoDeploymentMode`, `endpointType`, `state` and `updateStatus` for endpoints |

## Measured

`TestEndToEnd_loadReport` (`tests/integration/lambdawebdocker`) drives a real
container through the real router. On a Windows 11 laptop with Docker Desktop:
a cold start (first request, container created) took about 1.2 s; a warm request
took 1.6 ms at the median and 2.5 ms at the 99th percentile; 32 concurrent
keep-alive clients sustained about 6,000 requests per second with none failing.
Expect cold starts of 0.4 to 1.2 s depending on the machine and whether the
image is already pulled. The test asserts only loose bounds.

## Related

- [Lambda Web Functions](../lambda-web.md)
- [Lambda Web Functions operations](./operations.md)
- [Lambda](../lambda.md)
- [All service pages](../README.md)
- [AWS Lambda Web Functions](https://docs.aws.amazon.com/lambda/latest/dg/web-functions.html)
