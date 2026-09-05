"""
Unit tests for the scenario interpreter (lib/scenario).

These are not compat tests: no emulator, no network, no boto3 client. Every
call goes to an in-memory fake whose only job is to record the params it was
handed and return a canned response, which is what lets these tests pin the
things a real run cannot show cheaply — that a `readback`'s exports are held
back until its checks pass, that `eventually` reports the *last* attempt's
failure, that a 501 stays an `unimplemented` rather than becoming a `fail`, and
that every failure message carries all six required fields.

Run with:  python -m unittest discover -s tests  (from compat/suites/python-sdk/)
"""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from botocore.exceptions import ClientError  # noqa: E402

from lib.harness import TestContext, TestGroup, run_group  # noqa: E402
from lib.harness import TestCase as HarnessTestCase  # noqa: E402
from lib.scenario import ScenarioInterpreter, scenario_hooks  # noqa: E402
from lib.scenario.executor import ClientCache, error_matches, error_names  # noqa: E402
from lib.scenario.expressions import (  # noqa: E402
    evaluate,
    is_non_empty,
    json_equal,
    parse_path,
    resolve_path,
)
from lib.scenario.failures import (  # noqa: E402
    MISSING,
    ScenarioError,
    ScenarioFailure,
    failure_message,
)
from lib.scenario.loader import ScenarioGroup, ScenarioLibrary  # noqa: E402

CLIENT = {
    "sdkId": "SQS",
    "endpointPrefix": "sqs",
    "signingName": "sqs",
    "protocol": "awsJson1_0",
    "apiVersion": "2012-11-05",
    "targetPrefix": "AmazonSQS",
}
SCENARIO_FILE = "compat/model/scenarios/sqs.json"


# ─── Fakes ────────────────────────────────────────────────────────────────────


class FakeClient:
    """A boto3-shaped client: one snake_case method per operation, each
    returning the next scripted answer for that operation."""

    def __init__(self, script: dict[str, list]) -> None:
        # op-method name → answers, consumed in order; the last one repeats, so
        # a poll loop does not need its budget spelled out.
        self.script = {k: list(v) for k, v in script.items()}
        self.calls: list[tuple[str, dict]] = []

    def __getattr__(self, method: str):
        def call(**params):
            self.calls.append((method, params))
            answers = self.script.get(method)
            if not answers:
                raise AssertionError(f"fake client has no answer for {method}")
            answer = answers.pop(0) if len(answers) > 1 else answers[0]
            if isinstance(answer, Exception):
                raise answer
            return answer

        return call

    def ops(self) -> list[str]:
        return [name for name, _ in self.calls]

    def params_for(self, method: str) -> list[dict]:
        return [p for name, p in self.calls if name == method]


def client_error(code: str, *, status: int = 400, op: str = "Op",
                 headers: dict | None = None, wire_type: str | None = None
                 ) -> ClientError:
    response = {
        "Error": {"Code": code, "Message": f"{code} raised by the fake"},
        "ResponseMetadata": {"HTTPStatusCode": status, "HTTPHeaders": headers or {}},
    }
    if wire_type is not None:
        response["Error"]["__type"] = wire_type
    return ClientError(response, op)


def make_group(name="sqs-gen-queue", *, setup=(), tests=(), teardown=()) -> ScenarioGroup:
    return ScenarioGroup(
        file=SCENARIO_FILE, service="sqs", client=CLIENT, name=name, kind="lifecycle",
        setup=list(setup), tests={t["name"]: t for t in tests}, teardown=list(teardown),
    )


def make_interpreter(client: FakeClient) -> ScenarioInterpreter:
    return ScenarioInterpreter(clients=ClientCache(lambda e, r, s: client))


def make_ctx(run_id: str = "oc-run") -> TestContext:
    return TestContext(endpoint="http://127.0.0.1:1", region="us-east-1", run_id=run_id)


def run_one(spec: ScenarioGroup, test: str, client: FakeClient,
            ctx: TestContext | None = None) -> TestContext:
    """Setup, then one test, sharing a context — the shape a group runs in."""
    interp = make_interpreter(client)
    ctx = ctx if ctx is not None else make_ctx()
    interp.run_setup(spec, ctx)
    interp.run_test(spec, test, ctx)
    return ctx


def bag(ctx: TestContext) -> dict:
    return ctx["__scenario_context__"]


def harness_group(spec: ScenarioGroup, tests: list[str], client: FakeClient) -> TestGroup:
    """The group as the harness sees it: scenario setup, scenario teardown and
    one TestCase per named test."""
    interp = make_interpreter(client)
    return TestGroup(
        suite="python-sdk", service=spec.service, name=spec.name,
        tests=[HarnessTestCase(name=name, fn=interp.test_fn(spec, name)) for name in tests],
        setup=interp.setup_fn(spec), teardown=interp.teardown_fn(spec),
    )


