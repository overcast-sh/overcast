* [lambda-web] Lambda Web Function endpoints serve HTTP: each revision runs as a Node.js server in a Docker container, started on the first request and stopped when idle (#2302).
  requests are routed by the endpoint's `domainName`, scaled by `scalingConfig` and `throttleConfig`, and authorised for `IamAuth` endpoints with SigV4/SigV4a, IAM and resource policies.
  the revision's logs and `platform.*` system logs reach CloudWatch Logs, `AWS/Lambda/Web` metrics reach CloudWatch, and `lambda-web:*` events reach the bus; the system map shows running environments.
  new settings `LAMBDA_WEB_MAX_ENVIRONMENTS`, `LAMBDA_WEB_IDLE_TTL` and `LAMBDA_WEB_ENVIRONMENT_MEMORY_MB`; a Docker daemon is required to run code.
