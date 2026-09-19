---
title: "ECR operations"
description: "Every ECR operation Overcast declares — 22 of 25 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - ecr
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# ECR operations

22 of 25 listed operations are implemented. Back to [ECR](../ecr.md).

## Summary

| Category | ✅ Supported | 🧊 Inert | ⚠️ Partial | ❌ Unsupported |
| -------- | ------------ | -------- | ---------- | -------------- |
| General  | 2            |          | 3          |                |
| Auth     |              |          | 1          |                |
| Images   | 3            | 2        | 2          | 3              |
| Policy   | 6            |          |            |                |
| Tags     | 3            |          |            |                |

---

## Endpoints

### General

| Operation               | Status       | Notes                                                                                                                                                                                                                                                                                                                                        | AWS Docs                                                                                         |
| ----------------------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `CreateRepository`      | ⚠️ Partial   | Returns ARN, URI, createdAt, imageTagMutability, imageScanningConfiguration, and encryptionConfiguration; repositoryUri and push readiness depend on a reachable Docker daemon — without one, repositoryUri falls back to Overcast's own API port and the response carries the emulation-limitation header (`docker push` there answers 405) | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_CreateRepository.html)      |
| `DescribeRepositories`  | ⚠️ Partial   | Lists all repos or filters by name; repositoryUri is re-minted from the currently running registry on every read, so it reflects the same Docker-availability fallback as CreateRepository                                                                                                                                                   | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_DescribeRepositories.html)  |
| `DeleteRepository`      | ⚠️ Partial   | Deletes the repository and all its image records; a repository still holding images raises RepositoryNotEmptyException unless `force` is set. The not-empty check and the echoed repositoryUri both depend on the registry: an unreachable registry falls back to the store's own image records rather than refusing the delete              | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_DeleteRepository.html)      |
| `DescribeRegistry`      | ✅ Supported | Returns registry metadata with empty replication rules                                                                                                                                                                                                                                                                                       | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_DescribeRegistry.html)      |
| `PutImageTagMutability` | ✅ Supported | Stores MUTABLE/IMMUTABLE and DescribeRepositories echoes it; not enforced against a repeat PutImage of the same tag                                                                                                                                                                                                                          | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_PutImageTagMutability.html) |

### Auth

| Operation               | Status     | Notes                                                                                                                                                                                                                                                                                                                      | AWS Docs                                                                                         |
| ----------------------- | ---------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `GetAuthorizationToken` | ⚠️ Partial | Returns `base64("AWS:<password>")` and the registry proxy endpoint; token expiry is 12 hours. Without a reachable Docker daemon no registry ever starts, so proxyEndpoint falls back to Overcast's own API port and the token authenticates nothing there — `docker login` succeeds but a subsequent push/pull answers 405 | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_GetAuthorizationToken.html) |

### Images

| Operation                          | Status         | Notes                                                                                                                                                                                                                                                                                                                                                                           | AWS Docs                                                                                                    |
| ---------------------------------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `ListImages`                       | ⚠️ Partial     | Returns image IDs (tag + digest); reconciles local registry tags into the store when Docker is available. Deliberately does not fail when the registry is transiently unreachable, unlike DescribeImages/BatchGetImage — it answers with whatever the store currently holds rather than ServerException, since an empty or stale list names no specific image to be wrong about | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_ListImages.html)                       |
| `DescribeImages`                   | ✅ Supported   | Returns image detail objects (digest, tags, media type); an imageIds entry that resolves to nothing raises ImageNotFoundException; reconciles local registry manifests when Docker is available                                                                                                                                                                                 | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_DescribeImages.html)                   |
| `PutImage`                         | ✅ Supported   | Stores an image manifest; generates a digest if none supplied                                                                                                                                                                                                                                                                                                                   | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_PutImage.html)                         |
| `BatchGetImage`                    | ✅ Supported   | Fetches manifests by tag or digest; reconciles local registry manifests when Docker is available, and an unreachable registry answers ServerException rather than reporting an image falsely missing, mirroring DescribeImages                                                                                                                                                  | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_BatchGetImage.html)                    |
| `DescribeImageScanFindings`        | 🧊 Inert       | Always reports imageScanStatus UNSUPPORTED_IMAGE with empty findings/enhancedFindings; no scan engine is emulated, regardless of scanOnPush or any registry/repository scanning configuration                                                                                                                                                                                   | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_DescribeImageScanFindings.html)        |
| `BatchDeleteImage`                 | ⚠️ Partial     | Deletes images by tag or digest from the store; when Docker backs the repository, the registry itself is never asked to delete anything, so an image it still serves reappears on the next reconciling read (ListImages/DescribeImages/BatchGetImage) — unlike real ECR, where the deletion is durable                                                                          | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_BatchDeleteImage.html)                 |
| `PutImageScanningConfiguration`    | 🧊 Inert       | Stores scanOnPush and DescribeRepositories echoes it; no scan engine is emulated, so enabling it never makes DescribeImageScanFindings report anything but scanner-unavailable                                                                                                                                                                                                  | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_PutImageScanningConfiguration.html)    |
| `StartImageScan`                   | ❌ Unsupported | Not implemented, returns 501; no scan engine is emulated (see DescribeImageScanFindings)                                                                                                                                                                                                                                                                                        | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_StartImageScan.html)                   |
| `GetRegistryScanningConfiguration` | ❌ Unsupported | Not implemented, returns 501; registry-wide scanning configuration is not stored                                                                                                                                                                                                                                                                                                | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_GetRegistryScanningConfiguration.html) |
| `PutRegistryScanningConfiguration` | ❌ Unsupported | Not implemented, returns 501; registry-wide scanning configuration is not stored                                                                                                                                                                                                                                                                                                | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_PutRegistryScanningConfiguration.html) |

### Policy

| Operation                | Status       | Notes                                                      | AWS Docs                                                                                          |
| ------------------------ | ------------ | ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `SetRepositoryPolicy`    | ✅ Supported | Stores arbitrary IAM policy text                           | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_SetRepositoryPolicy.html)    |
| `GetRepositoryPolicy`    | ✅ Supported | Retrieves stored policy; returns 400 if none set           | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_GetRepositoryPolicy.html)    |
| `DeleteRepositoryPolicy` | ✅ Supported |                                                            | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_DeleteRepositoryPolicy.html) |
| `PutLifecyclePolicy`     | ✅ Supported | Stores lifecycle policy text for the repository            | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_PutLifecyclePolicy.html)     |
| `GetLifecyclePolicy`     | ✅ Supported | Retrieves stored lifecycle policy; returns 400 if none set | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_GetLifecyclePolicy.html)     |
| `DeleteLifecyclePolicy`  | ✅ Supported |                                                            | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_DeleteLifecyclePolicy.html)  |

### Tags

| Operation             | Status       | Notes                                  | AWS Docs                                                                                       |
| --------------------- | ------------ | -------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `TagResource`         | ✅ Supported | Adds/merges tags onto a repository ARN | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_TagResource.html)         |
| `UntagResource`       | ✅ Supported | Removes tag keys from a repository ARN | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_UntagResource.html)       |
| `ListTagsForResource` | ✅ Supported |                                        | [docs](https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_ListTagsForResource.html) |

## Related

- [ECR](../ecr.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
