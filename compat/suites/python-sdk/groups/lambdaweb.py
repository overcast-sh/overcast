"""
groups/lambdaweb.py — Lambda Web Functions compatibility test implementations for the Python suite.

Lambda Web Functions is its own service ("lambda-web", API version 2025-03-07),
separate from Lambda: boto3 names it ``lambda-web`` and signs for "lambda".
"""

from __future__ import annotations

import io
import json
import threading
import time
import zipfile
from http import client as http_client
from urllib.parse import urlsplit

from botocore.exceptions import ClientError

from lib.harness import TestContext
from lib.clients import make_clients

_ROLE_ARN = "arn:aws:iam::000000000000:role/compat-web-role"
_LIVE = "live"
_CANARY = "canary"


def _web(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region)._get("lambda-web")


def _fn_name(ctx: TestContext) -> str:
    """The run's web function: letters, digits, hyphens and underscores, at most 64."""
    return f"{ctx.run_id}-lambda-web-functions"


def _function_arn(ctx: TestContext) -> str:
    arn = ctx.get("lw_function_arn")
    if not arn:
        raise AssertionError("no function ARN from CreateWebFunction")
    return arn


def _revision_ids(ctx: TestContext) -> tuple[str, str]:
    first, second = ctx.get("lw_revision_1"), ctx.get("lw_revision_2")
    if not first or not second:
        raise AssertionError(f"missing revision IDs (first={first!r} second={second!r})")
    return first, second


def _code_bucket(ctx: TestContext) -> str:
    """The group's versioned bucket holding the revisions' packages."""
    return f"{ctx.run_id}-lambda-web-functions"


def _s3(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region).s3


def _zip(files: dict) -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        for name, body in files.items():
            zf.writestr(name, body)
    return buf.getvalue()


def _seed_bucket(ctx: TestContext, bucket: str, keys: list, files: dict) -> None:
    """A versioned bucket with each key holding a zip of the given files.

    CreateWebFunctionRevision checks that the bucket is versioned and the
    object exists, so even the control-plane tests upload a real package.
    """
    s3 = _s3(ctx)
    s3.create_bucket(Bucket=bucket)
    s3.put_bucket_versioning(Bucket=bucket, VersioningConfiguration={"Status": "Enabled"})
    body = _zip(files)
    for key in keys:
        s3.put_object(Bucket=bucket, Key=key, Body=body)


def _delete_bucket(ctx: TestContext, bucket: str) -> None:
    """Empty a versioned bucket (versions and delete markers) and delete it."""
    s3 = _s3(ctx)
    try:
        for page in s3.get_paginator("list_object_versions").paginate(Bucket=bucket):
            for v in page.get("Versions", []) + page.get("DeleteMarkers", []):
                s3.delete_object(Bucket=bucket, Key=v["Key"], VersionId=v["VersionId"])
    except Exception:
        pass
    try:
        s3.delete_bucket(Bucket=bucket)
    except Exception:
        pass


def _eventually(what: str, timeout: float, check) -> None:
    """Poll check() until it returns truthy. Revisions and endpoints settle
    asynchronously, as they do on AWS, so a test waits the way the SDK waiters
    would."""
    deadline = time.monotonic() + timeout
    while True:
        if check():
            return
        if time.monotonic() > deadline:
            raise AssertionError(f"{what}: not reached within {timeout:g}s")
        time.sleep(0.1)


def _wait_revision_active(ctx: TestContext, function_name: str, revision_id: str) -> None:
    def check() -> bool:
        resp = _web(ctx).get_web_function_revision(functionName=function_name, revisionId=revision_id)
        if resp.get("state") == "Failed":
            raise AssertionError(f"revision {revision_id} is Failed: {resp.get('errors')}")
        return resp.get("state") == "Active"

    _eventually(f"revision {revision_id} to become Active", 30, check)


def _build_config(ctx: TestContext, key: str) -> dict:
    return {
        "codeConfig": {"s3Object": {"bucket": _code_bucket(ctx), "key": key}},
        "runtimeConfig": {"runtime": "nodejs24.x"},
    }


def _policy(function_arn: str, principal: str) -> str:
    return (
        '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":%s,'
        '"Action":"lambda:InvokeWebFunctionEndpoint","Resource":"%s/endpoint/%s"}]}'
        % (principal, function_arn, _LIVE)
    )


def _expect_error(what: str, call, code: str) -> ClientError:
    try:
        call()
    except ClientError as err:
        got = err.response.get("Error", {}).get("Code", "")
        if got != code:
            raise AssertionError(f"{what}: expected {code}, got {got!r} ({err})")
        return err
    raise AssertionError(f"{what}: expected {code}, got success")


def _get_endpoint(ctx: TestContext, name: str) -> dict:
    return _web(ctx).get_web_function_endpoint(functionName=_fn_name(ctx), endpointName=name)


# ── lambda-web-functions ──────────────────────────────────────────────────────

def CreateWebFunction(ctx: TestContext) -> None:
    name = _fn_name(ctx)
    resp = _web(ctx).create_web_function(
        functionName=name,
        revisionConfig={
            "buildConfig": _build_config(ctx, "app-v1.zip"),
            "serviceConfig": {"executionRoleArn": _ROLE_ARN},
        },
        endpointConfig={
            "endpointName": _LIVE,
            "endpointType": "HomeRegion",
            "authType": "ApplicationManaged",
            "autoDeploymentMode": "LatestRevision",
        },
    )
    arn = resp.get("functionArn", "")
    if not arn.startswith("arn:aws:lambda:") or not arn.endswith(":web-function/" + name):
        raise AssertionError(f"CreateWebFunction: unexpected functionArn {arn!r}")
    if resp.get("functionName") != name:
        raise AssertionError(f"CreateWebFunction: functionName {resp.get('functionName')!r}, want {name!r}")
    revision = resp.get("revision") or {}
    if not revision.get("revisionId"):
        raise AssertionError("CreateWebFunction: no first revision in the response")
    endpoint = resp.get("endpoint") or {}
    if (
        endpoint.get("endpointName") != _LIVE
        or endpoint.get("autoDeploymentMode") != "LatestRevision"
        or not endpoint.get("domainName")
    ):
        raise AssertionError(f"CreateWebFunction: endpoint {endpoint}")
    ctx["lw_function_arn"] = arn
    ctx["lw_revision_1"] = revision["revisionId"]


def GetWebFunction(ctx: TestContext) -> None:
    arn = _function_arn(ctx)
    resp = _web(ctx).get_web_function(functionName=_fn_name(ctx))
    if resp.get("functionArn") != arn or resp.get("functionName") != _fn_name(ctx):
        raise AssertionError(f"GetWebFunction: got {resp.get('functionArn')!r} / {resp.get('functionName')!r}")
    if resp.get("state") != "Active":
        raise AssertionError(f"GetWebFunction: state {resp.get('state')!r}, want Active")


