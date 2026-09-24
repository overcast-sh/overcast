"""
groups/kinesis.py — Kinesis compatibility test implementations.
"""

from __future__ import annotations
import base64
import time
from lib.harness import TestContext
from lib.clients import make_clients


def _kin(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region).kinesis


def _wait_stream_active(kin, stream_name: str, max_wait: int = 10) -> None:
    deadline = time.time() + max_wait
    while time.time() < deadline:
        resp = kin.describe_stream_summary(StreamName=stream_name)
        status = resp["StreamDescriptionSummary"]["StreamStatus"]
        if status == "ACTIVE":
            return
        time.sleep(0.2)
    raise TimeoutError(f"Stream {stream_name!r} did not become ACTIVE within {max_wait}s")


# ── kinesis-records ───────────────────────────────────────────────────────────

def setup_kinesis_records(ctx: TestContext) -> None:
    kin = _kin(ctx)
    name = f"oc-{ctx.run_id}-rec"
    kin.create_stream(StreamName=name, ShardCount=1)
    _wait_stream_active(kin, name)
    ctx["kinesis_rec_stream"] = name


def teardown_kinesis_records(ctx: TestContext) -> None:
    kin = _kin(ctx)
    name = ctx.get("kinesis_rec_stream")
    if not name:
        return
    try:
        kin.delete_stream(StreamName=name, EnforceConsumerDeletion=True)
    except Exception:
        pass


def PutRecord(ctx: TestContext) -> None:
    kin = _kin(ctx)
    name = ctx["kinesis_rec_stream"]
    resp = kin.put_record(
        StreamName=name,
        Data=b"hello-record",
        PartitionKey="pk-1",
    )
    if not resp.get("ShardId"):
        raise AssertionError(f"PutRecord: missing ShardId in {resp}")
    ctx["kinesis_shard_id"] = resp["ShardId"]


def PutRecords(ctx: TestContext) -> None:
    kin = _kin(ctx)
    name = ctx["kinesis_rec_stream"]
    records = [
        {"Data": f"record-{i}".encode(), "PartitionKey": f"pk-{i}"}
        for i in range(5)
    ]
    resp = kin.put_records(StreamName=name, Records=records)
    if resp.get("FailedRecordCount", 0) > 0:
        raise AssertionError(f"PutRecords: {resp['FailedRecordCount']} failed records")


def GetShardIterator(ctx: TestContext) -> None:
    kin = _kin(ctx)
    name = ctx["kinesis_rec_stream"]
    shard_id = ctx.get("kinesis_shard_id") or "shardId-000000000000"
    resp = kin.get_shard_iterator(
        StreamName=name,
        ShardId=shard_id,
        ShardIteratorType="TRIM_HORIZON",
    )
    it = resp.get("ShardIterator")
    if not it:
        raise AssertionError("GetShardIterator: missing ShardIterator")
    ctx["kinesis_shard_iter"] = it


def GetRecords(ctx: TestContext) -> None:
    kin = _kin(ctx)
    it = ctx.get("kinesis_shard_iter")
    if not it:
        raise AssertionError("GetRecords: no shard iterator in context (run GetShardIterator first)")
    resp = kin.get_records(ShardIterator=it, Limit=100)
    records = resp.get("Records", [])
    if not records:
        raise AssertionError("GetRecords: no records returned (expected ≥1 from PutRecord/PutRecords)")
    # Verify data is readable
    _ = base64.b64decode(records[0]["Data"]) if isinstance(records[0]["Data"], str) else records[0]["Data"]


# ── ImplMap ───────────────────────────────────────────────────────────────────

IMPLS = {
    "kinesis-records:PutRecord": PutRecord,
    "kinesis-records:PutRecords": PutRecords,
    "kinesis-records:GetShardIterator": GetShardIterator,
    "kinesis-records:GetRecords": GetRecords,
}

SETUP = {
    "kinesis-records": setup_kinesis_records,
}

TEARDOWN = {
    "kinesis-records": teardown_kinesis_records,
}
