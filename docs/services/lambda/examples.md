---
title: "Lambda examples"
description: "Hot-reloading local source into a running function, deploying a container image through the emulated ECR, supplying layers from a cache or from real AWS, and running extensions that call back into Overcast."
section: "Service Reference"
tags:
  - docs
  - examples
  - lambda
  - services
---

# Lambda examples

Worked function setups past the [Lambda quick start](../lambda.md#quick-start):
hot reload, container images, layers and extensions.

## Hot reload

A local source directory mounted at `/var/task`, so edits are picked up on the
next invoke without uploading a new zip. Opt-in, and meant for the interpreted
runtimes — Node.js and Python; use the normal zip or image deploy path when you
need production-like packaging.

### `cdk watch`, when you would rather configure nothing

```bash
AWS_ENDPOINT_URL=http://localhost:4566 cdk watch
```

Each save calls `UpdateFunctionCode` on the changed function assets, which
invalidates the warm pool entry. No tag, no bind mount, no Docker file-sharing
configuration, and it works with every runtime and bundler.

### The bind mount, when you want no redeploy cycle at all

```bash
OVERCAST_LAMBDA_HOT_RELOAD=true overcast serve

aws lambda create-function --function-name demo-hot \
  --runtime nodejs20.x --handler index.handler \
  --role arn:aws:iam::000000000000:role/lambda-role \
  --zip-file fileb://minimal.zip \
  --tags overcast:hot-reload-path=/absolute/path/to/lambda/source
```

Invoke, edit files in that path, invoke again. `OVERCAST_HOT_RELOAD` turns it on
for every compute service at once — see
[The inner loop § Turning hot reload on](../../local-dev.md#turning-hot-reload-on).

| Rule | Detail |
| --- | --- |
| Absolute paths only | A relative path is refused |
| Windows paths normalised | `C:\Users\you\app` becomes `/c/Users/you/app` |
| Read-only | Mounted at `/var/task:ro` |
| Permissions | Host files must be readable by the runtime user in the container |

### The same tag, in CDK

```typescript
const fn = new lambda.Function(this, "MyFn", {
  runtime: lambda.Runtime.NODEJS_24_X,
  handler: "src/handler.handler",
  code: lambda.Code.fromAsset(path.join(__dirname, "src")),
});

cdk.Tags.of(fn).add("overcast:hot-reload-path", path.resolve(__dirname, "src"));
```

It activates after `cdk deploy`. Node.js 24 strips TypeScript types natively, so
raw `.ts` can be mounted; for Node.js 22 and earlier point the tag at compiled
output (`path.resolve(__dirname, "dist")`) and keep it fresh with your bundler —
Overcast logs a `WARN` at container acquire time if it finds `.ts` files and no
`.js` files there. What retires a warm container, and the bounds on the check
that decides it, are in
[The inner loop § What counts as a change](../../local-dev.md#what-counts-as-a-change).

## Step debugging

Set breakpoints in the handler from your editor with one flag and one tag:

```bash
OVERCAST_DEBUGGER=true overcast serve

aws lambda tag-resource \
  --resource arn:aws:lambda:us-east-1:000000000000:function:demo-hot \
  --tags overcast:debug=true
```

Overcast starts the container with the runtime's debug flag set and listens on
`127.0.0.1`, on the lowest free port from 9229 upward, for VS Code, a JetBrains
IDE or Chrome DevTools, and the function's timeout clock stops while a debugger
is attached. Runtimes, path
mappings and the `launch.json` entry are in
[Step debugging inside emulated compute](../../debugger.md).

## Container images

A `PackageType=Image` function runs from an image you pushed to Overcast's
[ECR](../ecr.md). Build on an AWS Lambda base image exactly as you would for real
AWS — the base image's Runtime Interface Client is what Overcast drives.

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

cat > Dockerfile <<'EOF'
FROM public.ecr.aws/lambda/nodejs:20
COPY app.js ${LAMBDA_TASK_ROOT}/
CMD ["app.handler"]
EOF

URI=$(aws ecr create-repository --repository-name my-fn \
  --query 'repository.repositoryUri' --output text)   # localhost:4510/000000000000/my-fn

docker build -t my-fn .
aws ecr get-login-password | docker login --username AWS --password-stdin "${URI%%/*}"
docker tag my-fn "$URI:v1"
docker push "$URI:v1"

aws lambda create-function --function-name my-fn --package-type Image \
  --role arn:aws:iam::000000000000:role/lambda-role \
  --code ImageUri=000000000000.dkr.ecr.us-east-1.amazonaws.com/my-fn:v1

aws lambda invoke --function-name my-fn --payload '{}' \
  --cli-binary-format raw-in-base64-out out.json && cat out.json
```

**Push to `repositoryUri`, deploy the `amazonaws.com` URI.** Both address the
same repository: the first is where the Docker daemon reaches the registry, the
second is what AWS and CDK write. Either form works in `ImageUri`.

### Moving the function to a new image

```bash
aws lambda update-function-configuration --function-name my-fn \
  --image-config 'Command=["other.handler"]'

aws lambda update-function-code --function-name my-fn \
  --image-uri 000000000000.dkr.ecr.us-east-1.amazonaws.com/my-fn:v2

aws lambda wait function-updated --function-name my-fn
```

`ImageConfig` overrides the image's own `ENTRYPOINT`, `CMD` and `WORKDIR`. The
new image is pulled in the background, so `update-function-code` answers
`LastUpdateStatus: InProgress` and the wait is what tells you the pull landed —
or that it failed, with `ImageAccessDenied` or `InvalidImage` in
`LastUpdateStatusReasonCode`. Every other update settles before the call
returns; see [Limitations](limitations.md#update-status).

CDK's `DockerImageFunction` with `DockerImageCode.fromImageAsset` needs none of
this by hand: `cdk deploy` builds the image, pushes it to the repository
`cdk bootstrap` created, and writes the `amazonaws.com` URI itself. See
[CDK § Container assets](../../cdk/limitations.md#container-assets-are-served-from-overcasts-own-registry).

## Layers

Layer ARNs from CDK or CloudFormation are injected into `/opt` before the runtime
starts, matching real Lambda. Layers published locally with
`PublishLayerVersion` resolve automatically; when several layers provide the same
path, later entries in the function's `Layers` list win.

A layer ARN that is neither local nor cache-backed fails before the cold start,
with a Lambda init-style error:

```
{"errorMessage":"Failed to load Lambda layer arn:aws:lambda:...: layer version not found","errorType":"Runtime.InitError"}
```

### Option 1 — pre-download the layer (no AWS credentials)

```bash
LAYER_URL=$(aws lambda get-layer-version-by-arn \
  --arn "arn:aws:lambda:ap-southeast-2:094274105915:layer:AWSLambdaPowertoolsTypeScriptV2:22" \
  --query 'Content.Location' --output text)

curl -o AWSLambdaPowertoolsTypeScriptV2_22.zip "$LAYER_URL"
mkdir -p .overcast/layers && mv AWSLambdaPowertoolsTypeScriptV2_22.zip .overcast/layers/
```

```yaml
services:
  overcast:
    image: overcast:dev
    volumes:
      - "./.overcast:/data" # or ./.overcast/layers:/data/layers:ro
      - "/var/run/docker.sock:/var/run/docker.sock"
```

| Detail | Value |
| --- | --- |
| Cache directory | `{OVERCAST_DATA_DIR}/layers` — `/data/layers` in the standard image. `LAMBDA_LAYER_CACHE_DIR` overrides it, mainly for the native binary outside Docker |
| Filename | `{LayerName}_{Version}.zip`, derived from the ARN: `arn:…:layer:AWS-Parameters-and-Secrets-Lambda-Extension:11` becomes `AWS-Parameters-and-Secrets-Lambda-Extension_11.zip` |
| Foreign-account ARNs | The same lookup satisfies the invoke-time existence check, so you never need to publish a local replacement layer |

### Option 2 — fetch from real AWS (needs credentials)

```yaml
services:
  overcast:
    image: overcast:dev
    environment:
      - LAMBDA_FETCH_REMOTE_LAYERS=true
      - LAMBDA_REMOTE_AWS_ACCESS_KEY_ID=AKIA...
      - LAMBDA_REMOTE_AWS_SECRET_ACCESS_KEY=...
      - LAMBDA_REMOTE_AWS_SESSION_TOKEN=... # if using SSO or an assumed role
```

Overcast downloads a missing layer at invocation time and caches it. The
credentials need `lambda:GetLayerVersion`, are **separate** from the
`AWS_ACCESS_KEY_ID=test` credentials Overcast's own APIs take, and are never
handed to Lambda containers. A remotely fetched layer is cached under a
content-addressed name rather than the friendly one, so do not expect
`{LayerName}_{Version}.zip` to appear.

## Extensions

Docker-backed functions — zip and container image alike — start executables found
directly under `/opt/extensions` before the runtime entrypoint, as children of the
same in-container init that owns the runtime. Layer file modes are preserved, so
extension binaries must be executable in the layer zip.

| Path | Purpose |
| --- | --- |
| `POST /2020-01-01/extension/register` | Registration |
| `GET /2020-01-01/extension/event/next` | Event loop |
| `POST /2020-01-01/extension/init/error` | Init failure |
| `POST /2020-01-01/extension/exit/error` | Exit failure |
| `PUT /2020-08-15/logs` | Logs API subscription |
| `PUT /2022-07-01/telemetry` | Telemetry API subscription |

| Behaviour | Detail |
| --- | --- |
| `INVOKE` events | Reach only extensions in the container that accepted the invocation |
| `SHUTDOWN` | Sent when Overcast tears a warm container down |
| One API per extension | An extension subscribed through one is refused by the other, as on AWS |
| `schemaVersion` | `2022-07-01`, `2022-12-13` or `2025-01-29`. The Telemetry API refuses an unknown one rather than half-honouring it |
| Record kinds | Both surfaces deliver HTTP destinations for `platform`, `function` and `extension` records. Function stdout and stderr arrive as `function`; the synthesised invocation records arrive as `platform` |
| JSON log lines | From `schemaVersion: 2022-12-13` a `LogFormat: JSON` function's line is embedded as the JSON object it already is. Older schemas, the Logs API, Text-format functions and non-object lines receive the string |
| Subscription records | `platform.extension` at each registration, and `platform.telemetrySubscription` or `platform.logsSubscription` on subscribing. Neither is written to CloudWatch |
| Log levels | Subscribers receive **every** record regardless of `ApplicationLogLevel` and `SystemLogLevel`, which filter CloudWatch Logs and the invoke tail only |
| Delivery | At-least-once and batched, cut at the subscription's `maxItems`, `maxBytes` or `timeoutMs`. A transport failure is retried with backoff |
| A destination that fails every attempt | Loses that batch, and is told: a `platform.logsDropped` event opens the next batch carrying `reason`, `droppedRecords` and `droppedBytes` |

### Extensions that call AWS

```text
arn:aws:lambda:ap-southeast-2:665172237481:layer:AWS-Parameters-and-Secrets-Lambda-Extension:90
arn:aws:lambda:ap-southeast-2:665172237481:layer:AWS-Parameters-and-Secrets-Lambda-Extension-Arm64:90
```

Reference extensions by layer version in your IaC, the form CDK, CloudFormation
and Lambda ARNs expose. The AWS Parameters and Secrets Lambda Extension for
`ap-southeast-2` was verified at layer version `90`.

| Rule | Detail |
| --- | --- |
| Endpoint variables are injected | `AWS_ENDPOINT_URL`, `AWS_ENDPOINT_URL_SSM`, `AWS_ENDPOINT_URL_SECRETS_MANAGER`, `AWS_REGION` and the `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` triple, so an endpoint-aware extension reaches the emulator |
| Match the **function's** architecture | An `x86_64` function needs the x86_64 layer even on Apple Silicon |
| No secret cache in front of the extension | The binary keeps its own environment-local cache, as on AWS, so a warm environment can return a prior value until `SECRETS_MANAGER_TTL` expires. Set the TTL to `0` when every request must read the current `AWSCURRENT` value |

## Related

- [Lambda](../lambda.md) — quick start and what works
- [Lambda limitations](./limitations.md) — concurrency, runtimes, logging, VPC placement
- [Lambda troubleshooting](./troubleshooting.md) — throttles, layer errors, extension endpoints
- [The inner loop](../../local-dev.md) — hot reload across services
- [Step debugging inside emulated compute](../../debugger.md) — breakpoints inside the handler
- [Debugging in the console](../../debugger-console.md) — the same breakpoints from the function page's Code tab
- [Egress modes](../../networking/egress.md) — what a function can reach outside the machine