def ListWebFunctions(ctx: TestContext) -> None:
    name = _fn_name(ctx)
    kwargs: dict = {}
    while True:
        resp = _web(ctx).list_web_functions(**kwargs)
        for fn in resp.get("functions", []):
            if fn.get("functionName") == name:
                if fn.get("functionArn") != ctx.get("lw_function_arn"):
                    raise AssertionError(f"ListWebFunctions: {name!r} listed with ARN {fn.get('functionArn')!r}")
                return
        token = resp.get("nextToken")
        if not token:
            raise AssertionError(f"ListWebFunctions: {name!r} not listed")
        kwargs["nextToken"] = token


def GetWebFunctionRevision(ctx: TestContext) -> None:
    first = ctx.get("lw_revision_1")
    if not first:
        raise AssertionError("no revision from CreateWebFunction")
    # A revision is Pending while its package is fetched and checked.
    _wait_revision_active(ctx, _fn_name(ctx), first)
    resp = _web(ctx).get_web_function_revision(functionName=_fn_name(ctx), revisionId=first)
    if resp.get("revisionId") != first or resp.get("state") != "Active":
        raise AssertionError(f"GetWebFunctionRevision: got {resp.get('revisionId')!r} in state {resp.get('state')!r}")
    if not resp.get("revisionArn", "").endswith("/revision/" + first):
        raise AssertionError(f"GetWebFunctionRevision: revisionArn {resp.get('revisionArn')!r}")
    # Settings the call did not give come back as AWS's documented defaults.
    service = resp.get("serviceConfig") or {}
    if service.get("timeoutSeconds") != 30 or service.get("maxConcurrencyPerEnvironment") != 64:
        raise AssertionError(f"GetWebFunctionRevision: serviceConfig {service}, want timeout 30 and concurrency 64")
    if service.get("executionRoleArn") != _ROLE_ARN:
        raise AssertionError(f"GetWebFunctionRevision: executionRoleArn {service.get('executionRoleArn')!r}")
    s3_object = ((resp.get("buildConfig") or {}).get("codeConfig") or {}).get("s3Object") or {}
    if s3_object.get("key") != "app-v1.zip":
        raise AssertionError(f"GetWebFunctionRevision: buildConfig {resp.get('buildConfig')}")


def CreateWebFunctionRevision(ctx: TestContext) -> None:
    web = _web(ctx)
    resp = web.create_web_function_revision(
        functionName=_fn_name(ctx),
        description="second revision",
        buildConfig=_build_config(ctx, "app-v2.zip"),
        serviceConfig={"executionRoleArn": _ROLE_ARN, "environmentVariables": {"MODE": "prod"}},
    )
    revision_id = resp.get("revisionId", "")
    if not revision_id or revision_id == ctx.get("lw_revision_1"):
        raise AssertionError(f"CreateWebFunctionRevision: revisionId {revision_id!r} is empty or the first revision's")
    ctx["lw_revision_2"] = revision_id
    _wait_revision_active(ctx, _fn_name(ctx), revision_id)

    got = web.get_web_function_revision(functionName=_fn_name(ctx), revisionId=revision_id)
    s3_object = ((got.get("buildConfig") or {}).get("codeConfig") or {}).get("s3Object") or {}
    env = (got.get("serviceConfig") or {}).get("environmentVariables") or {}
    if got.get("description") != "second revision" or env.get("MODE") != "prod" or s3_object.get("key") != "app-v2.zip":
        raise AssertionError(f"GetWebFunctionRevision after create: {got}")

    # The platform owns AWS_REGION and friends: a revision cannot set them.
    _expect_error(
        "CreateWebFunctionRevision with a reserved environment variable",
        lambda: web.create_web_function_revision(
            functionName=_fn_name(ctx),
            buildConfig=_build_config(ctx, "app-v2.zip"),
            serviceConfig={"executionRoleArn": _ROLE_ARN, "environmentVariables": {"AWS_REGION": "eu-west-1"}},
        ),
        "ValidationException",
    )


def GetWebFunctionEndpointLatestRevision(ctx: TestContext) -> None:
    _, second = _revision_ids(ctx)
    # The LatestRevision endpoint follows the new revision once it is Active.
    holder: dict = {}

    def serving() -> bool:
        holder["resp"] = _get_endpoint(ctx, _LIVE)
        weights = holder["resp"].get("revisionWeights", [])
        return (
            holder["resp"].get("state") == "Active"
            and len(weights) == 1
            and weights[0].get("revisionId") == second
        )

    _eventually(f"the live endpoint to serve {second}", 30, serving)
    resp = holder["resp"]
    if resp.get("autoDeploymentMode") != "LatestRevision":
        raise AssertionError(f"GetWebFunctionEndpoint: mode {resp.get('autoDeploymentMode')!r}")
    weights = resp.get("revisionWeights", [])
    if len(weights) != 1 or weights[0].get("revisionId") != second or weights[0].get("weight") != 100:
        raise AssertionError(f"GetWebFunctionEndpoint: revisionWeights {weights}, want {second} at 100")


def ListWebFunctionRevisions(ctx: TestContext) -> None:
    first, second = _revision_ids(ctx)
    order: list[str] = []
    kwargs: dict = {"functionName": _fn_name(ctx)}
    while True:
        resp = _web(ctx).list_web_function_revisions(**kwargs)
        order.extend(r.get("revisionId", "") for r in resp.get("revisions", []))
        token = resp.get("nextToken")
        if not token:
            break
        kwargs["nextToken"] = token
    # Newest first, and the refused revision with a reserved variable left no trace.
    if order != [second, first]:
        raise AssertionError(f"ListWebFunctionRevisions: got {order}, want [{second}, {first}] (newest first)")


def CreateWebFunctionEndpoint(ctx: TestContext) -> None:
    first, second = _revision_ids(ctx)
    # Weights may name only Active revisions.
    for revision_id in (first, second):
        _wait_revision_active(ctx, _fn_name(ctx), revision_id)
    resp = _web(ctx).create_web_function_endpoint(
        functionName=_fn_name(ctx),
        endpointName=_CANARY,
        endpointType="HomeRegion",
        authType="ApplicationManaged",
        autoDeploymentMode="Disabled",
        revisionWeights=[
            {"revisionId": first, "weight": 90},
            {"revisionId": second, "weight": 10},
        ],
    )
    arn = resp.get("endpointArn", "")
    if not arn.endswith("/endpoint/" + _CANARY) or not resp.get("domainName"):
        raise AssertionError(f"CreateWebFunctionEndpoint: endpointArn {arn!r} domainName {resp.get('domainName')!r}")
    ctx["lw_canary_arn"] = arn


