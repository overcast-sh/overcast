"""
groups/cognito.py — Cognito User Pools compatibility test implementations for the Python suite.

Groups: cognito-token-validity. cognito-userpools resolves through its
authored scenario (compat/model/authored/cognito-userpools.json).
"""

from __future__ import annotations
from lib.harness import TestContext
from lib.clients import make_clients


def _cognito(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region)._get("cognito-idp")


# ── ImplMap ───────────────────────────────────────────────────────────────────

IMPLS = {
    "cognito-token-validity:CreateUserPoolClientWithTokenValidity": lambda ctx: CreateClientTokenValidity(ctx),
    "cognito-token-validity:DescribeUserPoolClientTokenValidity": lambda ctx: DescribeClientTokenValidity(ctx),
    "cognito-token-validity:UpdateUserPoolClientTokenValidity": lambda ctx: UpdateClientTokenValidity(ctx),
    "cognito-token-validity:DeleteUserPoolClient": lambda ctx: DeleteUserPoolClientFn(ctx),
}

SETUP = {
    "cognito-token-validity": lambda ctx: None,
}
TEARDOWN = {
    "cognito-token-validity": lambda ctx: _teardown_token_validity(ctx),
}


# ── cognito-token-validity ────────────────────────────────────────────────────

def _teardown_token_validity(ctx: TestContext) -> None:
    cog = _cognito(ctx)
    pool_id = ctx.get("tv_pool_id")
    client_id = ctx.get("tv_client_id")
    if pool_id and client_id:
        try:
            cog.delete_user_pool_client(UserPoolId=pool_id, ClientId=client_id)
        except Exception:
            pass
    if pool_id:
        try:
            cog.delete_user_pool(UserPoolId=pool_id)
        except Exception:
            pass


def CreateClientTokenValidity(ctx: TestContext) -> None:
    cog = _cognito(ctx)
    pool_name = f"compat-tv-{ctx.run_id}"
    resp = cog.create_user_pool(PoolName=pool_name)
    pool_id = resp.get("UserPool", {}).get("Id")
    if not pool_id:
        raise AssertionError("CreateClientTokenValidity: missing pool Id")
    ctx["tv_pool_id"] = pool_id

    resp = cog.create_user_pool_client(
        UserPoolId=pool_id,
        ClientName=f"compat-client-{ctx.run_id}",
        AccessTokenValidity=2,
        IdTokenValidity=3,
        RefreshTokenValidity=7,
        TokenValidityUnits={
            "AccessToken": "hours",
            "IdToken": "hours",
            "RefreshToken": "days",
        },
    )
    client = resp.get("UserPoolClient", {})
    if not client.get("ClientId"):
        raise AssertionError("CreateClientTokenValidity: missing ClientId")
    ctx["tv_client_id"] = client["ClientId"]


def DescribeClientTokenValidity(ctx: TestContext) -> None:
    cog = _cognito(ctx)
    pool_id = ctx.get("tv_pool_id")
    client_id = ctx.get("tv_client_id")
    if not pool_id or not client_id:
        raise AssertionError("DescribeClientTokenValidity: missing pool/client")
    cog.describe_user_pool_client(UserPoolId=pool_id, ClientId=client_id)


def UpdateClientTokenValidity(ctx: TestContext) -> None:
    cog = _cognito(ctx)
    pool_id = ctx.get("tv_pool_id")
    client_id = ctx.get("tv_client_id")
    if not pool_id or not client_id:
        raise AssertionError("UpdateClientTokenValidity: missing pool/client")
    cog.update_user_pool_client(
        UserPoolId=pool_id,
        ClientId=client_id,
        AccessTokenValidity=30,
        TokenValidityUnits={
            "AccessToken": "minutes",
            "IdToken": "hours",
            "RefreshToken": "days",
        },
    )


def DeleteUserPoolClientFn(ctx: TestContext) -> None:
    cog = _cognito(ctx)
    pool_id = ctx.get("tv_pool_id")
    client_id = ctx.get("tv_client_id")
    if not pool_id or not client_id:
        return
    cog.delete_user_pool_client(UserPoolId=pool_id, ClientId=client_id)
