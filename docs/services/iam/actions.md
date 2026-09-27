---
title: "IAM actions"
description: "Which operation and which IAM action a request is checked as under OVERCAST_ENFORCE_IAM: action prefixes, S3's actions, and the credential scope."
section: "Service Reference"
tags:
  - docs
  - iam
  - services
---

# IAM actions

With [enforcement](../iam.md#request-time-enforcement-opt-in) on, a request is
checked as the IAM action AWS documents for the operation that serves it. Write
policies with the names the AWS documentation gives.

## Which operation

A request is the operation that serves it, whatever service its credential
scope names:

- An AWS Query call is the operation its `Action` and `Version` name:
  `CreateUser` signed for `s3` is `iam:CreateUser`.
- A path no service's route claims is S3's, whatever `Action` it carries:
  `PUT /my-bucket?Action=GetFederationToken` signed for `sts` is `s3:CreateBucket`.
- An S3 request is the operation S3 serves, chosen by its method, path and
  sub-resource: `PUT /my-bucket?policy` is `PutBucketPolicy`, not
  `CreateBucket`. The `x-id` query parameter the SDKs add plays no part, as it
  plays none in what S3 serves.
- A service's own route is that service's: `GET /clusters` signed for `sts`
  is `eks:ListClusters`, with or without an `?Action=`.
- A `/tags/{resourceArn}` call is the tag operation of the service the ARN
  names: an AppRegistry application's tags are `servicecatalog:` actions.

When a service's route serves a request no operation names, such as a POST to
an SQS queue URL with no `Action`, the request is checked as `<prefix>:*`, so
only a policy that allows the whole service allows it. An invocation of a
deployed API on API Gateway's own `/restapis/{id}/{stage}/_user_request_/` or
`/v2/apis/{id}/stages/{stage}/` route is not checked, as it is not on the
`execute-api` host; see [Limitations](./limitations.md#what-enforcement-does-not-see).

## Which action

The action is `<prefix>:<Operation>`, where the prefix is the one AWS uses. Ten
services differ from their Overcast service key:

| Service            | IAM action prefix       |
| ------------------ | ----------------------- |
| MSK                | `kafka:`                |
| Step Functions     | `states:`               |
| EFS                | `elasticfilesystem:`    |
| OpenSearch         | `es:`                   |
| ELBv2              | `elasticloadbalancing:` |
| AppRegistry        | `servicecatalog:`       |
| Cognito user pools | `cognito-idp:`          |
| WAF                | `wafv2:`                |
| DynamoDB Streams   | `dynamodb:`             |
| AppConfig Data     | `appconfig:`            |

Where AWS checks an action not named after the operation, that action is
checked. Lambda's three invoke operations are `lambda:InvokeFunction`. S3
follows AWS's
[required permissions for S3 API operations](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html):

| S3 operation                                                     | Action checked                                             |
| ---------------------------------------------------------------- | ---------------------------------------------------------- |
| `ListBuckets`                                                    | `s3:ListAllMyBuckets`                                      |
| `ListObjects`, `ListObjectsV2`, `HeadBucket`                     | `s3:ListBucket`                                            |
| `HeadObject`, `GetObjectAttributes`                              | `s3:GetObject`                                             |
| `CreateMultipartUpload`, `UploadPart`, `CompleteMultipartUpload` | `s3:PutObject`                                             |
| `ListParts`                                                      | `s3:ListMultipartUploadParts`                              |
| `CopyObject`, `UploadPartCopy`                                   | `s3:PutObject` on the target, `s3:GetObject` on the source |
| `DeleteObjects`                                                  | `s3:DeleteObject` on each key it names                     |
| `DeleteBucketCors` and the other configuration deletes           | The put action, such as `s3:PutBucketCORS`                 |

A `versionId` selects the version action where AWS has one, such as
`s3:GetObjectVersion` or `s3:DeleteObjectVersion`.

AWS answers a `DeleteObjects` key the caller may not delete with an entry in
`Errors` and deletes the rest. Overcast refuses the whole request with
`AccessDenied`, and deletes nothing.

### S3 Tables' Iceberg REST catalog

A call to the [Iceberg REST endpoint](https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables-integrating-open-source.html)
checks every action AWS lists for it. Loading a table, for example, needs both
`s3tables:GetTableMetadataLocation` and `s3tables:GetTableData`:

| Iceberg operation        | Actions checked                                                                        |
| ------------------------ | -------------------------------------------------------------------------------------- |
| `getConfig`              | `s3tables:GetTableBucket`                                                              |
| `listNamespaces`         | `s3tables:ListNamespaces`                                                              |
| `createNamespace`        | `s3tables:CreateNamespace`                                                             |
| `loadNamespaceMetadata`, `namespaceExists` | `s3tables:GetNamespace`                                              |
| `dropNamespace`          | `s3tables:DeleteNamespace`                                                             |
| `listTables`             | `s3tables:ListTables`                                                                  |
| `createTable`            | `s3tables:CreateTable`, `s3tables:PutTableData`                                        |
| `loadTable`              | `s3tables:GetTableMetadataLocation`, `s3tables:GetTableData`                           |
| `updateTable`            | `s3tables:UpdateTableMetadataLocation`, `s3tables:PutTableData`, `s3tables:GetTableData` |
| `tableExists`            | `s3tables:GetTable`                                                                    |
| `dropTable`              | `s3tables:DeleteTable`                                                                 |
| `renameTable`            | `s3tables:RenameTable`                                                                 |

AWS does not serve `updateProperties`, `registerTable` or `reportMetrics`.
Overcast does, and checks them as `s3tables:*`.

## Related

- [IAM](../iam.md) — enforcement, and the error each protocol gets
- [IAM troubleshooting](./troubleshooting.md) — `AccessDenied` after switching enforcement on
