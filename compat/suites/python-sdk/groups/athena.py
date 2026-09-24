"""
groups/athena.py — Athena control-plane compatibility test implementations for the Python suite.

athena-control drives a workgroup with a result location, a query run in it,
a named query, a prepared statement and a data catalog.
"""

from __future__ import annotations

from botocore.exceptions import ClientError

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
    if qe.get("WorkGroup") != _wg(ctx) or location != _RESULTS or not qe.get("Status", {}).get("State"):
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
}

SETUP = {}
TEARDOWN = {
    "athena-control": lambda ctx: _teardown(ctx),
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