def harness_results(spec: ScenarioGroup, tests: list[str], client: FakeClient) -> list[dict]:
    group = harness_group(spec, tests, client)
    events = _capture(lambda: _quiet(lambda: run_group(group, make_ctx())))
    return [e for e in events if e["event"] == "test_result"]


def _harness_status(spec: ScenarioGroup, test: str, client: FakeClient) -> str:
    return harness_results(spec, [test], client)[0]["status"]


# ─── Value expressions ────────────────────────────────────────────────────────


class TestExpressions(unittest.TestCase):
    def ev(self, value, context=None, run_id="oc-run", group="sqs-gen-queue"):
        return evaluate(value, context=context or {}, run_id=run_id, group=group)

    def test_scalars_and_structure_pass_through(self):
        self.assertEqual(self.ev("plain"), "plain")
        self.assertEqual(self.ev(30), 30)
        self.assertEqual(self.ev(False), False)
        self.assertIsNone(self.ev(None))
        self.assertEqual(self.ev({"A": [1, {"B": "c"}]}), {"A": [1, {"B": "c"}]})

    def test_lit_is_verbatim_and_uninterpreted(self):
        self.assertEqual(self.ev({"$lit": {"$ref": "queue.url"}}), {"$ref": "queue.url"})

    def test_ref_resolves_from_the_context_bag(self):
        self.assertEqual(self.ev({"$ref": "queue.url"}, {"queue.url": "u"}), "u")

    def test_unresolvable_ref_names_the_path_and_the_bag(self):
        with self.assertRaises(ScenarioError) as raised:
            self.ev({"$ref": "queue.arn"}, {"queue.url": "u"})
        self.assertIn("queue.arn", str(raised.exception))
        self.assertIn("queue.url", str(raised.exception))

    def test_name_is_runid_group_suffix_unshortened(self):
        self.assertEqual(
            self.ev({"$name": "dlq"}, run_id="oc-abc123", group="sqs-gen-message"),
            "oc-abc123-sqs-gen-message-dlq",
        )

    def test_concat_joins_literals_and_expressions(self):
        value = self.ev(
            {"$concat": ['{"arn":"', {"$ref": "dlq.arn"}, '"}']},
            {"dlq.arn": "arn:aws:sqs:::q"},
        )
        self.assertEqual(value, '{"arn":"arn:aws:sqs:::q"}')

    def test_concat_refuses_a_non_string_part(self):
        with self.assertRaises(ScenarioError):
            self.ev({"$concat": [{"$ref": "n"}]}, {"n": 7})

    def test_index_selects_an_element(self):
        self.assertEqual(self.ev({"$index": [{"$ref": "ids"}, 1]}, {"ids": ["a", "b"]}), "b")

    def test_index_out_of_range_is_an_error(self):
        with self.assertRaises(ScenarioError):
            self.ev({"$index": [{"$ref": "ids"}, 2]}, {"ids": ["a", "b"]})

    def test_expression_key_mixed_with_members_is_refused(self):
        with self.assertRaises(ScenarioError):
            self.ev({"$ref": "a", "Other": 1}, {"a": 1})

    def test_unknown_expression_is_refused(self):
        with self.assertRaises(ScenarioError):
            self.ev({"$upper": "a"})

    def test_expressions_nest_inside_structures(self):
        value = self.ev(
            {"Attributes": {"Redrive": {"$ref": "a"}}, "Entries": [{"Id": {"$name": "e"}}]},
            {"a": "x"},
        )
        self.assertEqual(value, {"Attributes": {"Redrive": "x"},
                                 "Entries": [{"Id": "oc-run-sqs-gen-queue-e"}]})


# ─── Paths ────────────────────────────────────────────────────────────────────


class TestPaths(unittest.TestCase):
    RESPONSE = {
        "QueueUrl": "http://q",
        "Attributes": {"QueueArn": "arn", "aws:tag/x": "y"},
        "Messages": [{"MessageId": "m1"}, {"MessageId": "m2"}],
        "Empty": [],
        "Null": None,
    }

    def test_parse(self):
        self.assertEqual(parse_path("$"), [])
        self.assertEqual(parse_path("$.Messages[0].MessageId"), ["Messages", 0, "MessageId"])
        self.assertEqual(parse_path("$.Attributes.aws:tag/x"), ["Attributes", "aws:tag/x"])

    def test_malformed_paths(self):
        for path in ("Messages", "$.", "$[a]", "$.Messages[01]"):
            with self.assertRaises(ScenarioError, msg=path):
                parse_path(path)

    def test_resolve(self):
        self.assertIs(resolve_path(self.RESPONSE, "$"), self.RESPONSE)
        self.assertEqual(resolve_path(self.RESPONSE, "$.QueueUrl"), "http://q")
        self.assertEqual(resolve_path(self.RESPONSE, "$.Attributes.QueueArn"), "arn")
        self.assertEqual(resolve_path(self.RESPONSE, "$.Messages[1].MessageId"), "m2")
        self.assertEqual(resolve_path(self.RESPONSE, "$.Empty"), [])
        self.assertIsNone(resolve_path(self.RESPONSE, "$.Null"))

    def test_missing_segments(self):
        for path in ("$.Nope", "$.Attributes.Nope", "$.Messages[2]", "$.Empty[0]",
                     "$.QueueUrl[0]", "$.QueueUrl.Nope", "$.Null.Nope"):
            self.assertIs(resolve_path(self.RESPONSE, path), MISSING, msg=path)