def GetWebFunctionEndpoint(ctx: TestContext) -> None:
    first, second = _revision_ids(ctx)
    resp = _get_endpoint(ctx, _CANARY)
    if (
        resp.get("endpointArn") != ctx.get("lw_canary_arn")
        or resp.get("autoDeploymentMode") != "Disabled"
        or resp.get("endpointType") != "HomeRegion"
    ):
        raise AssertionError(
            f"GetWebFunctionEndpoint: got {resp.get('endpointArn')!r} "
            f"mode {resp.get('autoDeploymentMode')!r} type {resp.get('endpointType')!r}"
        )
    weights = {w.get("revisionId"): w.get("weight") for w in resp.get("revisionWeights", [])}
    if weights != {first: 90, second: 10}:
        raise AssertionError(f"GetWebFunctionEndpoint: revisionWeights {weights}, want {first}:90 {second}:10")


def ListWebFunctionEndpoints(ctx: TestContext) -> None:
    seen: set[str] = set()
    kwargs: dict = {"functionName": _fn_name(ctx)}
    while True:
        resp = _web(ctx).list_web_function_endpoints(**kwargs)
        seen.update(e.get("endpointName", "") for e in resp.get("endpoints", []))
        token = resp.get("nextToken")
        if not token:
            break
        kwargs["nextToken"] = token
    if seen != {_LIVE, _CANARY}:
        raise AssertionError(f"ListWebFunctionEndpoints: got {sorted(seen)}, want live and canary")


def UpdateWebFunctionEndpoint(ctx: TestContext) -> None:
    resp = _web(ctx).update_web_function_endpoint(
        functionName=_fn_name(ctx),
        endpointName=_CANARY,
        authType="IamAuth",
        throttleConfig={"rateLimit": 8000},
    )
    if resp.get("authType") != "IamAuth" or (resp.get("throttleConfig") or {}).get("rateLimit") != 8000:
        raise AssertionError(
            f"UpdateWebFunctionEndpoint: authType {resp.get('authType')!r} throttleConfig {resp.get('throttleConfig')}"
        )
    got = _get_endpoint(ctx, _CANARY)
    if got.get("authType") != "IamAuth" or (got.get("throttleConfig") or {}).get("rateLimit") != 8000:
        raise AssertionError(
            f"GetWebFunctionEndpoint after update: authType {got.get('authType')!r} "
            f"throttleConfig {got.get('throttleConfig')}"
        )
    # The members the update left out are kept: the traffic split is unchanged.
    if len(got.get("revisionWeights", [])) != 2:
        raise AssertionError(f"GetWebFunctionEndpoint after update: revisionWeights {got.get('revisionWeights')}")


def UpdateWebFunctionEndpointClearThrottle(ctx: TestContext) -> None:
    # An empty throttleConfig clears the rate limit; it is not "leave as is".
    _web(ctx).update_web_function_endpoint(
        functionName=_fn_name(ctx), endpointName=_CANARY, throttleConfig={}
    )
    got = _get_endpoint(ctx, _CANARY)
    if (got.get("throttleConfig") or {}).get("rateLimit") is not None:
        raise AssertionError(f"GetWebFunctionEndpoint after clearing: throttleConfig {got.get('throttleConfig')}, want none")
    if got.get("authType") != "IamAuth":
        raise AssertionError(f"GetWebFunctionEndpoint after clearing: authType {got.get('authType')!r}, the earlier change was lost")


def DeleteWebFunctionRevisionServingTraffic(ctx: TestContext) -> None:
    first, _ = _revision_ids(ctx)
    web = _web(ctx)
    # The canary endpoint sends 90% of its traffic to the first revision.
    err = _expect_error(
        "DeleteWebFunctionRevision while serving traffic",
        lambda: web.delete_web_function_revision(functionName=_fn_name(ctx), revisionId=first),
        "ConflictException",
    )
    if err.response["ResponseMetadata"]["HTTPStatusCode"] != 409:
        raise AssertionError("DeleteWebFunctionRevision while serving traffic: expected HTTP 409")
    # The refused delete left the revision in place.
    web.get_web_function_revision(functionName=_fn_name(ctx), revisionId=first)


def TagResource(ctx: TestContext) -> None:
    arn = _function_arn(ctx)
    web = _web(ctx)
    web.tag_resource(resource=arn, tags={"team": "web", "stage": "compat"})
    tags = web.list_tags(resource=arn).get("tags", {})
    if tags.get("team") != "web" or tags.get("stage") != "compat":
        raise AssertionError(f"ListTags after TagResource: {tags}")


def ListTags(ctx: TestContext) -> None:
    tags = _web(ctx).list_tags(resource=_function_arn(ctx)).get("tags", {})
    if tags != {"team": "web", "stage": "compat"}:
        raise AssertionError(f"ListTags: got {tags}, want team=web and stage=compat")


def UntagResource(ctx: TestContext) -> None:
    arn = _function_arn(ctx)
    web = _web(ctx)
    web.untag_resource(resource=arn, tagKeys=["stage"])
    tags = web.list_tags(resource=arn).get("tags", {})
    if tags != {"team": "web"}:
        raise AssertionError(f"ListTags after UntagResource: {tags}, want only team=web")


def PutResourcePolicy(ctx: TestContext) -> None:
    arn = _function_arn(ctx)
    resp = _web(ctx).put_resource_policy(resourceArn=arn, policy=_policy(arn, '{"AWS":"444455556666"}'))
    if not resp.get("revisionId"):
        raise AssertionError("PutResourcePolicy: no revisionId")
    ctx["lw_policy_revision"] = resp["revisionId"]


def GetResourcePolicy(ctx: TestContext) -> None:
    resp = _web(ctx).get_resource_policy(resourceArn=_function_arn(ctx))
    if "444455556666" not in resp.get("policy", ""):
        raise AssertionError(f"GetResourcePolicy: policy does not name the account: {resp.get('policy')!r}")
    if resp.get("revisionId") != ctx.get("lw_policy_revision"):
        raise AssertionError(
            f"GetResourcePolicy: revisionId {resp.get('revisionId')!r}, "
            f"PutResourcePolicy returned {ctx.get('lw_policy_revision')!r}"
        )


def PutResourcePolicyPublicPrincipal(ctx: TestContext) -> None:
    arn = _function_arn(ctx)
    web = _web(ctx)
    # A wildcard principal with no condition would make the endpoint public.
    _expect_error(
        "PutResourcePolicy with a wildcard principal",
        lambda: web.put_resource_policy(resourceArn=arn, policy=_policy(arn, '"*"')),
        "ValidationException",
    )
    # The refused policy did not replace the stored one.
    stored = web.get_resource_policy(resourceArn=arn).get("policy", "")
    if "444455556666" not in stored:
        raise AssertionError(f"GetResourcePolicy after a refused put: policy changed to {stored!r}")


def DeleteResourcePolicy(ctx: TestContext) -> None:
    arn = _function_arn(ctx)
    web = _web(ctx)
    web.delete_resource_policy(resourceArn=arn)
    _expect_error(
        "GetResourcePolicy after DeleteResourcePolicy",
        lambda: web.get_resource_policy(resourceArn=arn),
        "ResourceNotFoundException",
    )


