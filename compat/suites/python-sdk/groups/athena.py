"""
groups/athena.py — Athena control-plane compatibility test implementations for the Python suite.

athena-control drives a workgroup with a result location, a query run in it,
a named query, a prepared statement and a data catalog. athena-engine runs
Hive DDL that writes the Glue Data Catalog, then a query over a CSV table in
S3 on the engine. athena-s3tables creates, writes and reads an Iceberg table
in a table bucket's catalog, "s3tablescatalog/<bucket>".
"""

from __future__ import annotations

import time

from botocore.exceptions import ClientError

from groups.glue_s3tables import table_bucket_name
from lib.harness import TestContext
from lib.clients import make_clients

_RESULTS = "s3://compat-athena-control/results/"
_STATEMENT = "compat_by_id"


def _athena(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region)._get("athena")


def _wg(ctx: TestContext) -> str:
    return f"{ctx.run_id}-athena-control-wg"


def _catalog(ctx: TestContext) -> str:
    return f"{ctx.run_id}-athena-control-catalog"


def _expect_invalid_request(what: str, call) -> None:
    try:
        call()
    except ClientError as exc:
        code = exc.response.get("Error", {}).get("Code", "")
        if code == "InvalidRequestException":
            return
        raise AssertionError(f"{what}: want InvalidRequestException, got {code}") from exc
    raise AssertionError(f"{what}: succeeded, want InvalidRequestException")


# ── athena-control ────────────────────────────────────────────────────────────

def CreateWorkGroup(ctx: TestContext) -> None:
    a = _athena(ctx)
    a.create_work_group(Name=_wg(ctx), Configuration={"ResultConfiguration": {"OutputLocation": _RESULTS}})
    wg = a.get_work_group(WorkGroup=_wg(ctx))["WorkGroup"]
    location = wg.get("Configuration", {}).get("ResultConfiguration", {}).get("OutputLocation")
    if wg.get("Name") != _wg(ctx) or location != _RESULTS:
        raise AssertionError(f"GetWorkGroup: got {wg!r}")


def UpdateWorkGroup(ctx: TestContext) -> None:
    a = _athena(ctx)
    a.update_work_group(
        WorkGroup=_wg(ctx),
        Description="compat",
        ConfigurationUpdates={"EnforceWorkGroupConfiguration": True},
    )
    wg = a.get_work_group(WorkGroup=_wg(ctx))["WorkGroup"]
    if wg.get("Description") != "compat" or wg.get("Configuration", {}).get("EnforceWorkGroupConfiguration") is not True:
        raise AssertionError(f"GetWorkGroup after update: got {wg!r}")


def ListWorkGroups(ctx: TestContext) -> None:
    names = [w.get("Name") for w in _athena(ctx).list_work_groups().get("WorkGroups", [])]
    if _wg(ctx) not in names or "primary" not in names:
        raise AssertionError(f"ListWorkGroups: {names} lacks {_wg(ctx)} or primary")


def StartQueryExecution(ctx: TestContext) -> None:
    a = _athena(ctx)
    qid = a.start_query_execution(QueryString="SELECT 1", WorkGroup=_wg(ctx))["QueryExecutionId"]
    ctx["athena_query_id"] = qid
    qe = a.get_query_execution(QueryExecutionId=qid)["QueryExecution"]
    location = qe.get("ResultConfiguration", {}).get("OutputLocation")
    if qe.get("WorkGroup") != _wg(ctx) or location != f"{_RESULTS}{qid}.csv" or not qe.get("Status", {}).get("State"):
        raise AssertionError(f"GetQueryExecution: got {qe!r}")


def ListQueryExecutions(ctx: TestContext) -> None:
    ids = _athena(ctx).list_query_executions(WorkGroup=_wg(ctx)).get("QueryExecutionIds", [])
    if ctx["athena_query_id"] not in ids:
        raise AssertionError(f"ListQueryExecutions: {ids} lacks {ctx['athena_query_id']}")


def CreateNamedQuery(ctx: TestContext) -> None:
    a = _athena(ctx)
    nid = a.create_named_query(
        Name="compat-query", Database="compat_db", QueryString="SELECT 1", WorkGroup=_wg(ctx)
    )["NamedQueryId"]
    ctx["athena_named_query_id"] = nid
    nq = a.get_named_query(NamedQueryId=nid)["NamedQuery"]
    if nq.get("Name") != "compat-query" or nq.get("WorkGroup") != _wg(ctx):
        raise AssertionError(f"GetNamedQuery: got {nq!r}")


