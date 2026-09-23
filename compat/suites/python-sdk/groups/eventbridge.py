"""
groups/eventbridge.py — EventBridge compatibility test implementations.

Note: boto3 uses the service name "events" for EventBridge.
"""

from __future__ import annotations
import json
import time
from lib.harness import TestContext
from lib.clients import make_clients


def _eb(ctx: TestContext):
    # boto3 uses "events" as the service identifier for EventBridge
    return make_clients(ctx.endpoint, ctx.region).eventbridge


# ── eventbridge-buses ─────────────────────────────────────────────────────────

def setup_eventbridge_buses(ctx: TestContext) -> None:
    eb = _eb(ctx)
    name = f"oc-{ctx.run_id}-bus"
    resp = eb.create_event_bus(Name=name)
    ctx["eb_bus_name"] = name
    ctx["eb_bus_arn"] = resp.get("EventBusArn", "")


def teardown_eventbridge_buses(ctx: TestContext) -> None:
    eb = _eb(ctx)
    name = ctx.get("eb_bus_name")
    if not name:
        return
    try:
        eb.delete_event_bus(Name=name)
    except Exception:
        pass


def CreateEventBus(ctx: TestContext) -> None:
    eb = _eb(ctx)
    name = f"oc-{ctx.run_id}-bus-create"
    resp = eb.create_event_bus(Name=name)
    if not resp.get("EventBusArn"):
        raise AssertionError(f"CreateEventBus: missing EventBusArn in {resp}")
    # Clean up
    try:
        eb.delete_event_bus(Name=name)
    except Exception:
        pass


def DescribeEventBus(ctx: TestContext) -> None:
    eb = _eb(ctx)
    name = ctx["eb_bus_name"]
    resp = eb.describe_event_bus(Name=name)
    if resp["Name"] != name:
        raise AssertionError(f"DescribeEventBus: name mismatch {resp['Name']!r}")
    if not resp.get("Arn"):
        raise AssertionError("DescribeEventBus: missing Arn")


def ListEventBuses(ctx: TestContext) -> None:
    eb = _eb(ctx)
    name = ctx["eb_bus_name"]
    resp = eb.list_event_buses()
    buses = resp.get("EventBuses", [])
    if not any(b["Name"] == name for b in buses):
        raise AssertionError(f"ListEventBuses: {name!r} not found in {[b['Name'] for b in buses]}")


def TagEventBus(ctx: TestContext) -> None:
    eb = _eb(ctx)
    arn = ctx["eb_bus_arn"]
    eb.tag_resource(ResourceARN=arn, Tags=[{"Key": "env", "Value": "compat"}])
    resp = eb.list_tags_for_resource(ResourceARN=arn)
    tags = {t["Key"]: t["Value"] for t in resp.get("Tags", [])}
    if not (tags.get("env") == "compat"):
        raise AssertionError(f"TagEventBus: env tag not found, got {tags}")


def ListTagsForResource(ctx: TestContext) -> None:
    eb = _eb(ctx)
    arn = ctx["eb_bus_arn"]
    resp = eb.list_tags_for_resource(ResourceARN=arn)
    tags = {t["Key"]: t["Value"] for t in resp.get("Tags", [])}
    if tags.get("env") != "compat":
        raise AssertionError(f"ListTagsForResource: expected env=compat, got {tags}")


def DeleteEventBus(ctx: TestContext) -> None:
    eb = _eb(ctx)
    name = f"oc-{ctx.run_id}-bus-del"
    eb.create_event_bus(Name=name)
    eb.delete_event_bus(Name=name)
    resp = eb.list_event_buses(NamePrefix=name)
    names = [b["Name"] for b in resp.get("EventBuses", [])]
    if not (name not in names):
        raise AssertionError(f"DeleteEventBus: bus {name} still present")


# ── eventbridge-events ────────────────────────────────────────────────────────

def setup_eventbridge_events(ctx: TestContext) -> None:
    eb = _eb(ctx)
    name = f"oc-{ctx.run_id}-evtbus"
    eb.create_event_bus(Name=name)
    ctx["eb_evt_bus"] = name


def teardown_eventbridge_events(ctx: TestContext) -> None:
    eb = _eb(ctx)
    name = ctx.get("eb_evt_bus")
    if not name:
        return
    try:
        eb.delete_event_bus(Name=name)
    except Exception:
        pass


def PutEvents(ctx: TestContext) -> None:
    eb = _eb(ctx)
    bus_name = ctx["eb_evt_bus"]
    resp = eb.put_events(
        Entries=[{
            "Source": "com.example.overcast",
            "DetailType": "TestEvent",
            "Detail": json.dumps({"key": "value"}),
            "EventBusName": bus_name,
        }]
    )
    if resp.get("FailedEntryCount", 0) > 0:
        raise AssertionError(f"PutEvents: {resp['FailedEntryCount']} failed entries {resp.get('Entries')}")


