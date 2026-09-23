"""
groups/cloudwatch_logs.py — CloudWatch Logs compatibility test implementations.

logs-groups is not here: it is a ported group, resolved from
compat/model/authored/logs-groups.json by the scenario backend (#1116).
"""

from __future__ import annotations
import time
from lib.harness import TestContext
from lib.clients import make_clients


def _logs(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region).logs


# ── logs-events ───────────────────────────────────────────────────────────────

def setup_logs_events(ctx: TestContext) -> None:
    logs = _logs(ctx)
    name = f"/compat/{ctx.run_id}-events"
    stream = "mystream"
    logs.create_log_group(logGroupName=name)
    logs.create_log_stream(logGroupName=name, logStreamName=stream)
    ctx["log_events_group"] = name
    ctx["log_events_stream"] = stream


def teardown_logs_events(ctx: TestContext) -> None:
    name = ctx.get("log_events_group")
    if name:
        try:
            _logs(ctx).delete_log_group(logGroupName=name)
        except Exception:
            pass


def PutLogEvents(ctx: TestContext) -> None:
    logs = _logs(ctx)
    name = ctx["log_events_group"]
    stream = ctx["log_events_stream"]
    now_ms = int(time.time() * 1000)
    logs.put_log_events(
        logGroupName=name,
        logStreamName=stream,
        logEvents=[
            {"timestamp": now_ms, "message": "event one"},
            {"timestamp": now_ms + 1, "message": "event two"},
        ],
    )


def GetLogEvents(ctx: TestContext) -> None:
    logs = _logs(ctx)
    name = ctx["log_events_group"]
    stream = ctx["log_events_stream"]
    resp = logs.get_log_events(
        logGroupName=name,
        logStreamName=stream,
        startFromHead=True,
    )
    events = resp.get("events", [])
    if not events:
        raise AssertionError("GetLogEvents: no events returned")
    messages = [e["message"] for e in events]
    if "event one" not in messages:
        raise AssertionError(f"GetLogEvents: expected 'event one' in {messages}")


def FilterLogEvents(ctx: TestContext) -> None:
    logs = _logs(ctx)
    name = ctx["log_events_group"]
    resp = logs.filter_log_events(logGroupName=name, filterPattern="event two")
    events = resp.get("events", [])
    if not events:
        raise AssertionError("FilterLogEvents: no events returned matching 'event two'")


def DescribeLogStreams(ctx: TestContext) -> None:
    logs = _logs(ctx)
    name = ctx["log_events_group"]
    stream = ctx["log_events_stream"]
    resp = logs.describe_log_streams(logGroupName=name)
    streams = [s["logStreamName"] for s in resp.get("logStreams", [])]
    if stream not in streams:
        raise AssertionError(f"DescribeLogStreams: {stream!r} not found in {streams}")


def DeleteLogStream(ctx: TestContext) -> None:
    logs = _logs(ctx)
    name = ctx["log_events_group"]
    stream = ctx["log_events_stream"]
    logs.delete_log_stream(logGroupName=name, logStreamName=stream)
    resp = logs.describe_log_streams(logGroupName=name)
    streams = [s["logStreamName"] for s in resp.get("logStreams", [])]
    if stream in streams:
        raise AssertionError(f"DeleteLogStream: {stream!r} still listed")


# ── ImplMap ───────────────────────────────────────────────────────────────────

IMPLS = {
    "logs-events:PutLogEvents": PutLogEvents,
    "logs-events:GetLogEvents": GetLogEvents,
    "logs-events:FilterLogEvents": FilterLogEvents,
    "logs-events:DescribeLogStreams": DescribeLogStreams,
    "logs-events:DeleteLogStream": DeleteLogStream,
}

SETUP = {
    "logs-events": setup_logs_events,
}

TEARDOWN = {
    "logs-events": teardown_logs_events,
}
