"""
groups/glue_s3tables.py — S3 Tables through Glue's s3tablescatalog, for the Python suite.

glue-s3tables-catalog reads a table bucket through the federated catalog:
its child catalog, the namespace as a database and the table as an Iceberg
table.
"""

from __future__ import annotations

import re

from lib.harness import TestContext
from lib.clients import make_clients

_TABLE = "orders"


def _client(ctx: TestContext, service: str):
    return make_clients(ctx.endpoint, ctx.region)._get(service)


def table_bucket_name(prefix: str, run_id: str) -> str:
    """A run's table bucket under prefix: lowercase letters, digits and hyphens only."""
    run = re.sub(r"[^a-z0-9-]", "", run_id.lower()).strip("-")
    return (prefix + run)[:63].rstrip("-")


def _bucket(ctx: TestContext) -> str:
    return table_bucket_name("glue-s3tables-", ctx.run_id)


def _namespace(ctx: TestContext) -> str:
    """Namespace names allow underscores, not hyphens."""
    return _bucket(ctx).replace("-", "_")


def _catalog_id(ctx: TestContext) -> str:
    catalog_id = ctx.get("glue_s3tables_catalog_id")
    if not catalog_id:
        raise AssertionError("no catalog ID from GetCatalogs")
    return catalog_id


def _setup(ctx: TestContext) -> None:
    s3tables = _client(ctx, "s3tables")
    arn = s3tables.create_table_bucket(name=_bucket(ctx))["arn"]
    ctx["glue_s3tables_bucket_arn"] = arn
    s3tables.create_namespace(tableBucketARN=arn, namespace=[_namespace(ctx)])
    s3tables.create_table(
        tableBucketARN=arn, namespace=_namespace(ctx), name=_TABLE, format="ICEBERG",
        metadata={"iceberg": {"schema": {"fields": [{"name": "id", "type": "long", "required": True}]}}},
    )


def _teardown(ctx: TestContext) -> None:
    arn = ctx.get("glue_s3tables_bucket_arn")
    if not arn:
        return
    s3tables = _client(ctx, "s3tables")
    for call in (
        lambda: s3tables.delete_table(tableBucketARN=arn, namespace=_namespace(ctx), name=_TABLE),
        lambda: s3tables.delete_namespace(tableBucketARN=arn, namespace=_namespace(ctx)),
        lambda: s3tables.delete_table_bucket(tableBucketARN=arn),
    ):
        try:
            call()
        except Exception:
            pass


# ── glue-s3tables-catalog ─────────────────────────────────────────────────────

def GetCatalogs(ctx: TestContext) -> None:
    resp = _client(ctx, "glue").get_catalogs(ParentCatalogId="s3tablescatalog")
    for catalog in resp.get("CatalogList", []):
        if catalog.get("Name") == _bucket(ctx):
            catalog_id = catalog.get("CatalogId", "")
            if not catalog_id.endswith(":s3tablescatalog/" + _bucket(ctx)):
                raise AssertionError(f"GetCatalogs: CatalogId {catalog_id!r}")
            ctx["glue_s3tables_catalog_id"] = catalog_id
            return
    raise AssertionError(f"GetCatalogs: {_bucket(ctx)!r} not listed under s3tablescatalog")


def GetCatalog(ctx: TestContext) -> None:
    catalog = _client(ctx, "glue").get_catalog(CatalogId=_catalog_id(ctx))["Catalog"]
    federated = catalog.get("FederatedCatalog", {})
    if (catalog.get("Name") != _bucket(ctx) or federated.get("ConnectionName") != "aws:s3tables"
            or federated.get("Identifier") != ctx.get("glue_s3tables_bucket_arn")):
        raise AssertionError(f"GetCatalog: got {catalog}")


def GetDatabases(ctx: TestContext) -> None:
    dbs = _client(ctx, "glue").get_databases(CatalogId=_catalog_id(ctx)).get("DatabaseList", [])
    if [db.get("Name") for db in dbs] != [_namespace(ctx)]:
        raise AssertionError(f"GetDatabases: got {dbs}")


def GetTable(ctx: TestContext) -> None:
    table = _client(ctx, "glue").get_table(
        CatalogId=_catalog_id(ctx), DatabaseName=_namespace(ctx), Name=_TABLE)["Table"]
    params = table.get("Parameters", {})
    if params.get("table_type", "").upper() != "ICEBERG" or not params.get("metadata_location"):
        raise AssertionError(f"GetTable: Parameters {params}")


# ── ImplMap ───────────────────────────────────────────────────────────────────

IMPLS = {
    "glue-s3tables-catalog:GetCatalogs": GetCatalogs,
    "glue-s3tables-catalog:GetCatalog": GetCatalog,
    "glue-s3tables-catalog:GetDatabases": GetDatabases,
    "glue-s3tables-catalog:GetTable": GetTable,
}

SETUP = {
    "glue-s3tables-catalog": _setup,
}
TEARDOWN = {
    "glue-s3tables-catalog": _teardown,
}