def PutEventsBatch(ctx: TestContext) -> None:
    eb = _eb(ctx)
    bus_name = ctx["eb_evt_bus"]
    entries = [
        {
            "Source": "com.example.overcast",
            "DetailType": f"BatchEvent{i}",
            "Detail": json.dumps({"index": i}),
            "EventBusName": bus_name,
        }
        for i in range(5)
    ]
    resp = eb.put_events(Entries=entries)
    if resp.get("FailedEntryCount", 0) > 0:
        raise AssertionError(f"PutEventsBatch: {resp['FailedEntryCount']} failed entries {resp.get('Entries')}")


# ── eventbridge-target-fanout ─────────────────────────────────────────────────
#
# Target fan-out: an event put on a bus reaches the rule's targets, with the
# target's input transformation applied first. The group provisions its own
# queues and rule so it never races the other EventBridge groups (issue #388).


def _fanout_source(ctx: TestContext) -> str:
    return f"oc.fanout.{ctx.run_id}"


def _fanout_rule(ctx: TestContext) -> str:
    return f"oc-{ctx.run_id}-fanout"


def setup_eventbridge_target_fanout(ctx: TestContext) -> None:
    eb = _eb(ctx)
    sqs = make_clients(ctx.endpoint, ctx.region).sqs
    for key, name in (
        ("eb_fanout_plain", f"oc-{ctx.run_id}-fanout-plain"),
        ("eb_fanout_shaped", f"oc-{ctx.run_id}-fanout-shaped"),
    ):
        url = sqs.create_queue(QueueName=name)["QueueUrl"]
        attrs = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["QueueArn"])
        ctx[key + "_url"] = url
        ctx[key + "_arn"] = attrs["Attributes"]["QueueArn"]

    eb.put_rule(
        Name=_fanout_rule(ctx),
        EventPattern=json.dumps({"source": [_fanout_source(ctx)]}),
        State="ENABLED",
    )


def teardown_eventbridge_target_fanout(ctx: TestContext) -> None:
    eb = _eb(ctx)
    sqs = make_clients(ctx.endpoint, ctx.region).sqs
    try:
        eb.remove_targets(Rule=_fanout_rule(ctx), Ids=["plain", "shaped"])
    except Exception:
        pass
    try:
        eb.delete_rule(Name=_fanout_rule(ctx))
    except Exception:
        pass
    for key in ("eb_fanout_plain_url", "eb_fanout_shaped_url"):
        url = ctx.get(key)
        if not url:
            continue
        try:
            sqs.delete_queue(QueueUrl=url)
        except Exception:
            pass


def PutFanoutTargets(ctx: TestContext) -> None:
    eb = _eb(ctx)
    rule = _fanout_rule(ctx)
    resp = eb.put_targets(
        Rule=rule,
        Targets=[
            {"Id": "plain", "Arn": ctx["eb_fanout_plain_arn"]},
            {
                "Id": "shaped",
                "Arn": ctx["eb_fanout_shaped_arn"],
                "InputTransformer": {
                    "InputPathsMap": {"order": "$.detail.orderId"},
                    "InputTemplate": '{"order":"<order>"}',
                },
            },
        ],
    )
    if resp.get("FailedEntryCount", 0) > 0:
        raise AssertionError(f"PutFanoutTargets: {resp['FailedEntryCount']} failed entries {resp.get('FailedEntries')}")

    targets = eb.list_targets_by_rule(Rule=rule).get("Targets", [])
    if len(targets) != 2:
        raise AssertionError(f"PutFanoutTargets: rule has {len(targets)} targets, want 2")
    shaped = next((t for t in targets if t["Id"] == "shaped"), None)
    if not shaped or not shaped.get("InputTransformer", {}).get("InputTemplate"):
        raise AssertionError("PutFanoutTargets: InputTransformer did not round-trip")


def PutEventsToQueueTarget(ctx: TestContext) -> None:
    _put_fanout_event(ctx, "queue-target")
    body = _await_fanout_message(ctx, ctx["eb_fanout_plain_url"], "queue-target")
    if _fanout_source(ctx) not in body or "queue-target" not in body:
        raise AssertionError(f"PutEventsToQueueTarget: delivered body missing the event envelope: {body}")


def PutEventsWithInputTransformer(ctx: TestContext) -> None:
    _put_fanout_event(ctx, "transformed")
    body = _await_fanout_message(ctx, ctx["eb_fanout_shaped_url"], "transformed")
    if body != '{"order":"transformed"}':
        raise AssertionError(f"PutEventsWithInputTransformer: delivered body = {body}, want the rendered template")