def GetWebAccountSettings(ctx: TestContext) -> None:
    resp = _web(ctx).get_web_account_settings()
    quotas = resp.get("accountQuotas") or {}
    if (
        quotas.get("maxTotalArmVCpus") != 2000
        or quotas.get("maxTotalRateLimit") != 100000
        or quotas.get("maxRevisionsPerFunction") != 50
        or quotas.get("maxEndpointsPerFunction") != 10
    ):
        raise AssertionError(f"GetWebAccountSettings: accountQuotas {quotas}")
    # Other suites' groups may be running their own functions, so at least ours.
    if (resp.get("accountUsage") or {}).get("functionCount", 0) < 1:
        raise AssertionError(f"GetWebAccountSettings: accountUsage {resp.get('accountUsage')}, want functionCount >= 1")


def DeleteWebFunctionEndpoint(ctx: TestContext) -> None:
    web = _web(ctx)
    web.delete_web_function_endpoint(functionName=_fn_name(ctx), endpointName=_CANARY)
    _expect_error(
        "GetWebFunctionEndpoint after delete",
        lambda: _get_endpoint(ctx, _CANARY),
        "ResourceNotFoundException",
    )


def DeleteWebFunctionRevision(ctx: TestContext) -> None:
    first, _ = _revision_ids(ctx)
    web = _web(ctx)
    # With the canary endpoint gone, only the live endpoint routes anywhere and
    # it serves the second revision, so the first can go.
    web.delete_web_function_revision(functionName=_fn_name(ctx), revisionId=first)
    _expect_error(
        "GetWebFunctionRevision after delete",
        lambda: web.get_web_function_revision(functionName=_fn_name(ctx), revisionId=first),
        "ResourceNotFoundException",
    )


def DeleteWebFunction(ctx: TestContext) -> None:
    web = _web(ctx)
    name = _fn_name(ctx)
    web.delete_web_function(functionName=name)
    _expect_error(
        "GetWebFunction after delete",
        lambda: web.get_web_function(functionName=name),
        "ResourceNotFoundException",
    )
    # The function's endpoints went with it.
    _expect_error(
        "GetWebFunctionEndpoint after the function is deleted",
        lambda: _get_endpoint(ctx, _LIVE),
        "ResourceNotFoundException",
    )


# ── lambda-web-endpoints ──────────────────────────────────────────────────────
#
# The data plane, black box: a Node.js server is uploaded to S3, deployed with
# the Lambda Web Functions SDK calls, and then reached with plain HTTP — to the
# Overcast base URL with the endpoint's domainName as the Host header, which is
# how a client reaches an endpoint whose DNS name resolves to Overcast. Every
# test needs Docker (the registry says so) because the package runs in a
# container.

# A dependency-free Node.js server. "/" answers with what the request looked
# like to the application; "/sleep" outlives any timeout the tests set;
# "/stream" writes two chunks 500 ms apart.
_APP_SOURCE = """
const http = require('http');
const [host, port] = process.env.AWS_LAMBDA_HTTP_ENDPOINT.split(':');
http.createServer((req, res) => {
  if (req.url.indexOf('/sleep') === 0) {
    setTimeout(() => res.end('late'), 20000);
    return;
  }
  if (req.url.indexOf('/stream') === 0) {
    res.setHeader('content-type', 'text/plain');
    res.write('one\\n');
    setTimeout(() => res.end('two\\n'), 500);
    return;
  }
  res.setHeader('x-app', 'echo');
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify({
    version: process.env.APP_VERSION || 'v1',
    method: req.method, url: req.url, host: req.headers.host,
    headers: req.headers,
  }));
}).listen(port, host);
"""
# Fails at import time, before it can listen.
_BROKEN_SOURCE = "require('./missing');\n"
# Runs forever and never listens on AWS_LAMBDA_HTTP_ENDPOINT.
_SILENT_SOURCE = "setInterval(function () {}, 1000);\n"

_APP_KEY = "app.zip"
_BROKEN_KEY = "broken.zip"
_SILENT_KEY = "silent.zip"


def _endpoints_bucket(ctx: TestContext) -> str:
    return f"{ctx.run_id}-lambda-web-endpoints"


def _setup_endpoints(ctx: TestContext) -> None:
    bucket = _endpoints_bucket(ctx)
    _seed_bucket(ctx, bucket, [_APP_KEY], {"index.js": _APP_SOURCE})
    s3 = _s3(ctx)
    s3.put_object(Bucket=bucket, Key=_BROKEN_KEY, Body=_zip({"index.js": _BROKEN_SOURCE}))
    s3.put_object(Bucket=bucket, Key=_SILENT_KEY, Body=_zip({"index.js": _SILENT_SOURCE}))


def _track(ctx: TestContext, function_name: str) -> None:
    """Remember a function so teardown can remove it even if its test failed."""
    names = ctx.get("lwe_functions") or []
    names.append(function_name)
    ctx["lwe_functions"] = names


def _delete_function(ctx: TestContext, function_name: str) -> None:
    """Remove a web function the explicit way: endpoints, revisions, function."""
    web = _web(ctx)
    try:
        for ep in web.list_web_function_endpoints(functionName=function_name).get("endpoints", []):
            try:
                web.delete_web_function_endpoint(functionName=function_name, endpointName=ep["endpointName"])
            except Exception:
                pass
    except Exception:
        pass
    try:
        for rev in web.list_web_function_revisions(functionName=function_name).get("revisions", []):
            try:
                web.delete_web_function_revision(functionName=function_name, revisionId=rev["revisionId"])
            except Exception:
                pass
    except Exception:
        pass
    try:
        web.delete_web_function(functionName=function_name)
    except Exception:
        pass


def _teardown_endpoints(ctx: TestContext) -> None:
    for function_name in ctx.get("lwe_functions") or []:
        _delete_function(ctx, function_name)
    _delete_bucket(ctx, _endpoints_bucket(ctx))


def _revision_config(ctx: TestContext, key: str, env=None, timeout=None) -> dict:
    service: dict = {"executionRoleArn": _ROLE_ARN}
    if env:
        service["environmentVariables"] = env
    if timeout is not None:
        service["timeoutSeconds"] = timeout
    return {
        "buildConfig": {
            "codeConfig": {"s3Object": {"bucket": _endpoints_bucket(ctx), "key": key}},
            "runtimeConfig": {"runtime": "nodejs24.x"},
        },
        "serviceConfig": service,
    }


def _wait_endpoint_serving(ctx: TestContext, function_name: str, endpoint: str, revision_id: str) -> dict:
    """Wait until the endpoint is Active and sends all its traffic to one revision."""
    holder: dict = {}

    def check() -> bool:
        resp = holder["resp"] = _web(ctx).get_web_function_endpoint(functionName=function_name, endpointName=endpoint)
        weights = resp.get("revisionWeights", [])
        return (
            resp.get("state") == "Active"
            and resp.get("updateStatus") != "InProgress"
            and len(weights) == 1
            and weights[0].get("revisionId") == revision_id
        )

    _eventually(f"endpoint {endpoint} to serve {revision_id}", 30, check)
    return holder["resp"]