def BatchGetNamedQuery(ctx: TestContext) -> None:
    nid = ctx["athena_named_query_id"]
    out = _athena(ctx).batch_get_named_query(NamedQueryIds=[nid, "compat-missing-id"])
    found, missed = out.get("NamedQueries", []), out.get("UnprocessedNamedQueryIds", [])
    if len(found) != 1 or found[0].get("NamedQueryId") != nid or len(missed) != 1:
        raise AssertionError(f"BatchGetNamedQuery: {len(found)} found, {len(missed)} unprocessed")


def CreatePreparedStatement(ctx: TestContext) -> None:
    a = _athena(ctx)
    a.create_prepared_statement(
        StatementName=_STATEMENT, WorkGroup=_wg(ctx), QueryStatement="SELECT * FROM t WHERE id = ?"
    )
    ps = a.get_prepared_statement(StatementName=_STATEMENT, WorkGroup=_wg(ctx))["PreparedStatement"]
    if ps.get("QueryStatement") != "SELECT * FROM t WHERE id = ?":
        raise AssertionError(f"GetPreparedStatement: got {ps!r}")


def CreateDataCatalog(ctx: TestContext) -> None:
    a = _athena(ctx)
    a.create_data_catalog(
        Name=_catalog(ctx),
        Type="HIVE",
        Parameters={"metadata-function": "arn:aws:lambda:us-east-1:000000000000:function:compat-meta"},
    )
    dc = a.get_data_catalog(Name=_catalog(ctx))["DataCatalog"]
    if dc.get("Name") != _catalog(ctx) or dc.get("Type") != "HIVE":
        raise AssertionError(f"GetDataCatalog: got {dc!r}")


def ListEngineVersions(ctx: TestContext) -> None:
    selected = [v.get("SelectedEngineVersion") for v in _athena(ctx).list_engine_versions().get("EngineVersions", [])]
    if "AUTO" not in selected:
        raise AssertionError(f"ListEngineVersions: {selected} lacks AUTO")


def DeleteDataCatalog(ctx: TestContext) -> None:
    a = _athena(ctx)
    a.delete_data_catalog(Name=_catalog(ctx))
    _expect_invalid_request("GetDataCatalog after DeleteDataCatalog", lambda: a.get_data_catalog(Name=_catalog(ctx)))


def DeleteWorkGroup(ctx: TestContext) -> None:
    a = _athena(ctx)
    a.delete_work_group(WorkGroup=_wg(ctx), RecursiveDeleteOption=True)
    _expect_invalid_request("GetWorkGroup after DeleteWorkGroup", lambda: a.get_work_group(WorkGroup=_wg(ctx)))


# ── athena-engine ─────────────────────────────────────────────────────────────

# The first query waits for the engine to be pulled and started.
_ENGINE_QUERY_WAIT = 240


def _s3(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region)._get("s3")


def _engine_bucket(ctx: TestContext) -> str:
    return f"{ctx.run_id}-athena-engine"


def _engine_db(ctx: TestContext) -> str:
    # An identifier Hive DDL and Trino SQL both accept unquoted.
    return ctx.run_id.replace("-", "_") + "_athena_engine"


def _run(ctx: TestContext, query: str) -> str:
    """Start query and wait for it to succeed, returning its id."""
    return _run_query(ctx, query, ResultConfiguration={"OutputLocation": f"s3://{_engine_bucket(ctx)}/results/"})


def _run_query(ctx: TestContext, query: str, **start) -> str:
    """Start query with the further StartQueryExecution arguments start, and
    wait for it to succeed, returning its id."""
    a = _athena(ctx)
    qid = a.start_query_execution(QueryString=query, **start)["QueryExecutionId"]
    deadline = time.monotonic() + _ENGINE_QUERY_WAIT
    while True:
        status = a.get_query_execution(QueryExecutionId=qid)["QueryExecution"]["Status"]
        if status["State"] == "SUCCEEDED":
            return qid
        if status["State"] in ("FAILED", "CANCELLED"):
            raise AssertionError(f"{query}: {status['State']}: {status.get('StateChangeReason')}")
        if time.monotonic() > deadline:
            raise AssertionError(f"{query}: still unfinished after {_ENGINE_QUERY_WAIT}s")
        time.sleep(0.5)


def CreateDatabaseStatement(ctx: TestContext) -> None:
    _run(ctx, f"CREATE DATABASE {_engine_db(ctx)}")
    db = _athena(ctx).get_database(CatalogName="AwsDataCatalog", DatabaseName=_engine_db(ctx))["Database"]
    if db.get("Name") != _engine_db(ctx):
        raise AssertionError(f"GetDatabase: {db!r}")


