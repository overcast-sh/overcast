* [iam] IAM enforcement answers a denied JSON-protocol call (Athena, Glue, KMS, SSM, ECS, ...) in JSON, so SDKs read `AccessDeniedException`.
  the denial follows the request's wire protocol: AWS JSON and Smithy RPC v2 answer 400, REST-JSON 403, Query 403 `AccessDenied`.
  EC2 answers `UnauthorizedOperation` in its own `<Response><Errors>` envelope; CloudFront and Route 53 wrap theirs in `<ErrorResponse>`.