# ─── Comparison primitives ────────────────────────────────────────────────────


class TestComparison(unittest.TestCase):
    def test_numbers_compare_across_python_types(self):
        self.assertTrue(json_equal(1, 1.0))

    def test_booleans_are_never_numbers(self):
        self.assertFalse(json_equal(True, 1))
        self.assertFalse(json_equal(0, False))
        self.assertTrue(json_equal(True, True))

    def test_strings_are_never_numbers(self):
        self.assertFalse(json_equal("30", 30))

    def test_containers_recurse(self):
        self.assertTrue(json_equal({"a": [1, {"b": "c"}]}, {"a": [1, {"b": "c"}]}))
        self.assertFalse(json_equal({"a": 1}, {"a": 1, "b": 2}))
        self.assertFalse(json_equal([1, 2], [2, 1]))

    def test_non_empty(self):
        for value in (0, False, "x", [1], {"a": 1}, 0.0):
            self.assertTrue(is_non_empty(value), msg=repr(value))
        for value in (None, "", [], {}, MISSING):
            self.assertFalse(is_non_empty(value), msg=repr(value))


# ─── Error naming ─────────────────────────────────────────────────────────────


class TestErrorNames(unittest.TestCase):
    ERROR = {"shape": "QueueDoesNotExist",
             "code": "AWS.SimpleQueueService.NonExistentQueue"}

    def test_the_wire_code_matches(self):
        exc = client_error("AWS.SimpleQueueService.NonExistentQueue")
        self.assertTrue(error_matches(exc, self.ERROR))

    def test_the_shape_name_matches(self):
        self.assertTrue(error_matches(client_error("QueueDoesNotExist"), self.ERROR))

    def test_a_json_protocol_type_uri_is_stripped(self):
        exc = client_error("Other", wire_type="com.amazonaws.sqs#QueueDoesNotExist")
        self.assertIn("QueueDoesNotExist", error_names(exc))
        self.assertTrue(error_matches(exc, self.ERROR))

    def test_the_query_error_header_matches(self):
        exc = client_error(
            "Other",
            headers={"x-amzn-query-error": "AWS.SimpleQueueService.NonExistentQueue;Sender"},
        )
        self.assertTrue(error_matches(exc, self.ERROR))

    def test_an_unrelated_error_does_not_match(self):
        self.assertFalse(error_matches(client_error("AccessDenied"), self.ERROR))


# ─── Assertion kinds ──────────────────────────────────────────────────────────


