"""
groups/lambdaweb.py — Lambda Web Functions compatibility test implementations for the Python suite.

Lambda Web Functions is its own service ("lambda-web", API version 2025-03-07),
separate from Lambda: boto3 names it ``lambda-web`` and signs for "lambda".
"""

from __future__ import annotations

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


def _build_config(key: str) -> dict:
    """Code in a zip the emulator never fetches, so the bucket need not exist."""
    return {
        "codeConfig": {"s3Object": {"bucket": "compat-web-code", "key": key}},
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
            "buildConfig": _build_config("app-v1.zip"),
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
        buildConfig=_build_config("app-v2.zip"),
        serviceConfig={"executionRoleArn": _ROLE_ARN, "environmentVariables": {"MODE": "prod"}},
    )
    revision_id = resp.get("revisionId", "")
    if not revision_id or revision_id == ctx.get("lw_revision_1"):
        raise AssertionError(f"CreateWebFunctionRevision: revisionId {revision_id!r} is empty or the first revision's")
    ctx["lw_revision_2"] = revision_id

    got = web.get_web_function_revision(functionName=_fn_name(ctx), revisionId=revision_id)
    s3_object = ((got.get("buildConfig") or {}).get("codeConfig") or {}).get("s3Object") or {}
    env = (got.get("serviceConfig") or {}).get("environmentVariables") or {}
    if got.get("description") != "second revision" or env.get("MODE") != "prod" or s3_object.get("key") != "app-v2.zip":
        raise AssertionError(f"GetWebFunctionRevision after create: {got}")


def GetWebFunctionEndpointLatestRevision(ctx: TestContext) -> None:
    _, second = _revision_ids(ctx)
    resp = _get_endpoint(ctx, _LIVE)
    if resp.get("autoDeploymentMode") != "LatestRevision" or resp.get("state") != "Active":
        raise AssertionError(
            f"GetWebFunctionEndpoint: mode {resp.get('autoDeploymentMode')!r} state {resp.get('state')!r}"
        )
    # The LatestRevision endpoint now serves the new revision with all traffic.
    weights = resp.get("revisionWeights", [])
    if len(weights) != 1 or weights[0].get("revisionId") != second or weights[0].get("weight") != 100:
        raise AssertionError(f"GetWebFunctionEndpoint: revisionWeights {weights}, want {second} at 100")


def ListWebFunctionRevisions(ctx: TestContext) -> None:
    first, second = _revision_ids(ctx)
    seen: set[str] = set()
    kwargs: dict = {"functionName": _fn_name(ctx)}
    while True:
        resp = _web(ctx).list_web_function_revisions(**kwargs)
        seen.update(r.get("revisionId", "") for r in resp.get("revisions", []))
        token = resp.get("nextToken")
        if not token:
            break
        kwargs["nextToken"] = token
    if seen != {first, second}:
        raise AssertionError(f"ListWebFunctionRevisions: got {sorted(seen)}, want exactly {first} and {second}")


def CreateWebFunctionEndpoint(ctx: TestContext) -> None:
    first, second = _revision_ids(ctx)
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


IMPLS = {
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


SETUP: dict = {}
TEARDOWN = {
    "lambda-web-functions": _teardown,
}