def _wait_updated(ctx: TestContext, function_name: str, endpoint: str, accept) -> None:
    """Wait until the endpoint's last update is applied and accept(resp) holds."""

    def check() -> bool:
        resp = _web(ctx).get_web_function_endpoint(functionName=function_name, endpointName=endpoint)
        if resp.get("updateStatus") == "Failed":
            raise AssertionError(f"update Failed: {resp.get('updateStatusReason')}")
        return resp.get("updateStatus") != "InProgress" and accept(resp)

    _eventually(f"endpoint {endpoint} update to apply", 30, check)


class _App:
    """One deployed web function with its "live" endpoint."""

    def __init__(self, ctx: TestContext, function_name: str) -> None:
        self.ctx = ctx
        self.fn = function_name
        self.domain = ""  # the endpoint's domainName: the Host a client sends
        self.rev = ""  # the first revision

    def send(self, method: str, path: str, headers=None) -> "_Response":
        return _send(self.ctx, self.domain, method, path, headers)

    def close(self) -> None:
        _delete_function(self.ctx, self.fn)


def _deploy(ctx: TestContext, suffix: str, key: str = _APP_KEY, auth: str = "ApplicationManaged", env=None, timeout=None) -> _App:
    function_name = f"{ctx.run_id}-lambda-web-endpoints-{suffix}"
    _track(ctx, function_name)
    app = _App(ctx, function_name)
    resp = _web(ctx).create_web_function(
        functionName=function_name,
        revisionConfig=_revision_config(ctx, key, env, timeout),
        endpointConfig={
            "endpointName": _LIVE,
            "endpointType": "HomeRegion",
            "authType": auth,
            "autoDeploymentMode": "LatestRevision",
        },
    )
    app.rev = (resp.get("revision") or {}).get("revisionId", "")
    if not app.rev or not resp.get("endpoint"):
        app.close()
        raise AssertionError(f"CreateWebFunction {function_name}: no revision or endpoint in {resp}")
    try:
        _wait_revision_active(ctx, function_name, app.rev)
        endpoint = _wait_endpoint_serving(ctx, function_name, _LIVE, app.rev)
        app.domain = endpoint.get("domainName", "")
        if not app.domain:
            raise AssertionError(f"GetWebFunctionEndpoint {function_name}: empty domainName")
    except Exception:
        app.close()
        raise
    return app


class _Response:
    def __init__(self, status: int, headers, body: bytes) -> None:
        self.status = status
        self.headers = headers
        self.body = body

    def header(self, name: str) -> str:
        return self.headers.get(name) or ""

    def error_type(self) -> str:
        return self.header("lambda-web-error-type")

    def text(self) -> str:
        return self.body.decode("utf-8", "replace").strip()


def _raw(ctx: TestContext, host: str, method: str, path: str, headers=None):
    """Send one request to the Overcast base URL with host as the Host header.

    Returns (connection, response); the caller reads the body and closes."""
    base = urlsplit(ctx.endpoint)
    conn = http_client.HTTPConnection(base.hostname, base.port or 80, timeout=90)
    conn.putrequest(method, path, skip_host=True, skip_accept_encoding=True)
    conn.putheader("Host", host)
    for key, value in (headers or {}).items():
        conn.putheader(key, value)
    if method in ("POST", "PUT", "PATCH"):
        conn.putheader("Content-Length", "0")
    conn.putheader("Connection", "close")
    conn.endheaders()
    return conn, conn.getresponse()


def _send(ctx: TestContext, host: str, method: str, path: str, headers=None) -> _Response:
    """Send a request and read the response to the end.

    One allowance: a first request can arrive while the emulator is still
    finishing its Docker start-up work, and is answered 502 with no
    lambda-web-error-type. Those are retried for a while; every platform error
    AWS documents carries a type."""
    deadline = time.monotonic() + 60
    attempt = 0
    while True:
        attempt += 1
        conn, resp = _raw(ctx, host, method, path, headers)
        try:
            out = _Response(resp.status, resp.headers, resp.read())
        finally:
            conn.close()
        if out.status == 502 and not out.error_type() and time.monotonic() < deadline:
            ctx.log(f"{method} {path}: untyped 502 (attempt {attempt}), retrying: {out.text()}")
            time.sleep(0.3)
            continue
        return out


def _expect_200(what: str, resp: _Response) -> dict:
    """Parse the echo app's answer, failing with the response when it is not a 200."""
    if resp.status != 200:
        raise AssertionError(f"{what}: HTTP {resp.status} (lambda-web-error-type {resp.error_type()!r}): {resp.text()}")
    try:
        echo = json.loads(resp.body)
    except ValueError as err:
        raise AssertionError(f"{what}: body is not the echo app's JSON: {err}: {resp.text()}")
    echo["headers"] = {k.lower(): v for k, v in (echo.get("headers") or {}).items()}
    return echo


def _expect_platform_error(what: str, resp: _Response, status: int, error_type: str) -> None:
    if resp.status != status or resp.error_type() != error_type:
        raise AssertionError(
            f"{what}: got HTTP {resp.status} with lambda-web-error-type {resp.error_type()!r}, "
            f"want {status} {error_type!r}: {resp.text()}"
        )


def _host_only(host: str) -> str:
    if ":" in host and not host.endswith("]"):
        host = host.rsplit(":", 1)[0]
    return host.lower()


def _with_app(ctx: TestContext, suffix: str, body, **deploy_args) -> None:
    """Deploy, run body(app), and always remove the function."""
    app = _deploy(ctx, suffix, **deploy_args)
    try:
        body(app)
    finally:
        app.close()


