---
title: "S3 operations"
description: "Every S3 operation Overcast declares — 45 of 53 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - operations
  - s3
  - services
---

<!-- BEGIN overcast:capabilities -->

# S3 operations

45 of 53 listed operations are implemented. Back to [S3](../s3.md).

## Summary

| Category          | ✅ Supported | ⚠️ Partial | ❌ Unsupported |
| ----------------- | ------------ | ---------- | -------------- |
| Buckets           | 8            |            |                |
| CORS              | 1            | 2          |                |
| Website           | 1            | 2          |                |
| Objects           | 11           |            | 3              |
| Multipart uploads | 6            |            | 1              |
| ACLs & policies   | 3            |            | 4              |
| Versioning        | 3            |            |                |
| Tagging           | 3            |            |                |
| Lifecycle         | 2            | 1          |                |
| Notifications     | 2            |            |                |

---

## Endpoints

### Buckets

| Operation                | Status       | Notes                                                                    | AWS Docs                                                                                |
| ------------------------ | ------------ | ------------------------------------------------------------------------ | --------------------------------------------------------------------------------------- |
| `CreateBucket`           | ✅ Supported | Account regional namespaces via x-amz-bucket-namespace: account-regional | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CreateBucket.html)           |
| `DeleteBucket`           | ✅ Supported | Bucket must be empty                                                     | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucket.html)           |
| `HeadBucket`             | ✅ Supported |                                                                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadBucket.html)             |
| `ListBuckets`            | ✅ Supported |                                                                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListBuckets.html)            |
| `GetBucketLocation`      | ✅ Supported |                                                                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketLocation.html)      |
| `GetBucketEncryption`    | ✅ Supported | Returns default SSE-S3 config; stores AES256/KMS bucket encryption rules | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketEncryption.html)    |
| `PutBucketEncryption`    | ✅ Supported | Stores AES256/KMS bucket encryption rules                                | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketEncryption.html)    |
| `DeleteBucketEncryption` | ✅ Supported |                                                                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketEncryption.html) |

### CORS

| Operation          | Status       | Notes                                    | AWS Docs                                                                          |
| ------------------ | ------------ | ---------------------------------------- | --------------------------------------------------------------------------------- |
| `GetBucketCors`    | ⚠️ Partial   | CORS rules; rule Id is not yet preserved | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketCors.html)    |
| `PutBucketCors`    | ⚠️ Partial   | CORS rules; rule Id is not yet preserved | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketCors.html)    |
| `DeleteBucketCors` | ✅ Supported |                                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketCors.html) |

### Website

| Operation             | Status       | Notes                                                                                                                                                                                                                  | AWS Docs                                                                             |
| --------------------- | ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `GetBucketWebsite`    | ⚠️ Partial   | Returns the whole configuration — IndexDocument, ErrorDocument, RedirectAllRequestsTo and RoutingRules; Overcast serves no website endpoint, so nothing is actually redirected                                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketWebsite.html)    |
| `PutBucketWebsite`    | ⚠️ Partial   | Stores IndexDocument, ErrorDocument, RedirectAllRequestsTo and RoutingRules with AWS's mutual exclusion and Protocol enum enforced; HttpRedirectCode values are not validated, and Overcast serves no website endpoint | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketWebsite.html)    |
| `DeleteBucketWebsite` | ✅ Supported |                                                                                                                                                                                                                        | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketWebsite.html) |

### Objects

| Operation             | Status         | Notes                                                                                                                                                                                                                                                 | AWS Docs                                                                             |
| --------------------- | -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `PutObject`           | ✅ Supported   | Stores body + x-amz-meta-* headers; If-None-Match: * and If-Match make the write conditional, failing 412 — or 404 when If-Match finds no current version                                                                                             | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html)           |
| `GetObject`           | ✅ Supported   | Returns body, ETag, metadata headers; versionId selects a specific version, and a delete-marked key answers 404 with x-amz-delete-marker                                                                                                              | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html)           |
| `HeadObject`          | ✅ Supported   |                                                                                                                                                                                                                                                       | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html)          |
| `DeleteObject`        | ✅ Supported   | Idempotent — 204 for missing keys; in a versioned bucket a delete adds a delete marker, and versionId removes one version permanently                                                                                                                 | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObject.html)        |
| `CopyObject`          | ✅ Supported   | x-amz-copy-source may name a source versionId                                                                                                                                                                                                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CopyObject.html)          |
| `ListObjectsV2`       | ✅ Supported   | prefix, delimiter, max-keys (validated, clamped to 1000), start-after, continuation-token, encoding-type=url and fetch-owner; x-amz-request-payer and x-amz-optional-object-attributes accepted but Requester Pays and RestoreStatus are not emulated | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html)       |
| `DeleteObjects`       | ✅ Supported   | Batch delete up to 1000 keys; quiet mode supported; per-entry VersionId, DeleteMarker and DeleteMarkerVersionId reported                                                                                                                              | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObjects.html)       |
| `ListObjects`         | ✅ Supported   | Marker-based pagination; supports prefix, delimiter, encoding-type=url and the same max-keys validation as ListObjectsV2                                                                                                                              | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjects.html)         |
| `GetObjectAttributes` | ❌ Unsupported | stub; returns 501                                                                                                                                                                                                                                     | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectAttributes.html) |
| `PutObjectTagging`    | ✅ Supported   | Up to 10 tags, unique keys, key 1-128 and value 0-256 characters; the tag character set is not validated                                                                                                                                              | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectTagging.html)    |
| `GetObjectTagging`    | ✅ Supported   |                                                                                                                                                                                                                                                       | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectTagging.html)    |
| `DeleteObjectTagging` | ✅ Supported   |                                                                                                                                                                                                                                                       | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObjectTagging.html) |
| `RestoreObject`       | ❌ Unsupported | Glacier restore simulation                                                                                                                                                                                                                            | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_RestoreObject.html)       |
| `SelectObjectContent` | ❌ Unsupported | S3 Select (SQL queries on objects)                                                                                                                                                                                                                    | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_SelectObjectContent.html) |

