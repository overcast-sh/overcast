"""
The shared blob-value conformance fixture, compat/model/testdata/blobs.

Every backend agrees on one document form for a blob — its canonical standard
base64 text (compat/model/README.md § Values) — and this is where python-sdk
proves it against the same cases every other suite reads: `$base64` decodes to
exactly the fixture's bytes, boto3's ``bytes`` render back to exactly that
text, an ``equals`` against the `$base64` holds, and every spelling the fixture
calls invalid is refused rather than decoded into something else. The second
half runs a blob round trip through the interpreter against a fake client:
bytes go to boto3, an exported blob sits in the bag as text, and a `$base64`
around a `$ref` to it hands boto3 the same bytes again.

Run with:  python -m unittest discover -s tests  (from compat/suites/python-sdk/)
"""

from __future__ import annotations

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from lib.registry import REPO_ROOT  # noqa: E402
from lib.scenario.expressions import (  # noqa: E402
    decode_base64,
    evaluate,
    json_equal,
    to_document,
)
from lib.scenario.failures import ScenarioError, ScenarioFailure, render  # noqa: E402

from test_scenario import FakeClient, make_ctx, make_group, make_interpreter, bag  # noqa: E402

FIXTURE = os.path.join(REPO_ROOT, "compat", "model", "testdata", "blobs", "blobs.json")


def load_fixture() -> dict:
    with open(FIXTURE, encoding="utf-8") as fh:
        fixture = json.load(fh)
    unknown = set(fixture) - {"$comment", "valid", "invalid"}
    if unknown:
        raise AssertionError(f"unknown keys in {FIXTURE}: {sorted(unknown)}")
    if not fixture["valid"] or not fixture["invalid"]:
        raise AssertionError("the blob fixture may not be skipped by emptying it")
    return fixture


def ev(value, context=None):
    return evaluate(value, context=context or {}, run_id="oc", group="g")


class TestSharedBlobFixture(unittest.TestCase):
    def test_valid_cases_decode_and_render_back(self):
        for case in load_fixture()["valid"]:
            with self.subTest(case["name"]):
                want = bytes.fromhex(case["hex"])
                self.assertEqual(ev({"$base64": case["base64"]}), want)
                self.assertEqual(
                    ev({"$base64": {"$ref": "rec.data"}}, {"rec.data": case["base64"]}), want)
                self.assertEqual(to_document({"Data": want}), {"Data": case["base64"]})
                self.assertTrue(json_equal(case["base64"], ev({"$base64": case["base64"]})))

    def test_invalid_cases_are_refused(self):
        for case in load_fixture()["invalid"]:
            with self.subTest(case["name"]):
                with self.assertRaises(ScenarioError):
                    decode_base64(case["base64"])
                with self.assertRaises(ScenarioError):
                    ev({"$base64": {"$ref": "rec.data"}}, {"rec.data": case["base64"]})

    def test_a_blob_renders_as_its_base64_text_in_a_message(self):
        self.assertEqual(render({"Data": b"record-1"}), '{"Data": "cmVjb3JkLTE="}')


class TestBlobRoundTrip(unittest.TestCase):
    def test_bytes_go_out_text_comes_back_and_a_ref_decodes_it(self):
        spec = make_group(name="kinesis-records", tests=[
            {"name": "Put", "op": "PutRecord",
             "call": {"op": "PutRecord", "params": {"Data": {"$base64": "cmVjb3JkLTE="}},
                      "export": {"rec.data": "$.Echo"}},
             "assert": [{"kind": "responseField",
                         "checks": {"$.Echo": {"equals": {"$base64": "cmVjb3JkLTE="}}}}]},
            {"name": "Again", "op": "PutRecord",
             "call": {"op": "PutRecord", "params": {"Data": {"$base64": {"$ref": "rec.data"}}}},
             "assert": [{"kind": "responseField",
                         "checks": {"$.Echo": {"equals": {"$base64": {"$ref": "rec.data"}}}}}]},
        ])
        client = FakeClient({"put_record": [{"Echo": b"record-1"}]})
        interp = make_interpreter(client)
        ctx = make_ctx()
        interp.run_setup(spec, ctx)
        interp.run_test(spec, "Put", ctx)
        self.assertEqual(bag(ctx)["rec.data"], "cmVjb3JkLTE=")
        interp.run_test(spec, "Again", ctx)
        self.assertEqual(client.params_for("put_record"),
                         [{"Data": b"record-1"}, {"Data": b"record-1"}])

    def test_a_different_blob_fails_equals_and_the_message_shows_text(self):
        spec = make_group(name="kinesis-records", tests=[
            {"name": "Put", "op": "PutRecord",
             "call": {"op": "PutRecord", "params": {"Data": {"$base64": "cmVjb3JkLTE="}}},
             "assert": [{"kind": "responseField",
                         "checks": {"$.Echo": {"equals": {"$base64": "cmVjb3JkLTI="}}}}]},
        ])
        client = FakeClient({"put_record": [{"Echo": b"record-1"}]})
        interp = make_interpreter(client)
        ctx = make_ctx()
        with self.assertRaises(ScenarioFailure) as caught:
            interp.run_test(spec, "Put", ctx)
        message = str(caught.exception)
        self.assertIn('"cmVjb3JkLTE="', message)
        self.assertIn('"cmVjb3JkLTI="', message)


if __name__ == "__main__":
    unittest.main()
