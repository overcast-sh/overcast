*! [iam/s3] Under `OVERCAST_ENFORCE_IAM`, an S3 request is authorised as the operation S3 serves, by the action AWS documents for it.
  `POST /bucket?delete` was not gated; it is `DeleteObjects`, checked as `s3:DeleteObject` on each key it names.
  `PUT /bucket?policy` and the other bucket sub-resources were checked as `s3:CreateBucket`; `x-id` no longer names the operation.
  `ListObjectsV2` and `HeadBucket` are `s3:ListBucket`, `HeadObject` is `s3:GetObject`, and multipart uploads are `s3:PutObject`.
  migration: a policy naming `s3:ListBuckets`, `s3:ListObjectsV2` or `s3:HeadObject` uses the AWS names: `s3:ListAllMyBuckets`, `s3:ListBucket`, `s3:GetObject`.
* [s3] Request logs name an S3 request as the operation S3 serves: `GET /bucket?prefix=logs/` is `ListObjects`, and `x-id` is ignored.
~. [iam] A `DeleteObjects` naming a key the caller may not delete is denied whole under `OVERCAST_ENFORCE_IAM`, and deletes nothing.
  AWS reports that key in `Errors` and deletes the rest.
* [s3] `DeleteObjects` reads at most 4 MiB of its body, and `x-amz-source-expected-bucket-owner` is checked only on `CopyObject` and `UploadPartCopy`.