class TestAssertions(unittest.TestCase):
    def test_response_field_checks(self):
        spec = make_group(tests=[{
            "name": "CreateQueue", "op": "CreateQueue",
            "call": {"op": "CreateQueue", "params": {"QueueName": {"$name": "q"}}},
            "assert": [{"kind": "responseField", "checks": {
                "$.QueueUrl": {"nonEmpty": True},
                "$.Attributes.VisibilityTimeout": {"equals": "30"},
                "$.Attributes.QueueArn": {"matches": "^arn:aws:sqs:"},
                "$.QueueUrls": {"isList": True},
                "$.Nope": {"missing": True},
            }}],
        }])
        client = FakeClient({"create_queue": [{
            "QueueUrl": "http://q",
            "Attributes": {"VisibilityTimeout": "30", "QueueArn": "arn:aws:sqs:::q"},
        }]})
        run_one(spec, "CreateQueue", client)
        self.assertEqual(client.params_for("create_queue"),
                         [{"QueueName": "oc-run-sqs-gen-queue-q"}])

    def test_is_list_accepts_an_empty_and_an_absent_page_but_not_a_scalar(self):
        def spec_for(check_path):
            return make_group(tests=[{
                "name": "ListQueues", "op": "ListQueues",
                "call": {"op": "ListQueues", "params": {}},
                "assert": [{"kind": "responseField",
                            "checks": {check_path: {"isList": True}}}],
            }])

        client = FakeClient({"list_queues": [{"QueueUrls": []}]})
        run_one(spec_for("$.QueueUrls"), "ListQueues", client)          # empty page
        run_one(spec_for("$.Absent"), "ListQueues", client)             # omitted page
        with self.assertRaises(ScenarioFailure):
            run_one(spec_for("$.QueueUrls"), "ListQueues",
                    FakeClient({"list_queues": [{"QueueUrls": "not-a-list"}]}))

    def test_non_empty_accepts_zero_and_false(self):
        spec = make_group(tests=[{
            "name": "CancelMessageMoveTask", "op": "CancelMessageMoveTask",
            "call": {"op": "CancelMessageMoveTask", "params": {"TaskHandle": "h"}},
            "assert": [{"kind": "responseField", "checks": {
                "$.ApproximateNumberOfMessagesMoved": {"nonEmpty": True}}}],
        }])
        run_one(spec, "CancelMessageMoveTask",
                FakeClient({"cancel_message_move_task":
                            [{"ApproximateNumberOfMessagesMoved": 0}]}))

    def test_readback_applies_its_export_only_when_the_checks_pass(self):
        spec = make_group(tests=[{
            "name": "CreateQueue", "op": "CreateQueue",
            "call": {"op": "CreateQueue", "params": {}, "export": {"queue.url": "$.QueueUrl"}},
            "assert": [{"kind": "readback",
                        "call": {"op": "GetQueueAttributes",
                                 "params": {"QueueUrl": {"$ref": "queue.url"}},
                                 "export": {"queue.arn": "$.Attributes.QueueArn"}},
                        "checks": {"$.Attributes.VisibilityTimeout": {"equals": "30"}}}],
        }])
        client = FakeClient({
            "create_queue": [{"QueueUrl": "http://q"}],
            "get_queue_attributes": [
                {"Attributes": {"QueueArn": "arn", "VisibilityTimeout": "1"}}],
        })
        ctx = make_ctx()
        with self.assertRaises(ScenarioFailure):
            run_one(spec, "CreateQueue", client, ctx)
        self.assertIn("queue.url", bag(ctx))    # the primary call's export stands
        self.assertNotIn("queue.arn", bag(ctx))  # the readback's does not

    def test_list_contains_needs_a_non_empty_list_holding_the_item(self):
        spec = make_group(tests=[{
            "name": "ListQueues", "op": "ListQueues",
            "call": {"op": "ListQueues", "params": {}},
            "assert": [{"kind": "listContains", "itemsPath": "$.QueueUrls",
                        "where": {"$": "http://q"}}],
        }])
        run_one(spec, "ListQueues",
                FakeClient({"list_queues": [{"QueueUrls": ["http://other", "http://q"]}]}))
        for response in ({"QueueUrls": []}, {}, {"QueueUrls": ["http://other"]}):
            with self.assertRaises(ScenarioFailure):
                run_one(spec, "ListQueues", FakeClient({"list_queues": [response]}))

    def test_list_contains_matches_every_where_entry_of_one_item(self):
        spec = make_group(tests=[{
            "name": "ListPolicies", "op": "ListPolicies",
            "call": {"op": "ListPolicies", "params": {}},
            "assert": [{"kind": "listContains", "itemsPath": "$.Policies",
                        "where": {"$.Id": "p-1", "$.Type": "SCP"}}],
        }])
        run_one(spec, "ListPolicies", FakeClient({"list_policies": [
            {"Policies": [{"Id": "p-2", "Type": "SCP"}, {"Id": "p-1", "Type": "SCP"}]}]}))
        with self.assertRaises(ScenarioFailure):
            # Both values are present, but never on the same item.
            run_one(spec, "ListPolicies", FakeClient({"list_policies": [
                {"Policies": [{"Id": "p-1", "Type": "TAG"}, {"Id": "p-2", "Type": "SCP"}]}]}))

    def test_absent_list_form_treats_a_missing_list_as_empty(self):
        spec = make_group(tests=[{
            "name": "DeleteMessage", "op": "DeleteMessage",
            "call": {"op": "DeleteMessage", "params": {}},
            "assert": [{"kind": "absent",
                        "call": {"op": "ReceiveMessage", "params": {}},
                        "itemsPath": "$.Messages", "where": {"$.MessageId": "m1"}}],
        }])
        run_one(spec, "DeleteMessage",
                FakeClient({"delete_message": [{}], "receive_message": [{}]}))
        with self.assertRaises(ScenarioFailure):
            run_one(spec, "DeleteMessage", FakeClient({
                "delete_message": [{}],
                "receive_message": [{"Messages": [{"MessageId": "m1"}]}]}))

    def test_absent_error_form(self):
        spec = make_group(tests=[{
            "name": "DeleteQueue", "op": "DeleteQueue",
            "call": {"op": "DeleteQueue", "params": {}},
            "assert": [{"kind": "absent",
                        "call": {"op": "GetQueueAttributes", "params": {}},
                        "error": {"shape": "QueueDoesNotExist",
                                  "code": "AWS.SimpleQueueService.NonExistentQueue"}}],
        }])
        run_one(spec, "DeleteQueue", FakeClient({
            "delete_queue": [{}],
            "get_queue_attributes": [client_error("QueueDoesNotExist")]}))
        with self.assertRaises(ScenarioFailure):  # the read-back succeeded
            run_one(spec, "DeleteQueue", FakeClient({
                "delete_queue": [{}], "get_queue_attributes": [{"Attributes": {}}]}))
        with self.assertRaises(ScenarioFailure):  # a different error
            run_one(spec, "DeleteQueue", FakeClient({
                "delete_queue": [{}],
                "get_queue_attributes": [client_error("AccessDenied")]}))

    def test_error_code_expects_the_primary_call_to_fail(self):
        spec = make_group(tests=[{
            "name": "GetQueueUrl", "op": "GetQueueUrl",
            "call": {"op": "GetQueueUrl", "params": {"QueueName": "nope"}},
            "assert": [{"kind": "errorCode",
                        "error": {"shape": "QueueDoesNotExist",
                                  "code": "AWS.SimpleQueueService.NonExistentQueue"}}],
        }])
        run_one(spec, "GetQueueUrl",
                FakeClient({"get_queue_url": [client_error("QueueDoesNotExist")]}))
        with self.assertRaises(ScenarioFailure) as raised:
            run_one(spec, "GetQueueUrl", FakeClient({"get_queue_url": [{"QueueUrl": "u"}]}))
        self.assertIn("<no error>", str(raised.exception))