def CreateExternalTableStatement(ctx: TestContext) -> None:
    _s3(ctx).put_object(Bucket=_engine_bucket(ctx), Key="people/part-0.csv", Body=b"1,alice\n2,bob\n")
    _run(ctx, f"CREATE EXTERNAL TABLE {_engine_db(ctx)}.people (id int, name string) "
              f"ROW FORMAT DELIMITED FIELDS TERMINATED BY ',' LOCATION 's3://{_engine_bucket(ctx)}/people/'")
    cols = _athena(ctx).get_table_metadata(
        CatalogName="AwsDataCatalog", DatabaseName=_engine_db(ctx), TableName="people")["TableMetadata"]["Columns"]
    if [c["Name"] for c in cols] != ["id", "name"] or cols[1].get("Type") != "string":
        raise AssertionError(f"GetTableMetadata: columns {cols!r}")


def SelectFromTable(ctx: TestContext) -> None:
    qid = _run(ctx, f"SELECT id, name FROM {_engine_db(ctx)}.people ORDER BY id")
    ctx["athena_engine_query"] = qid
    rs = _athena(ctx).get_query_results(QueryExecutionId=qid)["ResultSet"]
    rows = [[d.get("VarCharValue") for d in r["Data"]] for r in rs["Rows"]]
    if rows != [["id", "name"], ["1", "alice"], ["2", "bob"]]:
        raise AssertionError(f"GetQueryResults: rows {rows!r}, want the header then 1, alice and 2, bob")
    types = [c.get("Type") for c in rs["ResultSetMetadata"]["ColumnInfo"]]
    if types != ["integer", "varchar"]:
        raise AssertionError(f"GetQueryResults: column types {types!r}")


def GetQueryRuntimeStatistics(ctx: TestContext) -> None:
    stats = _athena(ctx).get_query_runtime_statistics(
        QueryExecutionId=ctx["athena_engine_query"])["QueryRuntimeStatistics"]
    if stats.get("Rows", {}).get("OutputRows") != 2 or "Timeline" not in stats:
        raise AssertionError(f"GetQueryRuntimeStatistics: {stats!r}")


def _engine_setup(ctx: TestContext) -> None:
    _s3(ctx).create_bucket(Bucket=_engine_bucket(ctx))


def _engine_teardown(ctx: TestContext) -> None:
    glue = make_clients(ctx.endpoint, ctx.region)._get("glue")
    try:
        glue.delete_database(Name=_engine_db(ctx))
    except Exception:
        pass
    s3 = _s3(ctx)
    try:
        for obj in s3.list_objects_v2(Bucket=_engine_bucket(ctx)).get("Contents", []):
            s3.delete_object(Bucket=_engine_bucket(ctx), Key=obj["Key"])
        s3.delete_bucket(Bucket=_engine_bucket(ctx))
    except Exception:
        pass


# ── athena-s3tables ───────────────────────────────────────────────────────────

_TABLES_NAMESPACE = "sales"
_TABLES_TABLE = "orders"


def _tables_bucket(ctx: TestContext) -> str:
    return table_bucket_name("athena-s3tables-", ctx.run_id)


def _tables_catalog(ctx: TestContext) -> str:
    return "s3tablescatalog/" + _tables_bucket(ctx)


def _tables_results(ctx: TestContext) -> str:
    """The S3 bucket query results are written to."""
    return f"{ctx.run_id}-athena-s3tables-results"


def _s3tables(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region)._get("s3tables")


def _run_in_bucket(ctx: TestContext, query: str) -> str:
    """Run query in the table bucket's catalog, in the namespace."""
    return _run_query(ctx, query, ResultConfiguration={"OutputLocation": f"s3://{_tables_results(ctx)}/"},
                      QueryExecutionContext={"Catalog": _tables_catalog(ctx), "Database": _TABLES_NAMESPACE})


def _metadata_location(ctx: TestContext) -> str:
    return _s3tables(ctx).get_table_metadata_location(
        tableBucketARN=ctx["athena_s3tables_bucket_arn"], namespace=_TABLES_NAMESPACE, name=_TABLES_TABLE,
    ).get("metadataLocation", "")


def CreateTableStatement(ctx: TestContext) -> None:
    _run_in_bucket(ctx, f"CREATE TABLE {_TABLES_TABLE} (id int, amount double) TBLPROPERTIES ('table_type' = 'iceberg')")
    location = _metadata_location(ctx)
    if not location:
        raise AssertionError("GetTableMetadataLocation: the created table has no metadata location")
    ctx["athena_s3tables_metadata_location"] = location


def InsertStatement(ctx: TestContext) -> None:
    qid = _run_in_bucket(ctx, f"INSERT INTO {_TABLES_TABLE} VALUES (1, 9.5), (2, 20.0)")
    count = _athena(ctx).get_query_results(QueryExecutionId=qid).get("UpdateCount")
    if count != 2:
        raise AssertionError(f"GetQueryResults: UpdateCount {count!r}, want 2")
    location = _metadata_location(ctx)
    if location == ctx["athena_s3tables_metadata_location"]:
        raise AssertionError(f"GetTableMetadataLocation: still {location!r} after the INSERT")


