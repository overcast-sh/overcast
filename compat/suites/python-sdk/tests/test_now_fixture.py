"""
The shared `$now` conformance fixture, compat/model/testdata/now.

`$now` is the client's clock when a call is made, in epoch milliseconds, plus
an offset (compat/model/README.md § Values). This is where python-sdk proves it
against the same cases every other suite reads: each valid spelling evaluates
to the fixture's value with the clock pinned to its instant, each invalid one
is refused, the clock is read once per call however many `$now`s the params
hold, and a `$now` is never evaluated outside a call's params.

Run with:  python -m unittest discover -s tests  (from compat/suites/python-sdk/)
"""

from __future__ import annotations

import json
import os
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from lib.registry import REPO_ROOT  # noqa: E402
from lib.scenario import executor as executor_module  # noqa: E402
from lib.scenario.expressions import check_now_arguments, evaluate  # noqa: E402
from lib.scenario.failures import ScenarioError, ScenarioFailure  # noqa: E402

from test_scenario import FakeClient, make_ctx, make_group, make_interpreter  # noqa: E402

FIXTURE = os.path.join(REPO_ROOT, "compat", "model", "testdata", "now", "now.json")


def load_fixture() -> dict:
    with open(FIXTURE, encoding="utf-8") as fh:
        fixture = json.load(fh)
    unknown = set(fixture) - {"$comment", "instant", "valid", "invalid", "invalidArguments", "call"}
    if unknown:
        raise AssertionError(f"unknown keys in {FIXTURE}: {sorted(unknown)}")
    if not fixture["valid"] or not fixture["invalid"] or not fixture["invalidArguments"]:
        raise AssertionError("the $now fixture may not be skipped by emptying it")
    return fixture


def ev(value, now_ms):
    return evaluate(value, context={}, run_id="oc", group="g", now_ms=now_ms)


class TestSharedNowFixture(unittest.TestCase):
    def test_valid_spellings_evaluate_against_the_pinned_clock(self):
        fixture = load_fixture()
        for case in fixture["valid"]:
            with self.subTest(case["name"]):
                self.assertEqual(ev({"$now": case["now"]}, fixture["instant"]), case["value"])

    def test_invalid_spellings_are_refused(self):
        fixture = load_fixture()
        for case in fixture["invalid"]:
            with self.subTest(case["name"]):
                with self.assertRaises(ScenarioError):
                    ev({"$now": case["now"]}, fixture["instant"])

    def test_invalid_arguments_are_refused(self):
        for case in load_fixture()["invalidArguments"]:
            with self.subTest(case["name"]):
                with self.assertRaises(ScenarioError):
                    check_now_arguments(case["unit"], case["offsetMillis"])

    def test_a_now_is_never_an_expected_value(self):
        # No reading is handed to anything but a call's params.
        with self.assertRaises(ScenarioError):
            ev({"$now": {"unit": "epochMillis"}}, None)

    def test_the_clock_is_read_once_per_call(self):
        fixture = load_fixture()
        call = fixture["call"]
        readings = iter(range(fixture["instant"], fixture["instant"] + 100 * call["tickMillis"],
                              call["tickMillis"]))
        spec = make_group(name="logs-events", tests=[
            {"name": "PutLogEvents", "op": "PutLogEvents",
             "call": {"op": "PutLogEvents", "params": call["params"]},
             "assert": [{"kind": "responseField",
                         "checks": {"$.nextSequenceToken": {"nonEmpty": True}}}]},
        ])
        client = FakeClient({"put_log_events": [{"nextSequenceToken": "t"}]})
        interp = make_interpreter(client)
        ctx = make_ctx()
        with mock.patch.object(executor_module, "clock_ms", side_effect=lambda: next(readings)):
            interp.run_setup(spec, ctx)
            interp.run_test(spec, "PutLogEvents", ctx)
        self.assertEqual(client.params_for("put_log_events"), [call["sent"]])

    def test_a_now_in_an_equals_fails_rather_than_reading_the_clock(self):
        spec = make_group(name="logs-events", tests=[
            {"name": "GetLogEvents", "op": "GetLogEvents",
             "call": {"op": "GetLogEvents", "params": {"logStreamName": "s"}},
             "assert": [{"kind": "responseField",
                         "checks": {"$.events[0].timestamp":
                                    {"equals": {"$now": {"unit": "epochMillis"}}}}}]},
        ])
        client = FakeClient({"get_log_events": [{"events": [{"timestamp": 1}]}]})
        interp = make_interpreter(client)
        ctx = make_ctx()
        interp.run_setup(spec, ctx)
        with self.assertRaises((ScenarioError, ScenarioFailure)):
            interp.run_test(spec, "GetLogEvents", ctx)


if __name__ == "__main__":
    unittest.main()