### Multipart uploads

| Operation                 | Status         | Notes                                                                                                                                                                                                                                                                                                                                                   | AWS Docs                                                                                 |
| ------------------------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| `CreateMultipartUpload`   | ✅ Supported   |                                                                                                                                                                                                                                                                                                                                                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CreateMultipartUpload.html)   |
| `UploadPart`              | ✅ Supported   |                                                                                                                                                                                                                                                                                                                                                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPart.html)              |
| `UploadPartCopy`          | ❌ Unsupported | stub; returns 501                                                                                                                                                                                                                                                                                                                                       | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPartCopy.html)          |
| `CompleteMultipartUpload` | ✅ Supported   | Validates the parts list as AWS does: a wrong or unknown ETag is InvalidPart, out-of-order parts InvalidPartOrder, a non-final part under 5 MiB EntityTooSmall, an empty list MalformedXML; a refused completion leaves the upload intact. Honours the same If-None-Match: * and If-Match conditional writes as PutObject, evaluated at completion time | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CompleteMultipartUpload.html) |
| `AbortMultipartUpload`    | ✅ Supported   |                                                                                                                                                                                                                                                                                                                                                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_AbortMultipartUpload.html)    |
| `ListMultipartUploads`    | ✅ Supported   |                                                                                                                                                                                                                                                                                                                                                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListMultipartUploads.html)    |
| `ListParts`               | ✅ Supported   |                                                                                                                                                                                                                                                                                                                                                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListParts.html)               |

### ACLs & policies

| Operation            | Status         | Notes             | AWS Docs                                                                            |
| -------------------- | -------------- | ----------------- | ----------------------------------------------------------------------------------- |
| `GetBucketAcl`       | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketAcl.html)       |
| `PutBucketAcl`       | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketAcl.html)       |
| `GetObjectAcl`       | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectAcl.html)       |
| `PutObjectAcl`       | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectAcl.html)       |
| `GetBucketPolicy`    | ✅ Supported   |                   | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketPolicy.html)    |
| `PutBucketPolicy`    | ✅ Supported   |                   | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketPolicy.html)    |
| `DeleteBucketPolicy` | ✅ Supported   |                   | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketPolicy.html) |

### Versioning

| Operation             | Status       | Notes                                                                                                                                                            | AWS Docs                                                                             |
| --------------------- | ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `GetBucketVersioning` | ✅ Supported |                                                                                                                                                                  | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketVersioning.html) |
| `PutBucketVersioning` | ✅ Supported | Enabled and Suspended, with AWS's semantics for both; objects that predate the change become their key's null version                                            | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketVersioning.html) |
| `ListObjectVersions`  | ✅ Supported | Versions and delete markers in AWS's order (key ascending, then most recent first), with prefix, delimiter, max-keys and key-marker/version-id-marker pagination | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectVersions.html)  |

### Tagging

| Operation             | Status       | Notes                                                                                                    | AWS Docs                                                                             |
| --------------------- | ------------ | -------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `GetBucketTagging`    | ✅ Supported |                                                                                                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketTagging.html)    |
| `PutBucketTagging`    | ✅ Supported | Up to 50 tags, unique keys, key 1-128 and value 0-256 characters; the tag character set is not validated | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketTagging.html)    |
| `DeleteBucketTagging` | ✅ Supported |                                                                                                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketTagging.html) |

### Lifecycle

| Operation                         | Status       | Notes                                                                                                                                                                                                                                                                                                                                                                                                                       | AWS Docs                                                                                         |
| --------------------------------- | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `GetBucketLifecycleConfiguration` | ✅ Supported | NoSuchLifecycleConfiguration when none is set; reports x-amz-transition-default-minimum-object-size                                                                                                                                                                                                                                                                                                                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketLifecycleConfiguration.html) |
| `PutBucketLifecycleConfiguration` | ⚠️ Partial   | Expiration, Transition, NoncurrentVersionExpiration, NoncurrentVersionTransition, ExpiredObjectDeleteMarker, AbortIncompleteMultipartUpload and prefix/tag/size filters are applied by an hourly sweeper; x-amz-transition-default-minimum-object-size gates transitions of objects under 128 KB, noncurrent ones included; expiring the current version of a versioned object adds a delete marker rather than deleting it | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketLifecycleConfiguration.html) |
| `DeleteBucketLifecycle`           | ✅ Supported |                                                                                                                                                                                                                                                                                                                                                                                                                             | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketLifecycle.html)           |

### Notifications

| Operation                            | Status       | Notes                                                                                                                                                                                                                                                                                                                                                                                            | AWS Docs                                                                                            |
| ------------------------------------ | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------- |
| `GetBucketNotificationConfiguration` | ✅ Supported | Returns empty config if none set                                                                                                                                                                                                                                                                                                                                                                 | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketNotificationConfiguration.html) |
| `PutBucketNotificationConfiguration` | ✅ Supported | SQS, SNS, Lambda and EventBridge destinations; prefix/suffix filters. Records carry versionId and sequencer; SNS deliveries carry the Records JSON as the notification envelope's Message string with Subject "Amazon S3 Notification", as real S3 does; EventBridge events carry AWS's Object Created/Object Deleted shape, including deletion-type, minus the fields Overcast has no value for | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketNotificationConfiguration.html) |

## Related

- [S3](../s3.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
