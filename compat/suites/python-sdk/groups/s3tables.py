"""
groups/s3tables.py — S3 Tables compatibility test implementations for the Python suite.
"""

from __future__ import annotations

import re

from botocore.exceptions import ClientError

from lib.harness import TestContext
from lib.clients import make_clients

_TABLE = "orders"


def _s3tables(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region)._get("s3tables")


def _bucket_name(ctx: TestContext) -> str:
    """The run's table bucket: lowercase letters, digits and hyphens only."""
    run = re.sub(r"[^a-z0-9-]", "", ctx.run_id.lower()).strip("-")
    return ("s3tables-tables-" + run)[:63].rstrip("-")


def _namespace(ctx: TestContext) -> str:
    """Namespace names allow underscores, not hyphens."""
    return _bucket_name(ctx).replace("-", "_")


def _arn(ctx: TestContext) -> str:
    arn = ctx.get("s3tables_bucket_arn")
    if not arn:
        raise AssertionError("no table bucket from CreateTableBucket")
    return arn


def _table_args(ctx: TestContext) -> dict:
    return {"tableBucketARN": _arn(ctx), "namespace": _namespace(ctx), "name": _TABLE}


def _error_code(err: ClientError) -> str:
    return err.response.get("Error", {}).get("Code", "")


# ── s3tables-tables ───────────────────────────────────────────────────────────

def CreateTableBucket(ctx: TestContext) -> None:
    name = _bucket_name(ctx)
    resp = _s3tables(ctx).create_table_bucket(name=name)
    arn = resp.get("arn", "")
    if not arn.startswith("arn:aws:s3tables:") or not arn.endswith(":bucket/" + name):
        raise AssertionError(f"CreateTableBucket: unexpected ARN {arn!r}")
    ctx["s3tables_bucket_arn"] = arn


def GetTableBucket(ctx: TestContext) -> None:
    arn = _arn(ctx)
    resp = _s3tables(ctx).get_table_bucket(tableBucketARN=arn)
    if resp.get("arn") != arn or resp.get("name") != _bucket_name(ctx):
        raise AssertionError(f"GetTableBucket: got {resp.get('arn')!r} / {resp.get('name')!r}")
    if not resp.get("createdAt") or not resp.get("ownerAccountId"):
        raise AssertionError("GetTableBucket: missing createdAt or ownerAccountId")


def ListTableBuckets(ctx: TestContext) -> None:
    name = _bucket_name(ctx)
    resp = _s3tables(ctx).list_table_buckets(prefix=name)
    if not any(b.get("name") == name for b in resp.get("tableBuckets", [])):
        raise AssertionError(f"ListTableBuckets: {name!r} not listed")


def CreateNamespace(ctx: TestContext) -> None:
    arn = _arn(ctx)
    ns = _namespace(ctx)
    resp = _s3tables(ctx).create_namespace(tableBucketARN=arn, namespace=[ns])
    if resp.get("namespace") != [ns] or resp.get("tableBucketARN") != arn:
        raise AssertionError(f"CreateNamespace: got {resp}")


def ListNamespaces(ctx: TestContext) -> None:
    resp = _s3tables(ctx).list_namespaces(tableBucketARN=_arn(ctx))
    namespaces = resp.get("namespaces", [])
    if len(namespaces) != 1 or namespaces[0].get("namespace") != [_namespace(ctx)]:
        raise AssertionError(f"ListNamespaces: got {namespaces}")


def CreateTable(ctx: TestContext) -> None:
    arn = _arn(ctx)
    resp = _s3tables(ctx).create_table(
        tableBucketARN=arn, namespace=_namespace(ctx), name=_TABLE, format="ICEBERG"
    )
    table_arn = resp.get("tableARN", "")
    if not table_arn.startswith(arn + "/table/") or not resp.get("versionToken"):
        raise AssertionError(f"CreateTable: got {resp}")
    ctx["s3tables_table_arn"] = table_arn


def GetTable(ctx: TestContext) -> None:
    resp = _s3tables(ctx).get_table(**_table_args(ctx))
    if resp.get("name") != _TABLE or resp.get("format") != "ICEBERG":
        raise AssertionError(f"GetTable: got name {resp.get('name')!r} format {resp.get('format')!r}")
    if resp.get("tableARN") != ctx.get("s3tables_table_arn"):
        raise AssertionError(f"GetTable: tableARN {resp.get('tableARN')!r}")
    if not str(resp.get("warehouseLocation", "")).startswith("s3://"):
        raise AssertionError(f"GetTable: warehouseLocation {resp.get('warehouseLocation')!r}")


