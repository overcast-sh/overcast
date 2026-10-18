* [lambda-web] Web Function endpoints serve HTTP: each revision runs as a Node.js server in Docker, started on demand and stopped when idle (#2302).
  requests are routed by the endpoint's `domainName`, scaled by `scalingConfig` and `throttleConfig`, and checked for `IamAuth` endpoints with SigV4/SigV4a, IAM and resource policies.
  logs, `AWS/Lambda/Web` metrics and `lambda-web:*` bus events are emitted; the system map shows running environments.
  new settings `LAMBDA_WEB_MAX_ENVIRONMENTS`, `LAMBDA_WEB_IDLE_TTL` and `LAMBDA_WEB_ENVIRONMENT_MEMORY_MB`; running code needs a Docker daemon.
* [core] a handler that aborts with `http.ErrAbortHandler`, such as a proxied stream whose upstream died, now cuts the connection instead of having an AWS error appended to the half-sent body (#2302).
