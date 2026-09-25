"""
groups/glue.py — Glue Data Catalog compatibility test implementations for the Python suite.

glue-catalog drives one partitioned table through its definition, an
optimistic-concurrency update, its versions and its partitions.
"""

from __future__ import annotations

from botocore.exceptions import ClientError

from lib.harness import TestContext
from lib.clients import make_clients


def _glue(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region)._get("glue")


# Glue folds names to lowercase; the run id already is, so these round-trip.
def _db(ctx: TestContext) -> str:
    return f"{ctx.run_id}-glue-catalog-db"


def _table(ctx: TestContext) -> str:
    return f"{ctx.run_id}-glue-catalog-events"


def _table_input(ctx: TestContext, parameters: dict) -> dict:
    return {
        "Name": _table(ctx),
        "TableType": "EXTERNAL_TABLE",
        "Parameters": parameters,
        "PartitionKeys": [{"Name": "year", "Type": "int"}, {"Name": "month", "Type": "string"}],
        "StorageDescriptor": {
            "Location": "s3://compat-glue-catalog/events/",
            "Columns": [{"Name": "id", "Type": "bigint"}, {"Name": "payload", "Type": "string"}],
            "SerdeInfo": {"SerializationLibrary": "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe"},
        },
    }


def _error_code(exc: ClientError) -> str:
    return exc.response.get("Error", {}).get("Code", "")


def _expect_not_found(what: str, call) -> None:
    try:
        call()
    except ClientError as exc:
        if _error_code(exc) == "EntityNotFoundException":
            return
        raise AssertionError(f"{what}: want EntityNotFoundException, got {_error_code(exc)}") from exc
    raise AssertionError(f"{what}: succeeded, want EntityNotFoundException")


# ── glue-catalog ──────────────────────────────────────────────────────────────

def CreateDatabase(ctx: TestContext) -> None:
    g = _glue(ctx)
    g.create_database(DatabaseInput={"Name": _db(ctx), "Description": "compat"})
    db = g.get_database(Name=_db(ctx))["Database"]
    if db.get("Name") != _db(ctx) or db.get("Description") != "compat":
        raise AssertionError(f"GetDatabase: got {db.get('Name')!r} / {db.get('Description')!r}")


def CreateTable(ctx: TestContext) -> None:
    _glue(ctx).create_table(DatabaseName=_db(ctx), TableInput=_table_input(ctx, {"classification": "parquet"}))


def GetTable(ctx: TestContext) -> None:
    table = _glue(ctx).get_table(DatabaseName=_db(ctx), Name=_table(ctx))["Table"]
    sd = table.get("StorageDescriptor") or {}
    if sd.get("Location") != "s3://compat-glue-catalog/events/" or len(sd.get("Columns", [])) != 2:
        raise AssertionError(f"GetTable: StorageDescriptor not returned as created: {sd}")
    if len(table.get("PartitionKeys", [])) != 2 or table.get("Parameters", {}).get("classification") != "parquet":
        raise AssertionError(f"GetTable: PartitionKeys/Parameters not returned: {table}")
    if not table.get("VersionId") or not table.get("CreateTime"):
        raise AssertionError("GetTable: missing VersionId or CreateTime")
    ctx["glue_version"] = table["VersionId"]


def UpdateTable(ctx: TestContext) -> None:
    g = _glue(ctx)
    read = ctx.get("glue_version")
    g.update_table(DatabaseName=_db(ctx), VersionId=read,
                   TableInput=_table_input(ctx, {"classification": "parquet", "compat": "updated"}))
    table = g.get_table(DatabaseName=_db(ctx), Name=_table(ctx))["Table"]
    if table.get("Parameters", {}).get("compat") != "updated" or table.get("VersionId") == read:
        raise AssertionError(f"UpdateTable: Parameters {table.get('Parameters')}, VersionId {table.get('VersionId')!r} (was {read!r})")


def UpdateTableStaleVersion(ctx: TestContext) -> None:
    try:
        _glue(ctx).update_table(DatabaseName=_db(ctx), VersionId=ctx.get("glue_version"),
                                TableInput=_table_input(ctx, {"compat": "stale"}))
    except ClientError as exc:
        if _error_code(exc) == "ConcurrentModificationException":
            return
        raise AssertionError(f"stale UpdateTable: want ConcurrentModificationException, got {_error_code(exc)}") from exc
    raise AssertionError("stale UpdateTable succeeded, want ConcurrentModificationException")


def GetTableVersions(ctx: TestContext) -> None:
    versions = _glue(ctx).get_table_versions(DatabaseName=_db(ctx), TableName=_table(ctx))["TableVersions"]
    if not any(v.get("VersionId") == ctx.get("glue_version") for v in versions):
        raise AssertionError(f"GetTableVersions: pre-update version {ctx.get('glue_version')!r} not listed")


