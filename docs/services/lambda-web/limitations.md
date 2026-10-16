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
| Revision code | Lambda downloads the zip from a versioned S3 bucket and prepares it | Never read. A missing bucket, key or version does not fail the revision |
| `runtimeConfig`, `serviceConfig` | Shape the execution environment | Validated and echoed; nothing runs, so timeout, concurrency, environment variables and logging have no effect |
| `kmsKeyArn`, `egressNetworkConnectorArn` | Encrypt the revision; give it VPC access | Format-checked and echoed. A revision with a network connector is refused on `MultiRegion` and `PerRegion` endpoints, as on AWS |
| `scalingConfig`, `throttleConfig` | Cap environments and requests per second | Validated (`rateLimit` takes AWS's supported values) and echoed; no traffic is counted |
| Resource policy | Authorises callers of the endpoint, across accounts | Stored and validated for public access; never evaluated |
| `IamAuth` | Requires a SigV4 or SigV4a signature on requests | Recorded; there is no request path |
| `MultiRegion` and `PerRegion` | Replicate to the other Regions | Recorded in `regions` and `regionalEndpoints`; the other Regions hold nothing |

## State transitions

AWS creates a revision and an endpoint `Pending` and moves them to `Active`
asynchronously. Here they are `Active` from the moment they exist, but the
create response reports `Pending`, so a client written to wait for `Active`
works unchanged. `Failed` and `Deleting` are never reached: deleting an
endpoint removes it at once.

## Inferred, not observed

These came from AWS's documentation and the Smithy model rather than from a call
to AWS, and may differ in message text or edge cases.

| Behaviour | Basis |
| --- | --- |
| A revision's ARN is `…:web-function/{name}/revision/{id}` | Symmetry with the documented endpoint ARN |
| Revision IDs look like `rev-a1b2c` | The documentation's examples |
| `stateReason` text | Required by the model; no example shows a value |
| `revisionWeights` is refused while `autoDeploymentMode` is `LatestRevision` | The two are described as alternatives |
| `LatestRevision` and no revision yet leaves `revisionWeights` empty | Nothing to route to |
| `CreateWebFunction` refuses a `MultiRegion` or `PerRegion` endpoint | Those require weights and the call has no member for them |
| Filter names | The model's documentation lists them: `state` for functions and revisions; `authType`, `autoDeploymentMode`, `endpointType`, `state` and `updateStatus` for endpoints |

## Related

- [Lambda Web Functions](../lambda-web.md)
- [Lambda Web Functions operations](./operations.md)
- [Lambda](../lambda.md)
- [All service pages](../README.md)
- [AWS Lambda Web Functions](https://docs.aws.amazon.com/lambda/latest/dg/web-functions.html)
