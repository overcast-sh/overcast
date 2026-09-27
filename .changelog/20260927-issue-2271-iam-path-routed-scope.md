* [iam] Under `OVERCAST_ENFORCE_IAM`, a request served by its path is authorised as the operation that serves it, not by its credential scope or `?Action=`.
  `PUT /bucket?Action=GetFederationToken` signed for `sts` was checked as `sts:GetFederationToken`; it is S3's `CreateBucket` and is now checked as `s3:CreateBucket`.
  A query-string `Action` names an operation only for a service AWS Query can address: `GET /clusters?Action=DeleteCluster` signed for `eks` is `eks:ListClusters`.