def UpdateColumnStatisticsForTable(ctx: TestContext) -> None:
    resp = _glue(ctx).update_column_statistics_for_table(
        DatabaseName=_db(ctx), TableName=_table(ctx),
        ColumnStatisticsList=[{
            "ColumnName": "id", "ColumnType": "bigint", "AnalyzedTime": 1700000000,
            "StatisticsData": {"Type": "LONG", "LongColumnStatisticsData": {
                "NumberOfNulls": 0, "NumberOfDistinctValues": 3, "MaximumValue": 9}},
        }],
    )
    if resp.get("Errors"):
        raise AssertionError(f"UpdateColumnStatisticsForTable: errors {resp['Errors']}")


def GetColumnStatisticsForTable(ctx: TestContext) -> None:
    resp = _glue(ctx).get_column_statistics_for_table(
        DatabaseName=_db(ctx), TableName=_table(ctx), ColumnNames=["id", "payload"])
    stats = resp.get("ColumnStatisticsList", [])
    if len(stats) != 1 or stats[0].get("ColumnName") != "id" or \
            stats[0]["StatisticsData"]["LongColumnStatisticsData"].get("NumberOfDistinctValues") != 3:
        raise AssertionError(f"GetColumnStatisticsForTable: {stats!r}")
    errors = resp.get("Errors", [])
    if len(errors) != 1 or errors[0].get("ColumnName") != "payload":
        raise AssertionError(f"GetColumnStatisticsForTable: errors {errors!r}, want payload, which has none")


def BatchCreatePartition(ctx: TestContext) -> None:
    inputs = [
        {"Values": [y, m], "StorageDescriptor": {"Location": f"s3://compat-glue-catalog/events/year={y}/month={m}/"}}
        for y, m in (("2023", "12"), ("2024", "01"), ("2024", "02"))
    ]
    resp = _glue(ctx).batch_create_partition(DatabaseName=_db(ctx), TableName=_table(ctx), PartitionInputList=inputs)
    if resp.get("Errors"):
        raise AssertionError(f"BatchCreatePartition: errors {resp['Errors']}")


def GetPartitions(ctx: TestContext) -> None:
    resp = _glue(ctx).get_partitions(DatabaseName=_db(ctx), TableName=_table(ctx),
                                     Expression="year = 2024 AND month IN ('01', '02')")
    got = sorted("/".join(p["Values"]) for p in resp.get("Partitions", []))
    if got != ["2024/01", "2024/02"]:
        raise AssertionError(f"GetPartitions: matched {got}, want ['2024/01', '2024/02']")


def DeletePartition(ctx: TestContext) -> None:
    g = _glue(ctx)
    g.delete_partition(DatabaseName=_db(ctx), TableName=_table(ctx), PartitionValues=["2023", "12"])
    _expect_not_found("GetPartition after DeletePartition", lambda: g.get_partition(
        DatabaseName=_db(ctx), TableName=_table(ctx), PartitionValues=["2023", "12"]))


def DeleteTable(ctx: TestContext) -> None:
    g = _glue(ctx)
    g.delete_table(DatabaseName=_db(ctx), Name=_table(ctx))
    _expect_not_found("GetTable after DeleteTable", lambda: g.get_table(DatabaseName=_db(ctx), Name=_table(ctx)))


def DeleteDatabase(ctx: TestContext) -> None:
    g = _glue(ctx)
    g.delete_database(Name=_db(ctx))
    _expect_not_found("GetDatabase after DeleteDatabase", lambda: g.get_database(Name=_db(ctx)))


# ── ImplMap ───────────────────────────────────────────────────────────────────

IMPLS = {
    "glue-catalog:CreateDatabase": CreateDatabase,
    "glue-catalog:CreateTable": CreateTable,
    "glue-catalog:GetTable": GetTable,
    "glue-catalog:UpdateTable": UpdateTable,
    "glue-catalog:UpdateTableStaleVersion": UpdateTableStaleVersion,
    "glue-catalog:GetTableVersions": GetTableVersions,
    "glue-catalog:UpdateColumnStatisticsForTable": UpdateColumnStatisticsForTable,
    "glue-catalog:GetColumnStatisticsForTable": GetColumnStatisticsForTable,
    "glue-catalog:BatchCreatePartition": BatchCreatePartition,
    "glue-catalog:GetPartitions": GetPartitions,
    "glue-catalog:DeletePartition": DeletePartition,
    "glue-catalog:DeleteTable": DeleteTable,
    "glue-catalog:DeleteDatabase": DeleteDatabase,
}

SETUP = {}
TEARDOWN = {
    "glue-catalog": lambda ctx: _teardown(ctx),
}


def _teardown(ctx: TestContext) -> None:
    # DeleteDatabase removes the table and its partitions with it.
    try:
        _glue(ctx).delete_database(Name=_db(ctx))
    except Exception:
        pass