class TestEventually(unittest.TestCase):
    SPEC = make_group(tests=[{
        "name": "SetQueueAttributes", "op": "SetQueueAttributes",
        "call": {"op": "SetQueueAttributes", "params": {}},
        "assert": [{"kind": "eventually", "maxAttempts": 3, "delayMs": 0, "assert": {
            "kind": "readback",
            "call": {"op": "GetQueueAttributes", "params": {},
                     "export": {"queue.timeout": "$.Attributes.VisibilityTimeout"}},
            "checks": {"$.Attributes.VisibilityTimeout": {"equals": "60"}},
        }}],
    }])

    def test_passes_on_a_later_attempt_and_exports_only_then(self):
        client = FakeClient({
            "set_queue_attributes": [{}],
            "get_queue_attributes": [
                {"Attributes": {"VisibilityTimeout": "30"}},
                {"Attributes": {"VisibilityTimeout": "60"}},
            ],
        })
        ctx = run_one(self.SPEC, "SetQueueAttributes", client)
        self.assertEqual(len(client.params_for("get_queue_attributes")), 2)
        self.assertEqual(bag(ctx)["queue.timeout"], "60")

    def test_gives_up_reporting_the_last_attempts_failure(self):
        client = FakeClient({
            "set_queue_attributes": [{}],
            "get_queue_attributes": [{"Attributes": {"VisibilityTimeout": "30"}}],
        })
        ctx = make_ctx()
        with self.assertRaises(ScenarioFailure) as raised:
            run_one(self.SPEC, "SetQueueAttributes", client, ctx)
        message = str(raised.exception)
        self.assertEqual(len(client.params_for("get_queue_attributes")), 3)
        self.assertIn("gave up after 3 attempt(s)", message)
        self.assertIn('expected=equals "60"', message)
        self.assertIn('actual="30"', message)
        self.assertNotIn("queue.timeout", bag(ctx))

    def test_a_501_inside_stops_the_loop_at_once(self):
        client = FakeClient({
            "set_queue_attributes": [{}],
            "get_queue_attributes": [client_error("NotImplemented", status=501)],
        })
        with self.assertRaises(ClientError):
            run_one(self.SPEC, "SetQueueAttributes", client)
        self.assertEqual(len(client.params_for("get_queue_attributes")), 1)


# ─── Calls, exports, unimplemented ────────────────────────────────────────────