def _put_fanout_event(ctx: TestContext, order_id: str) -> None:
    resp = _eb(ctx).put_events(
        Entries=[
            {
                "Source": _fanout_source(ctx),
                "DetailType": "FanoutTest",
                "Detail": json.dumps({"orderId": order_id}),
            }
        ]
    )
    if resp.get("FailedEntryCount", 0) > 0:
        raise AssertionError(f"PutEvents: {resp['FailedEntryCount']} failed entries")


def _await_fanout_message(ctx: TestContext, queue_url: str, want: str) -> str:
    """Poll a target queue for a delivered message containing ``want``.

    Both targets hang off one rule, so every event reaches both queues and a
    queue may hold an earlier test's message; matching on ``want`` (and
    consuming what does not match) keeps the tests order-independent.
    Delivery is asynchronous, so a bounded poll replaces a fixed sleep.
    """
    sqs = make_clients(ctx.endpoint, ctx.region).sqs
    for _ in range(15):
        resp = sqs.receive_message(QueueUrl=queue_url, MaxNumberOfMessages=10, WaitTimeSeconds=1)
        matched = None
        for message in resp.get("Messages", []):
            try:
                sqs.delete_message(QueueUrl=queue_url, ReceiptHandle=message["ReceiptHandle"])
            except Exception:
                pass
            if want in message["Body"]:
                matched = message["Body"]
        if matched is not None:
            return matched
        time.sleep(0.1)
    raise AssertionError(f"no message containing {want!r} delivered to the target queue")


# ── eventbridge-patterns ──────────────────────────────────────────────────────
#
# Stateless: TestEventPattern evaluates a pattern against an event without any
# bus, rule, or target involved, so this group needs no setup/teardown.


def _pattern_test_event(ctx: TestContext) -> str:
    return json.dumps({
        "id": ctx.run_id,
        "detail-type": "order.created",
        "source": "compat.eventbridge-patterns",
        "account": "000000000000",
        "time": "2026-01-01T00:00:00Z",
        "region": ctx.region,
        "resources": [],
        "detail": {"orderId": "1"},
    })


def TestEventPattern(ctx: TestContext) -> None:
    eb = _eb(ctx)
    pattern = json.dumps({
        "source": ["compat.eventbridge-patterns"],
        "detail-type": ["order.created"],
    })
    resp = eb.test_event_pattern(EventPattern=pattern, Event=_pattern_test_event(ctx))
    if resp.get("Result") is not True:
        raise AssertionError(f"TestEventPattern: expected Result=True, got {resp.get('Result')!r}")


def TestEventPatternNoMatch(ctx: TestContext) -> None:
    eb = _eb(ctx)
    pattern = json.dumps({"source": ["compat.eventbridge-patterns.other"]})
    resp = eb.test_event_pattern(EventPattern=pattern, Event=_pattern_test_event(ctx))
    if "Result" not in resp:
        raise AssertionError(f"TestEventPatternNoMatch: missing Result in {resp}")
    if resp["Result"] is not False:
        raise AssertionError(f"TestEventPatternNoMatch: expected Result=False, got {resp['Result']!r}")


# ── ImplMap ───────────────────────────────────────────────────────────────────

IMPLS = {
    "eventbridge-buses:CreateEventBus": CreateEventBus,
    "eventbridge-buses:DescribeEventBus": DescribeEventBus,
    "eventbridge-buses:ListEventBuses": ListEventBuses,
    "eventbridge-buses:TagEventBus": TagEventBus,
    "eventbridge-buses:ListEventBridgeTagsForResource": ListTagsForResource,
    "eventbridge-buses:DeleteEventBus": DeleteEventBus,
    "eventbridge-events:PutEvents": PutEvents,
    "eventbridge-events:PutEventsBatch": PutEventsBatch,
    "eventbridge-target-fanout:PutFanoutTargets": PutFanoutTargets,
    "eventbridge-target-fanout:PutEventsToQueueTarget": PutEventsToQueueTarget,
    "eventbridge-target-fanout:PutEventsWithInputTransformer": PutEventsWithInputTransformer,
    "eventbridge-patterns:TestEventPattern": TestEventPattern,
    "eventbridge-patterns:TestEventPatternNoMatch": TestEventPatternNoMatch,
}

SETUP = {
    "eventbridge-buses": setup_eventbridge_buses,
    "eventbridge-events": setup_eventbridge_events,
    "eventbridge-target-fanout": setup_eventbridge_target_fanout,
}

TEARDOWN = {
    "eventbridge-buses": teardown_eventbridge_buses,
    "eventbridge-events": teardown_eventbridge_events,
    "eventbridge-target-fanout": teardown_eventbridge_target_fanout,
}