def ServeEndpointRequest(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        resp = app.send("GET", "/hello/world?x=1&y=%2F", {"X-Forwarded-For": "1.2.3.4"})
        echo = _expect_200("GET through the endpoint domain", resp)
        if echo.get("method") != "GET" or echo.get("url") != "/hello/world?x=1&y=%2F":
            raise AssertionError(f"the application saw {echo.get('method')} {echo.get('url')}, want GET /hello/world?x=1&y=%2F")
        if _host_only(echo.get("host", "")) != _host_only(app.domain):
            raise AssertionError(f"the application saw Host {echo.get('host')!r}, want the endpoint domain {app.domain!r}")
        if echo["headers"].get("lambda-web-endpoint-name") != _LIVE:
            raise AssertionError(f"lambda-web-endpoint-name = {echo['headers'].get('lambda-web-endpoint-name')!r}, want {_LIVE!r}")
        request_id = echo["headers"].get("lambda-web-request-id", "")
        if not request_id or request_id != resp.header("lambda-web-request-id"):
            raise AssertionError(
                f"request id: the application saw {request_id!r}, the response carries {resp.header('lambda-web-request-id')!r}"
            )
        if not resp.header("lambda-web-logstream"):
            raise AssertionError(f"the response has no lambda-web-logstream header: {dict(resp.headers)}")
        if resp.header("x-app") != "echo":
            raise AssertionError(f"the application's own response header x-app was lost: {dict(resp.headers)}")
        if echo.get("version") != "v1":
            raise AssertionError(f"version = {echo.get('version')!r}, want v1")

    _with_app(ctx, "serve", body)


def ForwardRequestHeaders(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        resp = app.send(
            "GET",
            "/",
            {
                "X-Real-IP": "9.9.9.9",
                "X-HTTP-Method-Override": "DELETE",
                "X-HTTP-Method": "DELETE",
                "X-Method-Override": "DELETE",
                "Proxy-Authorization": "Basic Zm9vOmJhcg==",
                "Lambda-Web-Endpoint-Name": "spoofed",
                "Lambda-Web-Request-Id": "spoofed",
                "Lambda-Web-Custom": "1",
                "X-Forwarded-For": "1.2.3.4",
                "X-Forwarded-Host": "original.example.test",
            },
        )
        echo = _expect_200("GET with headers the platform controls", resp)
        headers = echo["headers"]
        for name in ("x-real-ip", "x-http-method-override", "x-http-method", "x-method-override", "proxy-authorization", "lambda-web-custom"):
            if name in headers:
                raise AssertionError(f"the application saw {name} = {headers[name]!r}, which the platform removes")
        if echo.get("method") != "GET":
            raise AssertionError(f"the application saw method {echo.get('method')}: a method override header took effect")
        if headers.get("lambda-web-endpoint-name") != _LIVE:
            raise AssertionError(
                f"lambda-web-endpoint-name = {headers.get('lambda-web-endpoint-name')!r}: a client-supplied value was not replaced (want {_LIVE!r})"
            )
        request_id = headers.get("lambda-web-request-id", "")
        if not request_id or request_id == "spoofed" or request_id != resp.header("lambda-web-request-id"):
            raise AssertionError(
                f"lambda-web-request-id: the application saw {request_id!r}, the response carries {resp.header('lambda-web-request-id')!r}"
            )
        xff = headers.get("x-forwarded-for", "")
        if not xff.startswith("1.2.3.4, ") or len(xff) <= len("1.2.3.4, "):
            raise AssertionError(f"x-forwarded-for = {xff!r}, want the client address appended to '1.2.3.4'")
        if headers.get("x-forwarded-host") != "original.example.test":
            raise AssertionError(f"x-forwarded-host = {headers.get('x-forwarded-host')!r}, want it forwarded untouched")

    _with_app(ctx, "headers", body)


def AuthorizationHeader(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        # An application-managed endpoint leaves authentication to the
        # application: a bearer token reaches it.
        echo = _expect_200("GET with a bearer token", app.send("GET", "/", {"Authorization": "Bearer compat-token-123"}))
        if echo["headers"].get("authorization") != "Bearer compat-token-123":
            raise AssertionError(f"the application saw Authorization {echo['headers'].get('authorization')!r}, want the bearer token")
        # An AWS signature is Lambda's, not the application's, and is removed.
        echo = _expect_200(
            "GET with a SigV4 Authorization",
            app.send(
                "GET",
                "/",
                {
                    "Authorization": "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260101/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=0123456789abcdef",
                    "X-Amz-Date": "20260101T000000Z",
                    "X-Amz-Security-Token": "compat-session-token",
                },
            ),
        )
        for name in ("authorization", "x-amz-security-token"):
            if name in echo["headers"]:
                raise AssertionError(f"the application saw {name} = {echo['headers'][name]!r}: a SigV4 credential must not reach it")

    _with_app(ctx, "authz", body)


def _create_revision(ctx: TestContext, function_name: str, version: str) -> str:
    cfg = _revision_config(ctx, _APP_KEY, {"APP_VERSION": version})
    resp = _web(ctx).create_web_function_revision(
        functionName=function_name, buildConfig=cfg["buildConfig"], serviceConfig=cfg["serviceConfig"]
    )
    revision_id = resp.get("revisionId", "")
    if not revision_id:
        raise AssertionError("CreateWebFunctionRevision: no revisionId")
    _wait_revision_active(ctx, function_name, revision_id)
    return revision_id


def LatestRevisionServesNewRevision(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        echo = _expect_200("the first revision", app.send("GET", "/"))
        if echo.get("version") != "v1":
            raise AssertionError(f"the first revision answered version {echo.get('version')!r}, want v1")
        second = _create_revision(ctx, app.fn, "v2")
        if second == app.rev:
            raise AssertionError(f"CreateWebFunctionRevision returned the first revision's id {second}")
        _wait_endpoint_serving(ctx, app.fn, _LIVE, second)
        # Requests reach the new revision (a warm environment of the old one may
        # still answer a request that was already routed).
        _eventually(
            "requests to be served by version v2",
            30,
            lambda: _expect_200("the second revision", app.send("GET", "/")).get("version") == "v2",
        )

    _with_app(ctx, "latest", body, env={"APP_VERSION": "v1"})


def WeightedRouting(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        web = _web(ctx)
        second = _create_revision(ctx, app.fn, "v2")
        # Weights run from 1 to 100 per revision, so a canary is a 90/10 split.
        created = web.create_web_function_endpoint(
            functionName=app.fn,
            endpointName="canary",
            endpointType="HomeRegion",
            authType="ApplicationManaged",
            autoDeploymentMode="Disabled",
            revisionWeights=[{"revisionId": app.rev, "weight": 90}, {"revisionId": second, "weight": 10}],
        )
        _eventually(
            "the canary endpoint to become Active",
            30,
            lambda: web.get_web_function_endpoint(functionName=app.fn, endpointName="canary").get("state") == "Active",
        )
        canary = created.get("domainName", "")
        if not canary or canary == app.domain:
            raise AssertionError(f"the canary endpoint's domain is {canary!r} (the live endpoint's is {app.domain!r})")
        counts: dict = {}
        requests = 200
        for i in range(requests):
            echo = _expect_200(f"request {i + 1} to the canary endpoint", _send(ctx, canary, "GET", "/"))
            counts[echo.get("version")] = counts.get(echo.get("version"), 0) + 1
        # 90/10 of 200 requests expects 180/20; the bounds are loose enough that
        # a correct split fails them with probability well under one in a million.
        if counts.get("v1", 0) < 120 or counts.get("v2", 0) < 2 or sum(counts.values()) != requests:
            raise AssertionError(
                f"a 90/10 split over {requests} requests served {counts}, want both revisions with v1 the clear majority"
            )

    _with_app(ctx, "weights", body, env={"APP_VERSION": "v1"})


def IamAuthRefusesUnsignedRequest(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        # Authorisation failures are a bare 403: AWS sends no lambda-web-error-type.
        resp = app.send("GET", "/")
        if resp.status != 403 or resp.error_type():
            raise AssertionError(
                f"an unsigned request to an IamAuth endpoint: HTTP {resp.status} with lambda-web-error-type "
                f"{resp.error_type()!r}, want a bare 403: {resp.text()}"
            )
        # The same call to the endpoint once it is ApplicationManaged.
        updated = _web(ctx).update_web_function_endpoint(
            functionName=app.fn, endpointName=_LIVE, authType="ApplicationManaged"
        )
        if updated.get("authType") != "ApplicationManaged":
            raise AssertionError(f"UpdateWebFunctionEndpoint: authType {updated.get('authType')!r}")
        _wait_updated(ctx, app.fn, _LIVE, lambda e: e.get("authType") == "ApplicationManaged")

        def served() -> bool:
            resp = app.send("GET", "/")
            if resp.status == 403:
                return False
            _expect_200("an unsigned request to an ApplicationManaged endpoint", resp)
            return True

        _eventually("the unsigned request to be served", 20, served)

    _with_app(ctx, "iam", body, auth="IamAuth")


def ImportCodeError(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        resp = app.send("GET", "/")
        _expect_platform_error("a package whose entry file requires a missing module", resp, 502, "Runtime.ImportCodeError")

    _with_app(ctx, "import", body, key=_BROKEN_KEY)


def ListenTimeout(ctx: TestContext) -> None:
    # The slow one: the platform waits 10 seconds for the server to start
    # listening before it gives up, so this test takes about that long.
    def body(app: _App) -> None:
        start = time.monotonic()
        resp = app.send("GET", "/")
        _expect_platform_error("an application that never listens", resp, 502, "Application.TimeoutListenError")
        waited = time.monotonic() - start
        if waited < 5:
            raise AssertionError(
                f"the platform gave up after {waited:.1f}s; it should wait about 10 seconds for the application to listen"
            )

    _with_app(ctx, "listen", body, key=_SILENT_KEY)


def UnknownDomain(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        # Keep everything but the endpoint's own id, so the host is shaped like
        # an endpoint domain and names nothing.
        labels = app.domain.split(".", 1)
        if len(labels) != 2:
            raise AssertionError(f"endpoint domain {app.domain!r} has no id label")
        resp = _send(ctx, "nosuchendpoint0." + labels[1], "GET", "/")
        _expect_platform_error("a request to an endpoint domain nobody owns", resp, 404, "Platform.InvalidEndpoint")

    _with_app(ctx, "unknown", body)


def UnsupportedMethod(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        resp = app.send("TRACE", "/")
        if resp.status != 400:
            raise AssertionError(f"TRACE: HTTP {resp.status} (lambda-web-error-type {resp.error_type()!r}), want 400: {resp.text()}")
        # The endpoint still serves supported methods.
        echo = _expect_200("POST after a refused TRACE", app.send("POST", "/"))
        if echo.get("method") != "POST":
            raise AssertionError(f"the application saw method {echo.get('method')}, want POST")

    _with_app(ctx, "method", body)


def _set_rate_limit(ctx: TestContext, app: _App, limit: int) -> None:
    updated = _web(ctx).update_web_function_endpoint(
        functionName=app.fn, endpointName=_LIVE, throttleConfig={"rateLimit": limit}
    )
    got = (updated.get("throttleConfig") or {}).get("rateLimit")
    if got != limit:
        raise AssertionError(f"UpdateWebFunctionEndpoint: throttleConfig.rateLimit = {got!r}, want {limit}")
    _wait_updated(ctx, app.fn, _LIVE, lambda e: (e.get("throttleConfig") or {}).get("rateLimit") == limit)


def EndpointDisabled(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        _expect_200("before disabling", app.send("GET", "/"))
        # A rate limit of 0 disables the endpoint.
        _set_rate_limit(ctx, app, 0)
        last: dict = {}

        def disabled() -> bool:
            last["resp"] = app.send("GET", "/")
            return last["resp"].status == 503

        try:
            _eventually("the endpoint to answer 503", 20, disabled)
        except AssertionError as err:
            resp = last["resp"]
            raise AssertionError(f"{err} (last: HTTP {resp.status} {resp.error_type()!r})")
        _expect_platform_error("an endpoint with rateLimit 0", last["resp"], 503, "Platform.EndpointDisabled")

    _with_app(ctx, "disabled", body)


def EndpointRateLimit(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        # Warm an environment first, so the burst measures the rate limit and
        # not a cold start.
        _expect_200("the warm-up request", app.send("GET", "/"))
        _set_rate_limit(ctx, app, 100)

        burst = 50
        results: list = [None] * burst

        def fire(i: int) -> None:
            try:
                results[i] = app.send("GET", "/")
            except Exception as err:  # reported below
                results[i] = err

        threads = [threading.Thread(target=fire, args=(i,)) for i in range(burst)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        throttled = served = 0
        for r in results:
            if isinstance(r, Exception):
                raise AssertionError(f"a request in the burst failed: {r}")
            if r.status == 200:
                served += 1
            elif r.status == 429:
                _expect_platform_error("a throttled request", r, 429, "Platform.EndpointRequestThrottle")
                if not r.header("retry-after"):
                    raise AssertionError(f"a throttled request carries no Retry-After header: {dict(r.headers)}")
                throttled += 1
            else:
                raise AssertionError(f"a request in the burst: HTTP {r.status} (lambda-web-error-type {r.error_type()!r}): {r.text()}")
        if throttled == 0:
            raise AssertionError(
                f"{burst} concurrent requests to an endpoint limited to 100 per second were all served; some should be throttled"
            )
        if served == 0:
            raise AssertionError(f"all {burst} requests were throttled; a limit of 100 per second allows a burst through")

    _with_app(ctx, "throttle", body)


def RequestTimeout(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        rev = _web(ctx).get_web_function_revision(functionName=app.fn, revisionId=app.rev)
        if (rev.get("serviceConfig") or {}).get("timeoutSeconds") != 3:
            raise AssertionError(f"GetWebFunctionRevision: serviceConfig {rev.get('serviceConfig')}, want timeoutSeconds 3")
        # Warm the environment so the wait below is the application's, not a cold start.
        _expect_200("the warm-up request", app.send("GET", "/"))
        start = time.monotonic()
        resp = app.send("GET", "/sleep")
        _expect_platform_error("an application that outlives timeoutSeconds", resp, 504, "Runtime.RequestTimeout")
        waited = time.monotonic() - start
        if waited < 2 or waited > 15:
            raise AssertionError(f"the request timed out after {waited:.1f}s, want about 3 seconds")

    _with_app(ctx, "timeout", body, timeout=3)


def DeletedEndpointDomain(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        web = _web(ctx)
        _expect_200("before deleting the endpoint", app.send("GET", "/"))
        web.delete_web_function_endpoint(functionName=app.fn, endpointName=_LIVE)

        def gone() -> bool:
            try:
                web.get_web_function_endpoint(functionName=app.fn, endpointName=_LIVE)
            except ClientError as err:
                if err.response.get("Error", {}).get("Code") == "ResourceNotFoundException":
                    return True
                raise
            return False

        _eventually("the deleted endpoint to be gone", 30, gone)
        last: dict = {}

        def not_found() -> bool:
            last["resp"] = app.send("GET", "/")
            return last["resp"].status == 404

        try:
            _eventually("the deleted endpoint's domain to answer 404", 30, not_found)
        except AssertionError as err:
            resp = last["resp"]
            raise AssertionError(f"{err} (last: HTTP {resp.status} {resp.error_type()!r})")
        _expect_platform_error("a request to a deleted endpoint's domain", last["resp"], 404, "Platform.InvalidEndpoint")

    _with_app(ctx, "deleted", body)


def StreamedResponse(ctx: TestContext) -> None:
    def body(app: _App) -> None:
        # Warm up, so the gap measured below is the application's 500 ms.
        _expect_200("the warm-up request", app.send("GET", "/"))
        conn, resp = _raw(ctx, app.domain, "GET", "/stream")
        try:
            if resp.status != 200:
                raise AssertionError(f"GET /stream: HTTP {resp.status} (lambda-web-error-type {resp.getheader('lambda-web-error-type')!r})")
            # The application writes "one" and, 500 ms later, "two". A proxy
            # that buffered the response would deliver both together.
            first = resp.readline()
            first_at = time.monotonic()
            if first != b"one\n":
                raise AssertionError(f"first chunk = {first!r}; want b'one\\n'")
            second = resp.readline()
            second_at = time.monotonic()
            if second != b"two\n":
                raise AssertionError(f"second chunk = {second!r}; want b'two\\n'")
            gap = second_at - first_at
            if gap < 0.3:
                raise AssertionError(
                    f"the chunks arrived {gap * 1000:.0f} ms apart: the response was buffered, not streamed (the application waits 500 ms)"
                )
        finally:
            conn.close()

    _with_app(ctx, "stream", body)



IMPLS = {
    "lambda-web-endpoints:ServeEndpointRequest": ServeEndpointRequest,
    "lambda-web-endpoints:ForwardRequestHeaders": ForwardRequestHeaders,
    "lambda-web-endpoints:AuthorizationHeader": AuthorizationHeader,
    "lambda-web-endpoints:LatestRevisionServesNewRevision": LatestRevisionServesNewRevision,
    "lambda-web-endpoints:WeightedRouting": WeightedRouting,
    "lambda-web-endpoints:IamAuthRefusesUnsignedRequest": IamAuthRefusesUnsignedRequest,
    "lambda-web-endpoints:ImportCodeError": ImportCodeError,
    "lambda-web-endpoints:ListenTimeout": ListenTimeout,
    "lambda-web-endpoints:UnknownDomain": UnknownDomain,
    "lambda-web-endpoints:UnsupportedMethod": UnsupportedMethod,
    "lambda-web-endpoints:EndpointDisabled": EndpointDisabled,
    "lambda-web-endpoints:EndpointRateLimit": EndpointRateLimit,
    "lambda-web-endpoints:RequestTimeout": RequestTimeout,
    "lambda-web-endpoints:DeletedEndpointDomain": DeletedEndpointDomain,
    "lambda-web-endpoints:StreamedResponse": StreamedResponse,
    "lambda-web-functions:CreateWebFunction": CreateWebFunction,
    "lambda-web-functions:GetWebFunction": GetWebFunction,
    "lambda-web-functions:ListWebFunctions": ListWebFunctions,
    "lambda-web-functions:GetWebFunctionRevision": GetWebFunctionRevision,
    "lambda-web-functions:CreateWebFunctionRevision": CreateWebFunctionRevision,
    "lambda-web-functions:GetWebFunctionEndpointLatestRevision": GetWebFunctionEndpointLatestRevision,
    "lambda-web-functions:ListWebFunctionRevisions": ListWebFunctionRevisions,
    "lambda-web-functions:CreateWebFunctionEndpoint": CreateWebFunctionEndpoint,
    "lambda-web-functions:GetWebFunctionEndpoint": GetWebFunctionEndpoint,
    "lambda-web-functions:ListWebFunctionEndpoints": ListWebFunctionEndpoints,
    "lambda-web-functions:UpdateWebFunctionEndpoint": UpdateWebFunctionEndpoint,
    "lambda-web-functions:UpdateWebFunctionEndpointClearThrottle": UpdateWebFunctionEndpointClearThrottle,
    "lambda-web-functions:DeleteWebFunctionRevisionServingTraffic": DeleteWebFunctionRevisionServingTraffic,
    "lambda-web-functions:TagResource": TagResource,
    "lambda-web-functions:ListTags": ListTags,
    "lambda-web-functions:UntagResource": UntagResource,
    "lambda-web-functions:PutResourcePolicy": PutResourcePolicy,
    "lambda-web-functions:GetResourcePolicy": GetResourcePolicy,
    "lambda-web-functions:PutResourcePolicyPublicPrincipal": PutResourcePolicyPublicPrincipal,
    "lambda-web-functions:DeleteResourcePolicy": DeleteResourcePolicy,
    "lambda-web-functions:GetWebAccountSettings": GetWebAccountSettings,
    "lambda-web-functions:DeleteWebFunctionEndpoint": DeleteWebFunctionEndpoint,
    "lambda-web-functions:DeleteWebFunctionRevision": DeleteWebFunctionRevision,
    "lambda-web-functions:DeleteWebFunction": DeleteWebFunction,
}


def _teardown(ctx: TestContext) -> None:
    # Deleting the function removes its revisions, endpoints, tags and policy,
    # but teardown says so explicitly rather than relying on the cascade.
    web = _web(ctx)
    name = _fn_name(ctx)
    calls = []
    arn = ctx.get("lw_function_arn")
    if arn:
        calls.append(lambda: web.delete_resource_policy(resourceArn=arn))
    for endpoint in (_CANARY, _LIVE):
        calls.append(lambda endpoint=endpoint: web.delete_web_function_endpoint(functionName=name, endpointName=endpoint))
    for key in ("lw_revision_1", "lw_revision_2"):
        revision_id = ctx.get(key)
        if revision_id:
            calls.append(lambda revision_id=revision_id: web.delete_web_function_revision(functionName=name, revisionId=revision_id))
    calls.append(lambda: web.delete_web_function(functionName=name))
    for call in calls:
        try:
            call()
        except Exception:
            pass
    _delete_bucket(ctx, _code_bucket(ctx))


def _setup(ctx: TestContext) -> None:
    _seed_bucket(ctx, _code_bucket(ctx), ["app-v1.zip", "app-v2.zip"], {"index.js": "// compat control-plane package\n"})


SETUP = {
    "lambda-web-functions": _setup,
    "lambda-web-endpoints": _setup_endpoints,
}
TEARDOWN = {
    "lambda-web-functions": _teardown,
    "lambda-web-endpoints": _teardown_endpoints,
}