class TestCalls(unittest.TestCase):
    def test_a_501_is_re_raised_so_the_harness_records_unimplemented(self):
        spec = make_group(name="sqs-gen-probe", tests=[{
            "name": "ListMessageMoveTasks", "op": "ListMessageMoveTasks",
            "call": {"op": "ListMessageMoveTasks", "params": {"SourceArn": "arn"}},
            "assert": [{"kind": "responseField", "checks": {"$.Results": {"isList": True}}}],
        }])
        client = FakeClient(
            {"list_message_move_tasks": [client_error("NotImplemented", status=501)]})
        # Re-raised unchanged: the harness's own detection reads the 501 off the
        # botocore error, and a ScenarioFailure wrapping it would be recorded as
        # a `fail` instead — which is the whole point of the probe groups.
        with self.assertRaises(ClientError):
            run_one(spec, "ListMessageMoveTasks", client)
        status = _harness_status(spec, "ListMessageMoveTasks", client)
        self.assertEqual(status, "unimplemented")

    def test_an_export_path_the_response_lacks_fails_the_step(self):
        spec = make_group(tests=[{
            "name": "ReceiveMessage", "op": "ReceiveMessage",
            "call": {"op": "ReceiveMessage", "params": {},
                     "export": {"message.receiptHandle": "$.Messages[0].ReceiptHandle"}},
            "assert": [{"kind": "responseField", "checks": {"$.Messages": {"isList": True}}}],
        }])
        with self.assertRaises(ScenarioFailure) as raised:
            run_one(spec, "ReceiveMessage", FakeClient({"receive_message": [{}]}))
        self.assertIn("$.Messages[0].ReceiptHandle", str(raised.exception))
        self.assertIn("<missing>", str(raised.exception))

    def test_the_params_the_sdk_received_are_the_evaluated_ones(self):
        spec = make_group(setup=[{"op": "CreateQueue", "params": {"QueueName": {"$name": "dlq"}},
                                  "export": {"dlq.url": "$.QueueUrl"}}],
                          tests=[{
                              "name": "SendMessage", "op": "SendMessage",
                              "call": {"op": "SendMessage", "params": {
                                  "QueueUrl": {"$ref": "dlq.url"},
                                  "MessageBody": "body",
                                  "DelaySeconds": 0,
                                  "MessageAttributes": {"k": {"DataType": "String"}}}},
                              "assert": [{"kind": "responseField",
                                          "checks": {"$.MessageId": {"nonEmpty": True}}}],
                          }])
        client = FakeClient({"create_queue": [{"QueueUrl": "http://dlq"}],
                             "send_message": [{"MessageId": "m1"}]})
        run_one(spec, "SendMessage", client)
        self.assertEqual(client.params_for("send_message"), [{
            "QueueUrl": "http://dlq",
            "MessageBody": "body",
            "DelaySeconds": 0,
            "MessageAttributes": {"k": {"DataType": "String"}},
        }])


# ─── Setup and teardown ───────────────────────────────────────────────────────


class TestSetupAndTeardown(unittest.TestCase):
    SPEC = make_group(
        setup=[{"op": "CreateQueue", "params": {"QueueName": {"$name": "dlq"}},
                "export": {"dlq.url": "$.QueueUrl"}},
               {"op": "CreateQueue", "params": {"QueueName": {"$name": "q"}},
                "export": {"queue.url": "$.QueueUrl"}}],
        tests=[{"name": "GetQueueUrl", "op": "GetQueueUrl",
                "call": {"op": "GetQueueUrl", "params": {}},
                "assert": [{"kind": "responseField",
                            "checks": {"$.QueueUrl": {"nonEmpty": True}}}]}],
        teardown=[{"op": "DeleteQueue", "params": {"QueueUrl": {"$ref": "queue.url"}}},
                  {"op": "DeleteQueue", "params": {"QueueUrl": {"$ref": "gone.url"}}},
                  {"op": "DeleteQueue", "params": {"QueueUrl": {"$ref": "dlq.url"}}}],
    )

    def test_setup_runs_in_order_and_shares_its_exports(self):
        client = FakeClient({"create_queue": [{"QueueUrl": "http://dlq"},
                                              {"QueueUrl": "http://q"}]})
        ctx = make_ctx()
        make_interpreter(client).run_setup(self.SPEC, ctx)
        self.assertEqual(bag(ctx), {"dlq.url": "http://dlq", "queue.url": "http://q"})

    def test_a_setup_failure_skips_every_test_of_the_group(self):
        client = FakeClient({"create_queue": [client_error("AccessDenied")]})
        results = harness_results(self.SPEC, ["GetQueueUrl"], client)
        self.assertEqual([r["status"] for r in results], ["skip"])
        self.assertTrue(results[0]["error"].startswith("setup failed: "))
        self.assertIn("sqs-gen-queue/setup:", results[0]["error"])
        self.assertIn("setup[0]", results[0]["error"])

    def test_each_teardown_step_is_wrapped_individually(self):
        client = FakeClient({"create_queue": [{"QueueUrl": "http://dlq"},
                                              {"QueueUrl": "http://q"}],
                             "delete_queue": [client_error("AccessDenied"), {}]})
        interp = make_interpreter(client)
        ctx = make_ctx()
        interp.run_setup(self.SPEC, ctx)
        _quiet(lambda: interp.run_teardown(self.SPEC, ctx))  # must not raise
        # Three steps: the first errored, the second's $ref was unresolvable and
        # was skipped, the third still ran.
        self.assertEqual(client.params_for("delete_queue"),
                         [{"QueueUrl": "http://q"}, {"QueueUrl": "http://dlq"}])