def SelectTableBucketRows(ctx: TestContext) -> None:
    query = f'SELECT id, amount FROM "{_tables_catalog(ctx)}"."{_TABLES_NAMESPACE}"."{_TABLES_TABLE}" ORDER BY id'
    qid = _run_query(ctx, query, ResultConfiguration={"OutputLocation": f"s3://{_tables_results(ctx)}/"})
    rs = _athena(ctx).get_query_results(QueryExecutionId=qid)["ResultSet"]
    rows = [[d.get("VarCharValue") for d in r["Data"]] for r in rs["Rows"]]
    if rows != [["id", "amount"], ["1", "9.5"], ["2", "20.0"]]:
        raise AssertionError(f"GetQueryResults: rows {rows!r}, want the header then 1, 9.5 and 2, 20.0")


def _tables_setup(ctx: TestContext) -> None:
    _s3(ctx).create_bucket(Bucket=_tables_results(ctx))
    arn = _s3tables(ctx).create_table_bucket(name=_tables_bucket(ctx))["arn"]
    ctx["athena_s3tables_bucket_arn"] = arn
    _s3tables(ctx).create_namespace(tableBucketARN=arn, namespace=[_TABLES_NAMESPACE])


def _tables_teardown(ctx: TestContext) -> None:
    arn = ctx.get("athena_s3tables_bucket_arn")
    if arn:
        s3tables = _s3tables(ctx)
        for call in (
            lambda: s3tables.delete_table(tableBucketARN=arn, namespace=_TABLES_NAMESPACE, name=_TABLES_TABLE),
            lambda: s3tables.delete_namespace(tableBucketARN=arn, namespace=_TABLES_NAMESPACE),
            lambda: s3tables.delete_table_bucket(tableBucketARN=arn),
        ):
            try:
                call()
            except Exception:
                pass
    s3, results = _s3(ctx), _tables_results(ctx)
    try:
        for obj in s3.list_objects_v2(Bucket=results).get("Contents", []):
            s3.delete_object(Bucket=results, Key=obj["Key"])
        s3.delete_bucket(Bucket=results)
    except Exception:
        pass


# ── ImplMap ───────────────────────────────────────────────────────────────────

IMPLS = {
    "athena-control:CreateWorkGroup": CreateWorkGroup,
    "athena-control:UpdateWorkGroup": UpdateWorkGroup,
    "athena-control:ListWorkGroups": ListWorkGroups,
    "athena-control:StartQueryExecution": StartQueryExecution,
    "athena-control:ListQueryExecutions": ListQueryExecutions,
    "athena-control:CreateNamedQuery": CreateNamedQuery,
    "athena-control:BatchGetNamedQuery": BatchGetNamedQuery,
    "athena-control:CreatePreparedStatement": CreatePreparedStatement,
    "athena-control:CreateDataCatalog": CreateDataCatalog,
    "athena-control:ListEngineVersions": ListEngineVersions,
    "athena-control:DeleteDataCatalog": DeleteDataCatalog,
    "athena-control:DeleteWorkGroup": DeleteWorkGroup,
    "athena-engine:CreateDatabaseStatement": CreateDatabaseStatement,
    "athena-engine:CreateExternalTableStatement": CreateExternalTableStatement,
    "athena-engine:SelectFromTable": SelectFromTable,
    "athena-engine:GetQueryRuntimeStatistics": GetQueryRuntimeStatistics,
    "athena-s3tables:CreateTableStatement": CreateTableStatement,
    "athena-s3tables:InsertStatement": InsertStatement,
    "athena-s3tables:SelectFromTable": SelectTableBucketRows,
}

SETUP = {
    "athena-engine": lambda ctx: _engine_setup(ctx),
    "athena-s3tables": lambda ctx: _tables_setup(ctx),
}
TEARDOWN = {
    "athena-control": lambda ctx: _teardown(ctx),
    "athena-engine": lambda ctx: _engine_teardown(ctx),
    "athena-s3tables": lambda ctx: _tables_teardown(ctx),
}


def _teardown(ctx: TestContext) -> None:
    a = _athena(ctx)
    try:
        a.delete_data_catalog(Name=_catalog(ctx))
    except Exception:
        pass
    # RecursiveDeleteOption removes the named query and prepared statement with it.
    try:
        a.delete_work_group(WorkGroup=_wg(ctx), RecursiveDeleteOption=True)
    except Exception:
        pass
