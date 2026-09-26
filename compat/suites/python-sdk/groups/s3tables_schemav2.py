"""
groups/s3tables_schemav2.py — S3 Tables CreateTable with a nested schemaV2, for the Python suite.

s3tables-schemav2 creates a table whose schemaV2 holds a list, a map and a
struct as Iceberg type documents, partitioned by a field of the struct, then
reads back the metadata file CreateTable wrote.
"""

from __future__ import annotations

from lib.harness import TestContext
from lib.clients import make_clients

from groups.glue_s3tables import table_bucket_name

_TABLE = "events"

_SCHEMA_V2 = {
    "type": "struct",
    "identifierFieldIds": [1],
    "fields": [
        {"id": 1, "name": "id", "required": True, "type": "long"},
        {"id": 2, "name": "tags", "required": False,
         "type": {"type": "list", "element-id": 5, "element": "string", "element-required": False}},
        {"id": 3, "name": "attributes", "required": False,
         "type": {"type": "map", "key-id": 6, "key": "string", "value-id": 7, "value": "string", "value-required": False}},
        {"id": 4, "name": "customer", "required": True,
         "type": {"type": "struct", "fields": [{"id": 8, "name": "region", "required": True, "type": "string"}]}},
    ],
}


def _s3tables(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region)._get("s3tables")


def _bucket(ctx: TestContext) -> str:
    return table_bucket_name("s3tables-v2-", ctx.run_id)


def _table(ctx: TestContext) -> dict:
    """The group's table, as the S3 Tables calls address it; namespaces allow underscores, not hyphens."""
    return {"tableBucketARN": ctx.get("s3tables_v2_bucket_arn"), "namespace": _bucket(ctx).replace("-", "_"), "name": _TABLE}


def _setup(ctx: TestContext) -> None:
    arn = _s3tables(ctx).create_table_bucket(name=_bucket(ctx))["arn"]
    ctx["s3tables_v2_bucket_arn"] = arn
    _s3tables(ctx).create_namespace(tableBucketARN=arn, namespace=[_table(ctx)["namespace"]])


def _teardown(ctx: TestContext) -> None:
    if not ctx.get("s3tables_v2_bucket_arn"):
        return
    table, client = _table(ctx), _s3tables(ctx)
    for call in (
        lambda: client.delete_table(**table),
        lambda: client.delete_namespace(tableBucketARN=table["tableBucketARN"], namespace=table["namespace"]),
        lambda: client.delete_table_bucket(tableBucketARN=table["tableBucketARN"]),
    ):
        try:
            call()
        except Exception:
            pass


# ── s3tables-schemav2 ─────────────────────────────────────────────────────────

def CreateTable(ctx: TestContext) -> None:
    resp = _s3tables(ctx).create_table(
        **_table(ctx), format="ICEBERG",
        metadata={"iceberg": {
            "schemaV2": _SCHEMA_V2,
            "partitionSpec": {"fields": [{"sourceId": 8, "transform": "identity", "name": "region"}]},
        }},
    )
    if "/table/" not in resp.get("tableARN", "") or not resp.get("versionToken"):
        raise AssertionError(f"CreateTable: tableARN {resp.get('tableARN')!r}, versionToken {resp.get('versionToken')!r}")


def GetTableMetadataLocation(ctx: TestContext) -> None:
    resp = _s3tables(ctx).get_table_metadata_location(**_table(ctx))
    loc, warehouse = resp.get("metadataLocation", ""), resp.get("warehouseLocation", "")
    if not warehouse or not loc.startswith(warehouse + "/metadata/") or not loc.endswith(".metadata.json"):
        raise AssertionError(f"GetTableMetadataLocation: metadataLocation {loc!r}, warehouseLocation {warehouse!r}")


# ── ImplMap ───────────────────────────────────────────────────────────────────

IMPLS = {
    "s3tables-schemav2:CreateTable": CreateTable,
    "s3tables-schemav2:GetTableMetadataLocation": GetTableMetadataLocation,
}

SETUP = {
    "s3tables-schemav2": _setup,
}
TEARDOWN = {
    "s3tables-schemav2": _teardown,
}