def _capture(fn) -> list[dict]:
    """Run `fn` and parse the NDJSON it emitted to stdout."""
    import contextlib
    import io

    buffer = io.StringIO()
    with contextlib.redirect_stdout(buffer):
        fn()
    return [json.loads(line) for line in buffer.getvalue().splitlines() if line.strip()]


# ─── Failure messages ─────────────────────────────────────────────────────────


class TestFailureMessages(unittest.TestCase):
    def test_every_required_field_is_present_and_in_order(self):
        message = failure_message(
            group="sqs-gen-queue", test="SetQueueAttributes", op="GetQueueAttributes",
            params={"QueueUrl": "http://q"}, assertion="readback",
            path="$.Attributes.VisibilityTimeout", expected='equals "60"', actual='"30"',
            scenario_file=SCENARIO_FILE, step="assert[0].assert",
        )
        self.assertEqual(
            message,
            'sqs-gen-queue/SetQueueAttributes: op=GetQueueAttributes '
            'params={"QueueUrl": "http://q"} assertion=readback '
            'path=$.Attributes.VisibilityTimeout expected=equals "60" actual="30" '
            'at compat/model/scenarios/sqs.json assert[0].assert',
        )

    def test_an_assertion_failure_carries_the_same_six_fields(self):
        spec = make_group(tests=[{
            "name": "GetQueueAttributes", "op": "GetQueueAttributes",
            "call": {"op": "GetQueueAttributes",
                     "params": {"QueueUrl": "http://q", "AttributeNames": ["All"]}},
            "assert": [{"kind": "responseField",
                        "checks": {"$.Attributes.QueueArn": {"equals": "arn:wanted"}}}],
        }])
        with self.assertRaises(ScenarioFailure) as raised:
            run_one(spec, "GetQueueAttributes", FakeClient({
                "get_queue_attributes": [{"Attributes": {"QueueArn": "arn:got"}}]}))
        message = str(raised.exception)
        for fragment in (
            "sqs-gen-queue/GetQueueAttributes:",                     # 1 group/test
            "op=GetQueueAttributes",                                 # 2 operation
            'params={"AttributeNames": ["All"], "QueueUrl": "http://q"}',  # 3 params
            "assertion=responseField path=$.Attributes.QueueArn",    # 4 kind + path
            'expected=equals "arn:wanted" actual="arn:got"',         # 5 expected/actual
            f"at {SCENARIO_FILE} assert[0]",                         # 6 file + step
        ):
            self.assertIn(fragment, message)

    def test_an_unresolvable_ref_reports_the_unevaluated_params(self):
        spec = make_group(tests=[{
            "name": "DeleteQueue", "op": "DeleteQueue",
            "call": {"op": "DeleteQueue", "params": {"QueueUrl": {"$ref": "queue.url"}}},
            "assert": [{"kind": "responseField", "checks": {"$": {"nonEmpty": True}}}],
        }])
        with self.assertRaises(ScenarioFailure) as raised:
            run_one(spec, "DeleteQueue", FakeClient({"delete_queue": [{}]}))
        message = str(raised.exception)
        self.assertIn('params={"QueueUrl": {"$ref": "queue.url"}}', message)
        self.assertIn("unresolvable $ref", message)
        self.assertIn(f"at {SCENARIO_FILE} call", message)


# ─── The loader hook ──────────────────────────────────────────────────────────


