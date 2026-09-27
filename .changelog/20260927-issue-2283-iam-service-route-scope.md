* [iam] Under `OVERCAST_ENFORCE_IAM`, a request a service's own route serves is authorised as that service's operation, not by its credential scope.
  `GET /clusters` signed for `sts` was served ungated; it is now `eks:ListClusters`, and `/tags/{resourceArn}` is the tag operation of the ARN's service.
  A request a service's route serves that no operation names is checked as `<prefix>:*` rather than let through; invocations of a deployed API stay ungated.
  S3 Tables' Iceberg REST catalog calls are checked as the `s3tables:` actions AWS lists for them.