def ListTables(ctx: TestContext) -> None:
    resp = _s3tables(ctx).list_tables(tableBucketARN=_arn(ctx), namespace=_namespace(ctx))
    tables = resp.get("tables", [])
    if len(tables) != 1 or tables[0].get("name") != _TABLE:
        raise AssertionError(f"ListTables: got {tables}")


def UpdateTableMetadataLocation(ctx: TestContext) -> None:
    client = _s3tables(ctx)
    cur = client.get_table_metadata_location(**_table_args(ctx))
    loc = cur["warehouseLocation"] + "/metadata/00001-compat.metadata.json"
    resp = client.update_table_metadata_location(
        **_table_args(ctx), versionToken=cur["versionToken"], metadataLocation=loc
    )
    if resp.get("metadataLocation") != loc or resp.get("versionToken") == cur["versionToken"]:
        raise AssertionError(f"UpdateTableMetadataLocation: got {resp}")
    ctx["s3tables_stale_token"] = cur["versionToken"]


def UpdateTableMetadataLocationStaleToken(ctx: TestContext) -> None:
    client = _s3tables(ctx)
    cur = client.get_table_metadata_location(**_table_args(ctx))
    try:
        client.update_table_metadata_location(
            **_table_args(ctx),
            versionToken=ctx.get("s3tables_stale_token"),
            metadataLocation=cur["warehouseLocation"] + "/metadata/00002-compat.metadata.json",
        )
    except ClientError as err:
        if _error_code(err) != "ConflictException":
            raise AssertionError(f"stale token: expected ConflictException, got {_error_code(err)}")
        if err.response["ResponseMetadata"]["HTTPStatusCode"] != 409:
            raise AssertionError("stale token: expected HTTP 409")
        return
    raise AssertionError("stale token: expected ConflictException, got success")


def DeleteTable(ctx: TestContext) -> None:
    client = _s3tables(ctx)
    client.delete_table(**_table_args(ctx))
    try:
        client.get_table(**_table_args(ctx))
    except ClientError as err:
        if _error_code(err) != "NotFoundException":
            raise AssertionError(f"GetTable after DeleteTable: expected NotFoundException, got {_error_code(err)}")
        return
    raise AssertionError("GetTable after DeleteTable: table still exists")


def DeleteNamespace(ctx: TestContext) -> None:
    _s3tables(ctx).delete_namespace(tableBucketARN=_arn(ctx), namespace=_namespace(ctx))


def DeleteTableBucket(ctx: TestContext) -> None:
    client = _s3tables(ctx)
    arn = _arn(ctx)
    client.delete_table_bucket(tableBucketARN=arn)
    try:
        client.get_table_bucket(tableBucketARN=arn)
    except ClientError as err:
        if _error_code(err) != "NotFoundException":
            raise AssertionError(f"GetTableBucket after delete: expected NotFoundException, got {_error_code(err)}")
        return
    raise AssertionError("GetTableBucket after delete: bucket still exists")


IMPLS = {
    "s3tables-tables:CreateTableBucket": CreateTableBucket,
    "s3tables-tables:GetTableBucket": GetTableBucket,
    "s3tables-tables:ListTableBuckets": ListTableBuckets,
    "s3tables-tables:CreateNamespace": CreateNamespace,
    "s3tables-tables:ListNamespaces": ListNamespaces,
    "s3tables-tables:CreateTable": CreateTable,
    "s3tables-tables:GetTable": GetTable,
    "s3tables-tables:ListTables": ListTables,
    "s3tables-tables:UpdateTableMetadataLocation": UpdateTableMetadataLocation,
    "s3tables-tables:UpdateTableMetadataLocationStaleToken": UpdateTableMetadataLocationStaleToken,
    "s3tables-tables:DeleteTable": DeleteTable,
    "s3tables-tables:DeleteNamespace": DeleteNamespace,
    "s3tables-tables:DeleteTableBucket": DeleteTableBucket,
}


def _teardown(ctx: TestContext) -> None:
    arn = ctx.get("s3tables_bucket_arn")
    if not arn:
        return
    client = _s3tables(ctx)
    for call in (
        lambda: client.delete_table(**_table_args(ctx)),
        lambda: client.delete_namespace(tableBucketARN=arn, namespace=_namespace(ctx)),
        lambda: client.delete_table_bucket(tableBucketARN=arn),
    ):
        try:
            call()
        except Exception:
            pass


SETUP: dict = {}
TEARDOWN = {
    "s3tables-tables": _teardown,
}