class TestLoaderHook(unittest.TestCase):
    SCENARIO = {
        "version": 1, "service": "sqs", "client": CLIENT,
        "groups": [{
            "name": "sqs-gen-queue", "kind": "lifecycle",
            "setup": [], "teardown": [],
            "tests": [{"name": "CreateQueue", "op": "CreateQueue",
                       "call": {"op": "CreateQueue", "params": {}},
                       "assert": [{"kind": "responseField",
                                   "checks": {"$.QueueUrl": {"nonEmpty": True}}}]}],
        }],
    }

    def setUp(self) -> None:
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        os.makedirs(os.path.join(self.dir.name, "compat", "model", "scenarios"))
        self.rel = "compat/model/scenarios/sqs.json"
        with open(os.path.join(self.dir.name, *self.rel.split("/")), "w",
                  encoding="utf-8") as f:
            json.dump(self.SCENARIO, f)
        self.interp = ScenarioInterpreter(library=ScenarioLibrary(self.dir.name))

    def test_resolves_a_scenario_test(self):
        self.assertIsNotNone(self.interp.backend("sqs-gen-queue", "CreateQueue", self.rel))

    def test_returns_not_mine_for_anything_it_cannot_find(self):
        self.assertIsNone(self.interp.backend("sqs-queues", "CreateQueue", None))
        self.assertIsNone(self.interp.backend("sqs-gen-nope", "CreateQueue", self.rel))
        self.assertIsNone(self.interp.backend("sqs-gen-queue", "Nope", self.rel))
        absent = "compat/model/scenarios/absent.json"
        self.assertIn("cannot read scenario file", _stderr(
            lambda: self.assertIsNone(
                self.interp.backend("sqs-gen-queue", "CreateQueue", absent))))

    def test_hooks_register_setup_and_teardown_as_a_pair(self):
        registry = {"groups": [
            {"name": "sqs-queues", "service": "sqs", "tests": []},
            {"name": "sqs-gen-queue", "service": "sqs", "generated": True,
             "scenario": self.rel, "tests": []},
        ]}
        hooks = scenario_hooks(registry, self.interp)
        self.assertEqual(sorted(hooks.setup), ["sqs-gen-queue"])
        self.assertEqual(sorted(hooks.teardown), ["sqs-gen-queue"])

    def test_a_file_that_will_not_load_is_reported_once_not_raised(self):
        broken = os.path.join(self.dir.name, "compat", "model", "scenarios", "broken.json")
        with open(broken, "w", encoding="utf-8") as f:
            f.write("{not json")
        rel = "compat/model/scenarios/broken.json"
        errors = _stderr(lambda: self.interp.backend("sqs-gen-queue", "CreateQueue", rel))
        self.assertIn("cannot read scenario file", errors)
        # Cached: a second resolution does not re-read or re-report.
        self.assertEqual(_stderr(lambda: self.interp.backend("g", "t", rel)), "")


def _stderr(fn) -> str:
    """Run `fn` and return what it wrote to stderr — the interpreter's own
    diagnostics channel (`ctx.log`), which never goes to stdout because the
    runner parses stdout as NDJSON."""
    import contextlib
    import io

    buffer = io.StringIO()
    with contextlib.redirect_stderr(buffer):
        fn()
    return buffer.getvalue()


def _quiet(fn):
    """Run `fn`, discarding the diagnostics it writes to stderr."""
    _stderr(fn)


# ─── The real corpus ──────────────────────────────────────────────────────────


class TestPilotCorpus(unittest.TestCase):
    """The scenario files this suite actually executes, checked structurally.

    Not a substitute for the real run — it is the cheap half: that every group
    the generated registry scopes to python-sdk resolves to a scenario group,
    every test resolves to a scenario test, and every operation the corpus
    names exists on the boto3 client the client spec derives."""

    def setUp(self) -> None:
        from lib.registry import load_generated_registry

        self.registry = load_generated_registry()
        if not self.registry["groups"]:
            self.skipTest("registry.generated.json is empty")
        self.interp = ScenarioInterpreter()

    def test_every_scoped_group_and_test_resolves(self):
        for group in self.registry["groups"]:
            if "python-sdk" not in group["suites"]:
                continue
            spec = self.interp.group_spec(group.get("scenario"), group["name"])
            self.assertIsNotNone(spec, msg=group["name"])
            for test in group["tests"]:
                self.assertIsNotNone(
                    self.interp.backend(group["name"], test["name"], group["scenario"]),
                    msg=f"{group['name']}/{test['name']}",
                )

    def test_every_operation_exists_on_the_derived_boto3_client(self):
        import botocore.session
        from botocore import xform_name

        session = botocore.session.get_session()
        seen: set[tuple[str, str]] = set()
        for group in self.registry["groups"]:
            spec = self.interp.group_spec(group.get("scenario"), group["name"])
            service = spec.client["endpointPrefix"]
            model = session.get_service_model(service)
            for op in _operations(spec):
                if (service, op) in seen:
                    continue
                seen.add((service, op))
                self.assertIn(op, model.operation_names, msg=f"{service}:{op}")
                self.assertEqual(xform_name(op), xform_name(op).lower())
        self.assertTrue(seen)


def _operations(spec: ScenarioGroup) -> set[str]:
    found: set[str] = set()

    def walk(node):
        if isinstance(node, dict):
            if isinstance(node.get("op"), str):
                found.add(node["op"])
            for value in node.values():
                walk(value)
        elif isinstance(node, list):
            for value in node:
                walk(value)

    walk({"setup": spec.setup, "tests": spec.tests, "teardown": spec.teardown})
    return found


if __name__ == "__main__":  # pragma: no cover
    unittest.main()
